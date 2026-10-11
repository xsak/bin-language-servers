// Package toml is the TOML language module: syntax and structure errors, JSON Schema validation
// (a "#:schema" line, settings or the SchemaStore catalog: pyproject.toml, Cargo.toml, ruff.toml,
// ...), completion, hover, outline and formatting.
package toml

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
)

func init() {
	lang.Register(lang.Info{
		Name:        "toml",
		Description: "TOML with JSON Schema (SchemaStore: pyproject, Cargo, ruff, ...), completion, formatting",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	schemalang.Base
}

func (s *Server) Name() string { return "toml" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	return s.InitBase(env, "toml", schema.ForTOML)
}

func (s *Server) TriggerCharacters() []string { return []string{".", "=", " ", "["} }

// schemaDirective is taplo's "#:schema <url>" at the top of a file.
var schemaDirective = regexp.MustCompile(`(?m)^#:schema\s+(\S+)`)

func (s *Server) schemaFor(ctx context.Context, doc *document.Doc, wait time.Duration) *schema.Schema {
	if m := schemaDirective.FindStringSubmatch(doc.Text); m != nil {
		return s.Load(ctx, schemalang.ResolveRef(doc, m[1]), wait)
	}
	return s.Load(ctx, s.ResolveFile(ctx, doc, wait), wait)
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	res := Parse(doc.Text)
	var out []protocol.Diagnostic
	for _, p := range res.Problems {
		out = append(out, schemalang.Diagnostic(doc, p.Start, p.End, protocol.SeverityError, p.Msg))
	}
	if !s.Settings().Validate {
		return out
	}
	if sch := s.schemaFor(ctx, doc, schemalang.DiagnoseWait); sch != nil {
		out = append(out, schemalang.SchemaDiagnostics(doc, sch.Validate(res.Root))...)
	}
	return out
}

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	res := Parse(doc.Text)
	off := doc.Offset(pos)
	// Keys in table headers: [tool.ruff]
	for _, h := range res.Headers {
		if off >= h.Start && off <= h.End {
			path := headerPathAt(doc.Text, h, off)
			return schemalang.Hover(doc, s.schemaFor(ctx, doc, schemalang.FeatureWait), path, res.Root.ToValue(), h.Start, h.End), nil
		}
	}
	loc, ok := res.Root.At(off)
	if !ok {
		return nil, nil
	}
	var start, end int
	switch {
	case loc.OnKey:
		start, end = loc.Prop.KeyStart, loc.Prop.KeyEnd
	case loc.Node != nil && loc.Node.Kind != tree.Object && loc.Node.Kind != tree.Array:
		start, end = loc.Node.Start, loc.Node.End
	default:
		return nil, nil
	}
	return schemalang.Hover(doc, s.schemaFor(ctx, doc, schemalang.FeatureWait), loc.Path, res.Root.ToValue(), start, end), nil
}

// headerPathAt returns the header's path up to the key segment under the cursor.
func headerPathAt(src string, h Header, off int) tree.Path {
	var path tree.Path
	text := src[h.Start:h.End]
	seg := strings.Count(text[:min(off-h.Start, len(text))], ".")
	for i, k := range h.Path {
		if i > seg {
			break
		}
		path = append(path, tree.KeySeg(k))
		if h.Array && i == len(h.Path)-1 && i < seg {
			path = append(path, tree.IndexSeg(-1))
		}
	}
	return path
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	return schemalang.Symbols(doc, Parse(doc.Text).Root), nil
}

// --- completion ---

// tablePath returns the table that line ln belongs to, from the last header above it.
func tablePath(lines []string, ln int) tree.Path {
	for l := ln - 1; l >= 0; l-- {
		t := strings.TrimSpace(lines[l])
		if !strings.HasPrefix(t, "[") {
			continue
		}
		array := strings.HasPrefix(t, "[[")
		inner := strings.TrimLeft(t, "[")
		if i := strings.Index(inner, "]"); i >= 0 {
			inner = inner[:i]
		}
		var path tree.Path
		for _, k := range splitKey(inner) {
			path = append(path, tree.KeySeg(k))
		}
		if array {
			path = append(path, tree.IndexSeg(-1))
		}
		return path
	}
	return nil
}

// splitKey splits a dotted key, honoring quotes.
func splitKey(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, strings.TrimSpace(cur.String()))
}

func (s *Server) Complete(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	sch := s.schemaFor(ctx, doc, schemalang.FeatureWait)
	if sch == nil {
		return nil, nil
	}
	res := Parse(doc.Text)
	value := res.Root.ToValue()
	lines := strings.Split(doc.Text, "\n")
	ln := int(pos.Line)
	if ln >= len(lines) {
		return nil, nil
	}
	off := doc.Offset(pos)
	lineStart := doc.Offset(protocol.Position{Line: pos.Line})
	before := lines[ln][:min(off-lineStart, len(lines[ln]))]
	trimmed := strings.TrimLeft(before, " \t")
	snippets := s.Env.SnippetSupport

	var items []protocol.CompletionItem
	var start int
	switch {
	case strings.HasPrefix(trimmed, "["):
		// Table header: complete the next key segment.
		inner := strings.TrimLeft(trimmed, "[")
		parts := splitKey(inner)
		var path tree.Path
		for _, k := range parts[:len(parts)-1] {
			path = append(path, tree.KeySeg(k))
		}
		start = off - len(parts[len(parts)-1])
		for _, p := range sch.Properties(path, value) {
			items = append(items, protocol.CompletionItem{Label: p.Name, Kind: protocol.CompletionKindModule,
				Detail: strings.Join(p.Types, " | "), InsertText: p.Name, Documentation: markdown(p.Description)})
		}
	case strings.Contains(trimmed, "="):
		// Value after "key =".
		eq := strings.Index(trimmed, "=")
		path := tablePath(lines, ln)
		for _, k := range splitKey(strings.TrimSpace(trimmed[:eq])) {
			path = append(path, tree.KeySeg(k))
		}
		vstart := len(before) - len(strings.TrimLeft(trimmed[eq+1:], " \t"))
		start = lineStart + vstart
		items = schemalang.ValueItems(sch.Describe(sch.At(path, value)), renderValue)
	default:
		// Key position (possibly dotted: "tool.ruff.li").
		parts := splitKey(trimmed)
		path := tablePath(lines, ln)
		for _, k := range parts[:len(parts)-1] {
			path = append(path, tree.KeySeg(k))
		}
		start = off - len(parts[len(parts)-1])
		existing := map[string]bool{}
		if n := lookup(res.Root, path); n != nil {
			existing = schemalang.SortedKeys(n)
		}
		delete(existing, parts[len(parts)-1])
		items = schemalang.PropertyItems(sch.Properties(path, value), existing, func(p schema.PropertyInfo) (string, bool) {
			if !snippets {
				return p.Name + " = ", false
			}
			return p.Name + " = " + valueSnippet(p), true
		})
	}
	r := doc.Range(start, off)
	for i := range items {
		items[i].TextEdit = &protocol.TextEdit{Range: r, NewText: items[i].InsertText}
	}
	return &protocol.CompletionList{Items: items}, nil
}

func markdown(s string) *protocol.MarkupContent {
	if s == "" {
		return nil
	}
	return &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: s}
}

// lookup follows a path in the tree ("any item" picks the last one).
func lookup(n *tree.Node, path tree.Path) *tree.Node {
	for _, seg := range path {
		if n == nil {
			return nil
		}
		if seg.IsIndex {
			if n.Kind != tree.Array || len(n.Items) == 0 {
				return nil
			}
			n = n.Items[len(n.Items)-1]
			continue
		}
		p := n.Prop(seg.Key)
		if p == nil {
			return nil
		}
		n = p.Value
	}
	return n
}

func valueSnippet(p schema.PropertyInfo) string {
	if len(p.Enum) > 0 {
		choices := make([]string, len(p.Enum))
		for i, v := range p.Enum {
			choices[i] = schemalang.ChoiceEscape(renderValue(v))
		}
		return "${1|" + strings.Join(choices, ",") + "|}"
	}
	if p.Default != nil && *p.Default != nil {
		if d := renderValue(*p.Default); !strings.ContainsAny(d, "{[") {
			return "${1:" + schemalang.SnippetEscape(d) + "}"
		}
	}
	t := ""
	if len(p.Types) == 1 {
		t = p.Types[0]
	}
	switch t {
	case "string":
		return `"$1"`
	case "boolean":
		return "${1|true,false|}"
	case "integer", "number":
		return "${1:0}"
	case "array":
		return "[$1]"
	case "object":
		return "{ $1 }"
	}
	return "$1"
}

// renderValue writes a value as TOML.
func renderValue(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%q", x)
	case nil:
		return `""`
	}
	return schema.JSONLiteral(v)
}
