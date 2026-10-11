// Package css is the CSS language module: syntax errors, unknown properties, values, at-rules and
// pseudo-selectors, completion and hover from the embedded vscode-custom-data, selector
// specificity, outline and formatting. Its functions work on plain text so the HTML module can
// use them for <style> blocks and style="" attributes.
package css

import (
	"context"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "css",
		Description: "CSS: checks, completion and hover (MDN data), specificity, outline, formatting",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	Env lang.Env
}

func (s *Server) Name() string { return "css" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{":", "-", "@", "(", ".", "#"} }

// Diagnostics converts problems at offset base of doc (base > 0 for embedded CSS).
func Diagnostics(doc *document.Doc, probs []Problem, base int) []protocol.Diagnostic {
	out := make([]protocol.Diagnostic, 0, len(probs))
	for _, p := range probs {
		out = append(out, protocol.Diagnostic{Range: doc.Range(base+p.Start, base+p.End),
			Severity: protocol.DiagnosticSeverity(p.Severity), Source: "bls", Message: p.Msg})
	}
	return out
}

// Items converts completion candidates at offset base of doc.
func Items(doc *document.Doc, items []CompletionItem, base int) []protocol.CompletionItem {
	out := make([]protocol.CompletionItem, 0, len(items))
	for i, it := range items {
		ci := protocol.CompletionItem{Label: it.Label, Kind: it.Kind, Detail: it.Detail, FilterText: strings.TrimLeft(it.Label, ":@"),
			TextEdit: &protocol.TextEdit{Range: doc.Range(base+it.Start, base+it.End), NewText: it.Insert},
			SortText: sortKey(i)}
		if it.Snippet {
			ci.InsertTextFormat = protocol.InsertTextSnippet
		}
		if it.Doc != "" {
			ci.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: it.Doc}
		}
		out = append(out, ci)
	}
	return out
}

func sortKey(i int) string {
	const digits = "0123456789"
	return string([]byte{digits[i/1000%10], digits[i/100%10], digits[i/10%10], digits[i%10]})
}

func (s *Server) Diagnose(_ context.Context, doc *document.Doc) []protocol.Diagnostic {
	return Diagnostics(doc, Check(doc.Text, false), 0)
}

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	items := Complete(doc.Text, doc.Offset(pos), false, s.Env.SnippetSupport)
	return &protocol.CompletionList{Items: Items(doc, items, 0)}, nil
}

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	md, start, end, ok := Hover(doc.Text, doc.Offset(pos), false)
	if !ok {
		return nil, nil
	}
	r := doc.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	return ToProtocol(doc, Symbols(doc.Text), 0), nil
}

// ToProtocol converts outline symbols at offset base of doc.
func ToProtocol(doc *document.Doc, syms []Symbol, base int) []protocol.DocumentSymbol {
	out := make([]protocol.DocumentSymbol, 0, len(syms))
	for _, s := range syms {
		out = append(out, protocol.DocumentSymbol{Name: s.Name, Kind: s.Kind,
			Range: doc.Range(base+s.Start, base+s.End), SelectionRange: doc.Range(base+s.SelStart, base+s.SelEnd),
			Children: ToProtocol(doc, s.Children, base)})
	}
	return out
}

func (s *Server) Format(_ context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	indent := "\t"
	if opts.InsertSpaces {
		indent = strings.Repeat(" ", int(max(opts.TabSize, 1)))
	}
	eol := "\n"
	if strings.Contains(doc.Text, "\r\n") {
		eol = "\r\n"
	}
	out, ok := Format(doc.Text, indent, eol)
	if !ok || out == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: out}}, nil
}
