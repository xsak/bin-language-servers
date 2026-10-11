// Package json is the JSON / JSONC language module: syntax errors, JSON Schema validation,
// completion, hover, outline and formatting.
package json

import (
	"context"
	"strings"
	"time"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/jsonast"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
)

func init() {
	lang.Register(lang.Info{
		Name:        "json",
		Description: "JSON and JSONC with JSON Schema (SchemaStore, $schema, settings)",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	schemalang.Base
}

func (s *Server) Name() string { return "json" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	return s.InitBase(env, "json", schema.ForJSON)
}

func (s *Server) TriggerCharacters() []string { return []string{`"`, ":"} }

// jsonc reports whether comments and trailing commas are allowed.
func jsonc(doc *document.Doc) bool {
	if doc.LanguageID == "jsonc" || doc.LanguageID == "json5" {
		return true
	}
	p := doc.Path()
	for _, suffix := range []string{".jsonc", "tsconfig.json", "jsconfig.json", ".code-workspace",
		"/.vscode/settings.json", "/zed/settings.json", "/zed/keymap.json", "/gram/settings.json",
		"/gram/keymap.json", ".eslintrc.json", "devcontainer.json"} {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

func (s *Server) parse(doc *document.Doc) *jsonast.Result {
	return jsonast.Parse(doc.Text, jsonast.Options{Comments: jsonc(doc)})
}

// schemaFor picks the schema: "$schema" in the document, then settings, then SchemaStore.
func (s *Server) schemaFor(ctx context.Context, doc *document.Doc, root *tree.Node, d time.Duration) *schema.Schema {
	if ref := root.StringProp("$schema"); ref != "" {
		return s.Load(ctx, schemalang.ResolveRef(doc, ref), d)
	}
	return s.Load(ctx, s.ResolveFile(ctx, doc, d), d)
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	res := s.parse(doc)
	var out []protocol.Diagnostic
	for _, p := range res.Problems {
		sev := protocol.SeverityError
		if p.Warning {
			sev = protocol.SeverityWarning
		}
		out = append(out, schemalang.Diagnostic(doc, p.Start, p.End, sev, p.Message))
	}
	if res.Root == nil || !s.Settings().Validate {
		return out
	}
	if sch := s.schemaFor(ctx, doc, res.Root, schemalang.DiagnoseWait); sch != nil {
		out = append(out, schemalang.SchemaDiagnostics(doc, sch.Validate(res.Root))...)
	}
	return out
}

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	res := s.parse(doc)
	if res.Root == nil {
		return nil, nil
	}
	loc, ok := res.Root.At(doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	start, end := 0, 0
	switch {
	case loc.OnKey:
		start, end = loc.Prop.KeyStart, loc.Prop.KeyEnd
	case loc.Node != nil && loc.Node.Kind != tree.Object && loc.Node.Kind != tree.Array:
		start, end = loc.Node.Start, loc.Node.End
	default:
		return nil, nil
	}
	sch := s.schemaFor(ctx, doc, res.Root, schemalang.FeatureWait)
	return schemalang.Hover(doc, sch, loc.Path, res.Root.ToValue(), start, end), nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	return schemalang.Symbols(doc, s.parse(doc).Root), nil
}

func (s *Server) Format(_ context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	if !s.Settings().Format {
		return nil, nil
	}
	indent := "\t"
	if opts.InsertSpaces {
		indent = strings.Repeat(" ", int(max(opts.TabSize, 1)))
	}
	eol := "\n"
	if strings.Contains(doc.Text, "\r\n") {
		eol = "\r\n"
	}
	out, ok := jsonast.Format(doc.Text, jsonast.FormatOptions{
		Indent: indent, EOL: eol,
		FinalNewline: opts.InsertFinalNewline || strings.HasSuffix(doc.Text, "\n"),
	})
	if !ok || out == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: out}}, nil
}
