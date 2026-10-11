// Package server implements the LSP lifecycle and document synchronization, and routes language
// feature requests to a lang.Language module.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/jsonrpc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// DefaultDebounce is the delay between the last edit and running diagnostics.
const DefaultDebounce = 200 * time.Millisecond

// Options configures a Server.
type Options struct {
	Version  string
	Logger   *slog.Logger
	Debounce time.Duration
	CacheDir string // root cache directory; the module gets CacheDir/<name>
	// OnExit is called when the client sends "exit". code is 0 after a proper shutdown.
	OnExit func(code int)
}

type state int

const (
	stateUninitialized state = iota
	stateRunning
	stateShutdown
)

// Server serves one language module over one connection.
type Server struct {
	lang lang.Language
	opts Options
	log  *slog.Logger
	conn *jsonrpc.Conn

	mu     sync.Mutex
	state  state
	store  *document.Store
	timers map[protocol.DocumentURI]*time.Timer

	diagMu  sync.Mutex
	diagGen map[protocol.DocumentURI]uint64 // drops results from superseded runs
}

// New creates a server for l. Call Serve to run it.
func New(l lang.Language, opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Debounce == 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.OnExit == nil {
		opts.OnExit = func(int) {}
	}
	return &Server{
		lang:    l,
		opts:    opts,
		log:     opts.Logger.With("lang", l.Name()),
		timers:  map[protocol.DocumentURI]*time.Timer{},
		diagGen: map[protocol.DocumentURI]uint64{},
	}
}

// Serve runs the server on conn until the stream ends.
func (s *Server) Serve(ctx context.Context, conn *jsonrpc.Conn) error {
	s.conn = conn
	return conn.Serve(ctx, s)
}

// Handle implements jsonrpc.Handler for requests.
func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()

	switch {
	case method == "initialize":
		if st != stateUninitialized {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "already initialized")
		}
		return s.initialize(ctx, params)
	case st == stateUninitialized:
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerNotInitialized, "server not initialized")
	case st == stateShutdown:
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "server is shutting down")
	case method == "shutdown":
		s.mu.Lock()
		s.state = stateShutdown
		for _, t := range s.timers {
			t.Stop()
		}
		s.mu.Unlock()
		return nil, nil
	}

	switch method {
	case "textDocument/completion":
		var p protocol.CompletionParams
		return withDoc(s, params, &p, &p.TextDocument, func(f lang.Completer, d *document.Doc) (any, error) {
			return f.Complete(ctx, d, p.Position)
		})
	case "textDocument/hover":
		var p protocol.HoverParams
		return withDoc(s, params, &p, &p.TextDocument, func(f lang.Hoverer, d *document.Doc) (any, error) {
			return f.Hover(ctx, d, p.Position)
		})
	case "textDocument/definition":
		var p protocol.DefinitionParams
		return withDoc(s, params, &p, &p.TextDocument, func(f lang.Definer, d *document.Doc) (any, error) {
			return f.Definition(ctx, d, p.Position)
		})
	case "textDocument/documentSymbol":
		var p protocol.DocumentSymbolParams
		return withDoc(s, params, &p, &p.TextDocument, func(f lang.Symboler, d *document.Doc) (any, error) {
			return f.Symbols(ctx, d)
		})
	case "textDocument/formatting":
		var p protocol.DocumentFormattingParams
		return withDoc(s, params, &p, &p.TextDocument, func(f lang.Formatter, d *document.Doc) (any, error) {
			return f.Format(ctx, d, p.Options)
		})
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "method not supported: %s", method)
}

// withDoc decodes params, looks up the document and the feature interface F, and calls fn.
// A missing document or feature yields a null result rather than an error, which editors
// handle more gracefully.
func withDoc[F any](s *Server, raw json.RawMessage, params any, td *protocol.TextDocumentIdentifier,
	fn func(F, *document.Doc) (any, error)) (any, error) {
	if err := json.Unmarshal(raw, params); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
	}
	f, ok := s.lang.(F)
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "not supported by %s", s.lang.Name())
	}
	d := s.store.Get(td.URI)
	if d == nil {
		return nil, nil
	}
	return fn(f, d)
}

func (s *Server) initialize(ctx context.Context, raw json.RawMessage) (any, error) {
	var p protocol.InitializeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
	}

	enc := protocol.PositionEncodingUTF16
	if p.Capabilities.General != nil {
		// Prefer UTF-8 (cheapest for Go strings) when the client offers it.
		if slices.Contains(p.Capabilities.General.PositionEncodings, protocol.PositionEncodingUTF8) {
			enc = protocol.PositionEncodingUTF8
		}
	}

	env := lang.Env{
		InitOptions: p.InitializationOptions,
		Logger:      s.log,
		Client:      client{s},
	}
	for _, f := range p.WorkspaceFolders {
		if path := document.URIToPath(f.URI); path != "" {
			env.WorkspaceFolders = append(env.WorkspaceFolders, path)
		}
	}
	if len(env.WorkspaceFolders) == 0 {
		if path := document.URIToPath(p.RootURI); path != "" {
			env.WorkspaceFolders = []string{path}
		}
	}
	if len(env.WorkspaceFolders) > 0 {
		env.RootPath = env.WorkspaceFolders[0]
	}
	if s.opts.CacheDir != "" {
		env.CacheDir = filepath.Join(s.opts.CacheDir, s.lang.Name())
	}
	if td := p.Capabilities.TextDocument; td != nil {
		if td.Completion != nil && td.Completion.CompletionItem != nil {
			env.SnippetSupport = td.Completion.CompletionItem.SnippetSupport
		}
		if td.Hover != nil {
			env.MarkdownSupported = slices.Contains(td.Hover.ContentFormat, protocol.MarkupMarkdown)
		}
	}

	if err := s.lang.Init(ctx, env); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "init %s: %v", s.lang.Name(), err)
	}

	s.mu.Lock()
	s.store = document.NewStore(enc)
	s.state = stateRunning
	s.mu.Unlock()

	if p.ClientInfo != nil {
		s.log.Info("initialize", "client", p.ClientInfo.Name, "clientVersion", p.ClientInfo.Version,
			"encoding", enc, "root", env.RootPath)
	}
	return protocol.InitializeResult{
		Capabilities: s.capabilities(enc),
		ServerInfo:   &protocol.ServerInfo{Name: "bls-" + s.lang.Name(), Version: s.opts.Version},
	}, nil
}

func (s *Server) capabilities(enc string) protocol.ServerCapabilities {
	caps := protocol.ServerCapabilities{
		PositionEncoding: enc,
		TextDocumentSync: &protocol.TextDocumentSyncOptions{
			OpenClose: true,
			Change:    protocol.SyncIncremental,
			Save:      &protocol.SaveOptions{},
		},
	}
	l := s.lang
	if _, ok := l.(lang.Completer); ok {
		caps.CompletionProvider = &protocol.CompletionOptions{}
		if t, ok := l.(lang.TriggerCharacterer); ok {
			caps.CompletionProvider.TriggerCharacters = t.TriggerCharacters()
		}
	}
	_, caps.HoverProvider = l.(lang.Hoverer)
	_, caps.DefinitionProvider = l.(lang.Definer)
	_, caps.DocumentSymbolProvider = l.(lang.Symboler)
	_, caps.DocumentFormattingProvider = l.(lang.Formatter)
	return caps
}

// Notify implements jsonrpc.Handler for notifications. It runs on the read goroutine, so
// document edits are applied in order.
func (s *Server) Notify(ctx context.Context, method string, params json.RawMessage) {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()

	if method == "exit" {
		code := 1
		if st == stateShutdown {
			code = 0
		}
		s.opts.OnExit(code)
		return
	}
	if st != stateRunning {
		return
	}

	switch method {
	case "initialized":
	case "textDocument/didOpen":
		var p protocol.DidOpenTextDocumentParams
		if s.decode(method, params, &p) {
			d := s.store.Open(p.TextDocument)
			s.scheduleDiagnostics(d.URI, 0)
		}
	case "textDocument/didChange":
		var p protocol.DidChangeTextDocumentParams
		if s.decode(method, params, &p) {
			if _, err := s.store.Change(p); err != nil {
				s.log.Warn("didChange", "err", err)
				return
			}
			s.scheduleDiagnostics(p.TextDocument.URI, s.opts.Debounce)
		}
	case "textDocument/didSave":
		var p protocol.DidSaveTextDocumentParams
		if s.decode(method, params, &p) {
			if sl, ok := s.lang.(lang.SaveListener); ok {
				if d := s.store.Get(p.TextDocument.URI); d != nil {
					go sl.DidSave(ctx, d)
				}
			}
		}
	case "textDocument/didClose":
		var p protocol.DidCloseTextDocumentParams
		if s.decode(method, params, &p) {
			s.store.Close(p.TextDocument.URI)
			s.mu.Lock()
			if t := s.timers[p.TextDocument.URI]; t != nil {
				t.Stop()
				delete(s.timers, p.TextDocument.URI)
			}
			s.mu.Unlock()
			s.diagMu.Lock()
			delete(s.diagGen, p.TextDocument.URI)
			s.diagMu.Unlock()
			if _, ok := s.lang.(lang.Diagnoser); ok {
				s.publish(p.TextDocument.URI, nil, []protocol.Diagnostic{})
			}
		}
	case "workspace/didChangeConfiguration":
		var p protocol.DidChangeConfigurationParams
		if s.decode(method, params, &p) {
			if c, ok := s.lang.(lang.Configurable); ok {
				if err := c.Configure(p.Settings); err != nil {
					s.log.Warn("configure", "err", err)
				}
				s.refreshDiagnostics()
			}
		}
	default:
		s.log.Debug("ignored notification", "method", method)
	}
}

func (s *Server) decode(method string, raw json.RawMessage, v any) bool {
	if err := json.Unmarshal(raw, v); err != nil {
		s.log.Warn("bad params", "method", method, "err", err)
		return false
	}
	return true
}

func (s *Server) scheduleDiagnostics(uri protocol.DocumentURI, delay time.Duration) {
	if _, ok := s.lang.(lang.Diagnoser); !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[uri]; t != nil {
		t.Stop()
	}
	s.timers[uri] = time.AfterFunc(delay, func() { s.runDiagnostics(uri) })
}

func (s *Server) runDiagnostics(uri protocol.DocumentURI) {
	d := s.store.Get(uri)
	if d == nil {
		return
	}
	s.diagMu.Lock()
	s.diagGen[uri]++
	gen := s.diagGen[uri]
	s.diagMu.Unlock()

	diags := s.lang.(lang.Diagnoser).Diagnose(context.Background(), d)
	if diags == nil {
		diags = []protocol.Diagnostic{}
	}

	s.diagMu.Lock()
	stale := s.diagGen[uri] != gen
	s.diagMu.Unlock()
	if stale {
		return
	}
	v := d.Version
	s.publish(uri, &v, diags)
}

func (s *Server) refreshDiagnostics() {
	for _, d := range s.store.All() {
		s.scheduleDiagnostics(d.URI, 0)
	}
}

func (s *Server) publish(uri protocol.DocumentURI, version *int32, diags []protocol.Diagnostic) {
	err := s.conn.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
		URI: uri, Version: version, Diagnostics: diags,
	})
	if err != nil {
		s.log.Warn("publishDiagnostics", "err", err)
	}
}

// client implements lang.Client.
type client struct{ s *Server }

func (c client) LogMessage(typ protocol.MessageType, msg string) {
	_ = c.s.conn.Notify("window/logMessage", protocol.LogMessageParams{Type: typ, Message: msg})
}

func (c client) RefreshDiagnostics() { c.s.refreshDiagnostics() }

// DefaultCacheDir returns $XDG_CACHE_HOME/bls (or the OS equivalent).
func DefaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "bls")
}
