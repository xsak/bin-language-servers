// Package lang defines what a language module implements and keeps the registry of modules
// compiled into the binary.
//
// A module implements Language plus any of the optional feature interfaces. The server checks
// which interfaces are implemented and advertises only those capabilities to the client.
package lang

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// Env is what the server hands a module at initialization.
type Env struct {
	RootPath          string   // first workspace folder, may be empty
	WorkspaceFolders  []string // local paths
	InitOptions       json.RawMessage
	CacheDir          string // per-language cache directory, created on demand by the module
	Logger            *slog.Logger
	SnippetSupport    bool
	MarkdownSupported bool
	// Client lets a module talk back to the editor (log messages, configuration requests).
	Client Client
}

// Client is the subset of client calls a module may make.
type Client interface {
	LogMessage(typ protocol.MessageType, msg string)
	// RefreshDiagnostics re-runs diagnostics for every open document, e.g. after a schema
	// download finished or settings changed.
	RefreshDiagnostics()
}

// Language is the mandatory part of a module.
type Language interface {
	Name() string
	Init(ctx context.Context, env Env) error
}

// Optional feature interfaces.

type Diagnoser interface {
	Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic
}

type Completer interface {
	Complete(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error)
}

// TriggerCharacterer lets a Completer declare completion trigger characters.
type TriggerCharacterer interface {
	TriggerCharacters() []string
}

type Hoverer interface {
	Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error)
}

type Symboler interface {
	Symbols(ctx context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error)
}

type Formatter interface {
	Format(ctx context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error)
}

type Definer interface {
	Definition(ctx context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error)
}

// Referencer finds every use of the symbol at a position.
type Referencer interface {
	References(ctx context.Context, doc *document.Doc, pos protocol.Position, includeDecl bool) ([]protocol.Location, error)
}

// CodeActioner offers quick fixes for a range (diagnostics are the ones the client shows there).
type CodeActioner interface {
	CodeActions(ctx context.Context, doc *document.Doc, rng protocol.Range, diags []protocol.Diagnostic) ([]protocol.CodeAction, error)
}

// LinkedEditor returns ranges that are renamed together (textDocument/linkedEditingRange).
type LinkedEditor interface {
	LinkedEditingRanges(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.LinkedEditingRanges, error)
}

// Configurable receives workspace/didChangeConfiguration settings (the raw "settings" object).
type Configurable interface {
	Configure(settings json.RawMessage) error
}

// ConfigSectioner lets a Configurable module name the settings sections ("json", "yaml") the
// server requests with workspace/configuration after initialization. The answers are passed to
// Configure as one object: {"json": {...}}.
type ConfigSectioner interface {
	ConfigSections() []string
}

// SaveListener is notified on textDocument/didSave, e.g. to run slower external linters.
type SaveListener interface {
	DidSave(ctx context.Context, doc *document.Doc)
}

// Factory creates a fresh module instance.
type Factory func() Language

// Info describes a registered module.
type Info struct {
	Name        string
	Description string
	New         Factory
}

var (
	mu       sync.RWMutex
	registry = map[string]Info{}
)

// Register adds a module. It is meant to be called from a module package's init function and
// panics on duplicate names.
func Register(info Info) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[info.Name]; dup {
		panic(fmt.Sprintf("lang: duplicate registration of %q", info.Name))
	}
	registry[info.Name] = info
}

// Lookup returns the module registered under name.
func Lookup(name string) (Info, bool) {
	mu.RLock()
	defer mu.RUnlock()
	info, ok := registry[name]
	return info, ok
}

// All returns every registered module, sorted by name.
func All() []Info {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Info, 0, len(registry))
	for _, info := range registry {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
