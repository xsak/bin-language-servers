// Package schemalang holds what the schema-driven modules (json, yaml, and later compose and
// ansible) share: settings handling, the schema store and resolver, completion item building,
// and the document outline.
package schemalang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/tree"
)

// Timeouts for waiting on a schema that is still loading. Diagnostics never block for long:
// the store triggers a refresh when the schema arrives.
const (
	DiagnoseWait = 100 * time.Millisecond
	FeatureWait  = 5 * time.Second
)

// Base is embedded by schema-driven language modules.
type Base struct {
	Section  string // settings section: "json" or "yaml"
	Env      lang.Env
	Store    *schema.Store
	Resolver *schema.Resolver
}

// InitBase sets up the store, catalog and resolver. filter selects catalog entries.
func (b *Base) InitBase(env lang.Env, section string, filter func([]string) bool) error {
	b.Env, b.Section = env, section
	cacheDir := ""
	if env.CacheDir != "" {
		// One schema cache for all modules: ~/.cache/bls/schemas.
		cacheDir = filepath.Join(filepath.Dir(env.CacheDir), "schemas")
	}
	b.Store = schema.NewStore(schema.Options{
		CacheDir: cacheDir,
		Offline:  os.Getenv("BLS_OFFLINE") != "",
		Logger:   env.Logger,
		OnLoaded: func() {
			if env.Client != nil {
				env.Client.RefreshDiagnostics()
			}
		},
	})
	b.Resolver = &schema.Resolver{Catalog: schema.NewCatalog(b.Store, os.Getenv("BLS_SCHEMA_CATALOG")), Filter: filter}
	b.Resolver.SetSettings(schema.DefaultSettings())
	if len(env.InitOptions) > 0 {
		// Some clients put settings in initializationOptions.
		if err := b.Configure(env.InitOptions); err != nil {
			env.Logger.Debug("initializationOptions are not settings", "err", err)
		}
	}
	return nil
}

// Configure implements lang.Configurable.
func (b *Base) Configure(raw json.RawMessage) error {
	s, err := schema.ParseSettings(raw, b.Section, b.Env.RootPath, b.Store)
	if err != nil {
		return err
	}
	b.Resolver.SetSettings(s)
	return nil
}

// ConfigSections implements lang.ConfigSectioner.
func (b *Base) ConfigSections() []string { return []string{b.Section} }

// Settings returns the current settings.
func (b *Base) Settings() schema.Settings { return b.Resolver.Settings() }

// Load returns the schema at url, waiting at most wait. Missing, pending and failing schemas
// all return nil; failures are logged once by the store.
func (b *Base) Load(ctx context.Context, url string, wait time.Duration) *schema.Schema {
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	sch, err := b.Store.Get(ctx, url)
	if err != nil {
		return nil
	}
	return sch
}

// LoadErr is like Load but returns the error, so callers can fall back on ErrNotFound.
func (b *Base) LoadErr(ctx context.Context, url string, wait time.Duration) (*schema.Schema, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	return b.Store.Get(ctx, url)
}

// ResolveFile returns the schema URL for doc from settings and the catalog.
func (b *Base) ResolveFile(ctx context.Context, doc *document.Doc, wait time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	u, err := b.Resolver.Resolve(ctx, doc.Path())
	if err != nil && !errors.Is(err, schema.ErrPending) {
		b.Env.Logger.Debug("resolve schema", "uri", doc.URI, "err", err)
	}
	return u
}

// ResolveRef resolves a $schema value or modeline URL relative to the document's directory.
func ResolveRef(doc *document.Doc, ref string) string {
	dir := ""
	if p := doc.Path(); p != "" {
		dir = filepath.Dir(p)
	}
	return schema.ResolveURL(ref, dir)
}

// Diagnostic builds a protocol diagnostic from a byte range.
func Diagnostic(doc *document.Doc, start, end int, sev protocol.DiagnosticSeverity, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{Range: doc.Range(start, end), Severity: sev, Source: "bls", Message: msg}
}

// SchemaDiagnostics converts schema problems to warnings.
func SchemaDiagnostics(doc *document.Doc, probs []schema.Problem) []protocol.Diagnostic {
	out := make([]protocol.Diagnostic, 0, len(probs))
	for _, p := range probs {
		out = append(out, Diagnostic(doc, p.Start, p.End, protocol.SeverityWarning, p.Message))
	}
	return out
}

// Hover renders a schema description at path, or nil if the schema says nothing.
func Hover(doc *document.Doc, sch *schema.Schema, path tree.Path, value any, start, end int) *protocol.Hover {
	if sch == nil {
		return nil
	}
	schemas := sch.At(path, value)
	if len(schemas) == 0 {
		return nil
	}
	md := sch.Describe(schemas).Markdown()
	if md == "" {
		return nil
	}
	r := doc.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}
}

// Symbols builds the outline of a value tree.
func Symbols(doc *document.Doc, root *tree.Node) []protocol.DocumentSymbol {
	if root == nil {
		return nil
	}
	switch root.Kind {
	case tree.Object:
		return propSymbols(doc, root)
	case tree.Array:
		return itemSymbols(doc, root)
	}
	return nil
}

func propSymbols(doc *document.Doc, n *tree.Node) []protocol.DocumentSymbol {
	out := make([]protocol.DocumentSymbol, 0, len(n.Props))
	for _, p := range n.Props {
		end := p.KeyEnd
		sym := protocol.DocumentSymbol{Name: p.Key, Kind: protocol.SymbolKey}
		if sym.Name == "" {
			sym.Name = `""`
		}
		if p.Value != nil {
			end = max(end, p.Value.End)
			sym.Kind = symbolKind(p.Value)
			sym.Detail = detail(p.Value)
			sym.Children = Symbols(doc, p.Value)
		}
		sym.Range = doc.Range(p.KeyStart, end)
		sym.SelectionRange = doc.Range(p.KeyStart, p.KeyEnd)
		out = append(out, sym)
	}
	return out
}

func itemSymbols(doc *document.Doc, n *tree.Node) []protocol.DocumentSymbol {
	var out []protocol.DocumentSymbol
	for i, it := range n.Items {
		if it.Kind != tree.Object && it.Kind != tree.Array {
			continue
		}
		name := fmt.Sprintf("[%d]", i)
		for _, k := range []string{"name", "id", "key", "title"} {
			if v := it.StringProp(k); v != "" {
				name = v
				break
			}
		}
		r := doc.Range(it.Start, it.End)
		out = append(out, protocol.DocumentSymbol{Name: name, Kind: symbolKind(it), Range: r,
			SelectionRange: doc.Range(it.Start, min(it.End, it.Start+1)), Children: Symbols(doc, it)})
	}
	return out
}

func symbolKind(n *tree.Node) protocol.SymbolKind {
	switch n.Kind {
	case tree.Object:
		return protocol.SymbolModule
	case tree.Array:
		return protocol.SymbolArray
	case tree.String:
		return protocol.SymbolString
	case tree.Number:
		return protocol.SymbolNumber
	case tree.Bool:
		return protocol.SymbolBoolean
	default:
		return protocol.SymbolNull
	}
}

func detail(n *tree.Node) string {
	switch n.Kind {
	case tree.Object, tree.Array:
		return ""
	case tree.Null:
		return "null"
	}
	s := fmt.Sprint(n.Value)
	if len(s) > 40 {
		s = s[:37] + "..."
	}
	return s
}

// PropertyItems turns schema properties into completion items. insert builds the text for one
// property (format specific); existing keys are skipped.
func PropertyItems(props []schema.PropertyInfo, existing map[string]bool,
	insert func(p schema.PropertyInfo) (text string, snippet bool)) []protocol.CompletionItem {
	items := make([]protocol.CompletionItem, 0, len(props))
	for _, p := range props {
		if existing[p.Name] {
			continue
		}
		text, snippet := insert(p)
		it := protocol.CompletionItem{
			Label:      p.Name,
			Kind:       protocol.CompletionKindProperty,
			Detail:     strings.Join(p.Types, " | "),
			InsertText: text,
			SortText:   sortKey(p),
			FilterText: p.Name,
		}
		if snippet {
			it.InsertTextFormat = protocol.InsertTextSnippet
		}
		if p.Description != "" {
			it.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: p.Description}
		}
		if p.Deprecated {
			it.Detail = strings.TrimSpace("(deprecated) " + it.Detail)
		}
		items = append(items, it)
	}
	return items
}

func sortKey(p schema.PropertyInfo) string {
	switch {
	case p.Deprecated:
		return "2" + p.Name
	case p.Required:
		return "0" + p.Name
	}
	return "1" + p.Name
}

// ValueItems turns value suggestions into completion items; render formats a value.
func ValueItems(d schema.Description, render func(v any) string) []protocol.CompletionItem {
	vals := d.Values()
	items := make([]protocol.CompletionItem, 0, len(vals))
	for i, v := range vals {
		text := render(v)
		it := protocol.CompletionItem{Label: text, Kind: protocol.CompletionKindValue, InsertText: text,
			SortText: fmt.Sprintf("%04d", i)}
		for j, e := range d.Enum {
			if schema.JSONLiteral(e) == schema.JSONLiteral(v) && j < len(d.EnumDocs) && d.EnumDocs[j] != "" {
				it.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: d.EnumDocs[j]}
			}
		}
		if len(d.Enum) > 0 {
			it.Kind = protocol.CompletionKindEnum
		}
		items = append(items, it)
	}
	return items
}

// SnippetEscape escapes text for use inside an LSP snippet.
func SnippetEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `$`, `\$`, `}`, `\}`).Replace(s)
}

// ChoiceEscape escapes text for use inside a snippet choice ${1|a,b|}.
func ChoiceEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `$`, `\$`, `}`, `\}`, `,`, `\,`, `|`, `\|`).Replace(s)
}

// SortedKeys returns the keys of an object node as a set.
func SortedKeys(n *tree.Node) map[string]bool {
	out := map[string]bool{}
	if n != nil {
		for _, p := range n.Props {
			out[p.Key] = true
		}
	}
	return out
}
