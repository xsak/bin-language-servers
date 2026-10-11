// Package lsptest runs a language module in-process behind a real JSON-RPC connection, so
// tests exercise the same code path as the editor without spawning a process.
package lsptest

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/xsak/bin-language-servers/internal/jsonrpc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/server"
)

// Client is a test LSP client connected to an in-process server.
type Client struct {
	t    testing.TB
	conn *jsonrpc.Conn
	ctx  context.Context

	mu    sync.Mutex
	diags map[protocol.DocumentURI][]chan protocol.PublishDiagnosticsParams
	last  map[protocol.DocumentURI]protocol.PublishDiagnosticsParams
}

// Start launches l and returns a client that is not yet initialized.
func Start(t testing.TB, l lang.Language) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	srvIn, cliOut := io.Pipe()
	cliIn, srvOut := io.Pipe()

	srv := server.New(l, server.Options{Version: "test", Debounce: time.Millisecond, CacheDir: t.TempDir()})
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		_ = srv.Serve(ctx, jsonrpc.NewConn(srvIn, srvOut))
		_ = srvOut.Close()
	}()

	c := &Client{
		t: t, ctx: ctx,
		conn:  jsonrpc.NewConn(cliIn, cliOut),
		diags: map[protocol.DocumentURI][]chan protocol.PublishDiagnosticsParams{},
		last:  map[protocol.DocumentURI]protocol.PublishDiagnosticsParams{},
	}
	cliDone := make(chan struct{})
	go func() {
		defer close(cliDone)
		_ = c.conn.Serve(ctx, (*clientHandler)(c))
	}()

	t.Cleanup(func() {
		cancel()
		_ = cliOut.Close()
		<-srvDone
		_ = cliIn.Close()
		<-cliDone
	})
	return c
}

// Initialize performs initialize + initialized with typical Zed-like capabilities.
func (c *Client) Initialize(rootURI protocol.DocumentURI) protocol.InitializeResult {
	c.t.Helper()
	params := map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"clientInfo": map[string]any{
			"name": "lsptest",
		},
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"textDocument": map[string]any{
				"completion": map[string]any{"completionItem": map[string]any{"snippetSupport": true}},
				"hover":      map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
			},
		},
	}
	var res protocol.InitializeResult
	c.Call("initialize", params, &res)
	c.Notify("initialized", map[string]any{})
	return res
}

// Call sends a request and fails the test on error.
func (c *Client) Call(method string, params, result any) {
	c.t.Helper()
	if err := c.CallErr(method, params, result); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

// CallErr sends a request and returns its error.
func (c *Client) CallErr(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	return c.conn.Call(ctx, method, params, result)
}

// Notify sends a notification.
func (c *Client) Notify(method string, params any) {
	c.t.Helper()
	if err := c.conn.Notify(method, params); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

// Open sends didOpen and returns the URI.
func (c *Client) Open(uri protocol.DocumentURI, languageID, text string) protocol.DocumentURI {
	c.Notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: languageID, Version: 1, Text: text},
	})
	return uri
}

// Diagnostics waits for the next publishDiagnostics for uri that has version >= minVersion
// (or no version).
func (c *Client) Diagnostics(uri protocol.DocumentURI, minVersion int32) []protocol.Diagnostic {
	c.t.Helper()
	ch := make(chan protocol.PublishDiagnosticsParams, 16)
	c.mu.Lock()
	if p, ok := c.last[uri]; ok && (p.Version == nil || *p.Version >= minVersion) {
		c.mu.Unlock()
		return p.Diagnostics
	}
	c.diags[uri] = append(c.diags[uri], ch)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, w := range c.diags[uri] {
			if w == ch {
				c.diags[uri] = append(c.diags[uri][:i], c.diags[uri][i+1:]...)
				break
			}
		}
	}()

	timeout := time.After(10 * time.Second)
	for {
		select {
		case p := <-ch:
			if p.Version == nil || *p.Version >= minVersion {
				return p.Diagnostics
			}
		case <-timeout:
			c.t.Fatalf("timed out waiting for diagnostics on %s", uri)
			return nil
		}
	}
}

// Hover requests hover at line/char.
func (c *Client) Hover(uri protocol.DocumentURI, line, char uint32) *protocol.Hover {
	c.t.Helper()
	var h *protocol.Hover
	c.Call("textDocument/hover", pos(uri, line, char), &h)
	return h
}

// Complete requests completion at line/char.
func (c *Client) Complete(uri protocol.DocumentURI, line, char uint32) *protocol.CompletionList {
	c.t.Helper()
	var l *protocol.CompletionList
	c.Call("textDocument/completion", pos(uri, line, char), &l)
	return l
}

func pos(uri protocol.DocumentURI, line, char uint32) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position:     protocol.Position{Line: line, Character: char},
	}
}

type clientHandler Client

func (h *clientHandler) Handle(context.Context, string, json.RawMessage) (any, error) {
	return nil, nil
}

func (h *clientHandler) Notify(_ context.Context, method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p protocol.PublishDiagnosticsParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	h.mu.Lock()
	h.last[p.URI] = p
	defer h.mu.Unlock()
	for _, ch := range h.diags[p.URI] {
		select {
		case ch <- p:
		default:
		}
	}
}
