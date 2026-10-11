// Package html is the HTML language module: structure checks, element/attribute/value completion
// and hover from the embedded vscode-custom-data, outline, linked editing of start/end tags, and
// CSS features inside <style> and style="" through the css module.
package html

import (
	"context"
	"fmt"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/webdata"
	"github.com/xsak/bin-language-servers/langs/css"
)

func init() {
	lang.Register(lang.Info{
		Name:        "html",
		Description: "HTML: structure checks, completion and hover (MDN data), outline, linked tag editing, embedded CSS",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	Env lang.Env
}

func (s *Server) Name() string { return "html" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{"<", "/", " ", "\"", "'", ":", "&"} }

func tagData(name string) *webdata.Entry { return webdata.Find(webdata.HTML().Tags, name) }

// knownTag accepts HTML elements, custom elements (with a dash) and anything inside svg/math.
func knownTag(n *Node) bool {
	if tagData(n.Tag) != nil || strings.Contains(n.Tag, "-") || n.Tag == "svg" || n.Tag == "math" {
		return true
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Tag == "svg" || p.Tag == "math" {
			return true
		}
	}
	return false
}

func (s *Server) Diagnose(_ context.Context, doc *document.Doc) []protocol.Diagnostic {
	d := Parse(doc.Text)
	var out []protocol.Diagnostic
	diag := func(start, end, sev int, msg string) {
		out = append(out, protocol.Diagnostic{Range: doc.Range(start, end), Severity: protocol.DiagnosticSeverity(sev),
			Source: "bls", Message: msg})
	}
	for _, p := range d.Problems {
		diag(p.Start, p.End, p.Severity, p.Msg)
	}
	ids := map[string]bool{}
	d.Walk(func(n *Node) {
		if !knownTag(n) {
			diag(n.NameStart, n.NameEnd, 2, fmt.Sprintf("Unknown element <%s>.", n.Tag))
		}
		if a := n.Attr("id"); a != nil && a.Value != "" && !strings.Contains(a.Value, "{") {
			if ids[a.Value] {
				diag(a.VStart, a.VEnd, 2, fmt.Sprintf("Duplicate id %q.", a.Value))
			}
			ids[a.Value] = true
		}
		if n.Tag == "img" && n.Attr("alt") == nil {
			diag(n.NameStart, n.NameEnd, 3, "<img> should have an alt attribute (use alt=\"\" for decorative images).")
		}
		if n.Tag == "style" && n.ContentStart >= 0 {
			out = append(out, css.Diagnostics(doc, css.Check(doc.Text[n.ContentStart:n.ContentEnd], false), n.ContentStart)...)
		}
		if a := n.Attr("style"); a != nil && a.VStart >= 0 && !strings.Contains(a.Value, "{{") {
			out = append(out, css.Diagnostics(doc, css.Check(a.Value, true), a.VStart)...)
		}
	})
	return out
}

// position describes the cursor's place in the markup.
type position struct {
	node    *Node
	inStyle bool  // inside <style> content
	attr    *Attr // attribute whose name or value contains the cursor
	inValue bool
	inTag   bool // inside a start tag (attribute position)
	closing bool // right after "</"
	tagName bool // typing a tag name after '<'
	start   int  // start of the word being typed
}

func locate(doc *Document, src string, off int) position {
	p := position{start: off}
	n := doc.NodeAt(off)
	p.node = n
	if n != nil {
		if n.ContentStart >= 0 && off >= n.ContentStart && off <= n.ContentEnd && n.Tag == "style" {
			p.inStyle = true
			return p
		}
		if off > n.NameEnd && off < n.OpenEnd || (off > n.NameEnd && n.OpenEnd == len(src)) {
			p.inTag = true
			for i := range n.Attrs {
				a := &n.Attrs[i]
				if off >= a.NStart && off <= a.NEnd {
					p.attr, p.start = a, a.NStart
					return p
				}
				if a.VStart >= 0 && off >= a.VStart && off <= a.VEnd {
					p.attr, p.inValue = a, true
					p.start = off
					for p.start > a.VStart && !isSpace(src[p.start-1]) {
						p.start--
					}
					return p
				}
			}
			return p
		}
		if off >= n.NameStart && off <= n.NameEnd {
			p.tagName, p.start = true, n.NameStart
			return p
		}
	}
	// "<" or "</" followed by a partial name, in text.
	i := off
	for i > 0 && isNameChar(src[i-1]) {
		i--
	}
	if i >= 2 && src[i-2:i] == "</" {
		p.closing, p.start = true, i
	} else if i >= 1 && src[i-1] == '<' {
		p.tagName, p.start = true, i
	}
	return p
}

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	off := doc.Offset(pos)
	d := Parse(doc.Text)
	p := locate(d, doc.Text, off)
	snip := s.Env.SnippetSupport
	r := doc.Range(p.start, off)
	var items []protocol.CompletionItem
	add := func(label, detail, docMD, text string, snippet bool, kind protocol.CompletionItemKind) {
		it := protocol.CompletionItem{Label: label, Kind: kind, Detail: detail,
			TextEdit: &protocol.TextEdit{Range: r, NewText: text}, FilterText: label}
		if snippet {
			it.InsertTextFormat = protocol.InsertTextSnippet
		}
		if docMD != "" {
			it.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: docMD}
		}
		items = append(items, it)
	}

	switch {
	case p.inStyle:
		n := p.node
		ci := css.Complete(doc.Text[n.ContentStart:n.ContentEnd], off-n.ContentStart, false, snip)
		return &protocol.CompletionList{Items: css.Items(doc, ci, n.ContentStart)}, nil
	case p.inValue && p.attr.Name == "style":
		a := p.attr
		ci := css.Complete(a.Value, off-a.VStart, true, snip)
		return &protocol.CompletionList{Items: css.Items(doc, ci, a.VStart)}, nil
	case p.inValue:
		for _, v := range attrValues(p.node.Tag, p.attr.Name) {
			add(v.Name, "", v.Markdown(), v.Name, false, protocol.CompletionKindValue)
		}
	case p.closing:
		for n := d.NodeAt(off); n != nil; n = n.Parent {
			if n.CloseStart < 0 && !voidElements[n.Tag] && n.Start < off {
				add("/"+n.Tag, "close tag", "", n.Tag+">", false, protocol.CompletionKindKeyword)
				break
			}
		}
	case p.tagName:
		// A tag being typed has no '>' yet; renaming an existing tag inserts the name only.
		fresh := p.node == nil || p.node.NameStart != p.start ||
			!strings.Contains(doc.Text[p.node.NameEnd:p.node.OpenEnd], ">")
		for _, t := range webdata.HTML().Tags {
			text, isSnip := t.Name, false
			if snip && fresh {
				if voidElements[t.Name] {
					text = t.Name + "$0>"
				} else {
					text = t.Name + ">$0</" + t.Name + ">"
				}
				isSnip = true
			}
			add(t.Name, "element", t.Markdown(), text, isSnip, protocol.CompletionKindKeyword)
		}
	case p.inTag:
		present := map[string]bool{}
		for _, a := range p.node.Attrs {
			if a.Name != "" && (p.attr == nil || a.NStart != p.attr.NStart) {
				present[a.Name] = true
			}
		}
		for _, a := range attributes(p.node.Tag) {
			if present[a.Name] {
				continue
			}
			text, isSnip := a.Name, false
			if a.ValueSet != "v" && (p.attr == nil || p.attr.VStart < 0) {
				text = a.Name + `="$1"`
				isSnip = snip
				if !snip {
					text = a.Name + `=""`
				}
			}
			add(a.Name, "attribute", a.Markdown(), text, isSnip, protocol.CompletionKindProperty)
		}
	default:
		// Character references after '&'.
		i := off
		for i > 0 && isAlpha(doc.Text[i-1]) {
			i--
		}
		if i > 0 && doc.Text[i-1] == '&' {
			r = doc.Range(i-1, off)
			for _, e := range entities {
				add(e[0], e[1], "", e[0], false, protocol.CompletionKindConstant)
			}
		}
	}
	return &protocol.CompletionList{Items: items}, nil
}

var entities = [][2]string{{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", "\""}, {"&apos;", "'"},
	{"&nbsp;", "no-break space"}, {"&copy;", "©"}, {"&reg;", "®"}, {"&trade;", "™"}, {"&hellip;", "…"},
	{"&mdash;", "—"}, {"&ndash;", "–"}, {"&laquo;", "«"}, {"&raquo;", "»"}, {"&euro;", "€"}, {"&times;", "×"},
	{"&larr;", "←"}, {"&rarr;", "→"}, {"&uarr;", "↑"}, {"&darr;", "↓"}, {"&check;", "✓"}, {"&shy;", "soft hyphen"}}

// attributes returns the element's own attributes, then the global ones and event handlers.
func attributes(tag string) []webdata.Entry {
	var out []webdata.Entry
	if t := tagData(tag); t != nil {
		out = append(out, t.Attributes...)
	}
	out = append(out, webdata.HTML().GlobalAttributes...)
	return out
}

// attrValues returns the values of an attribute's value set.
func attrValues(tag, attr string) []webdata.Entry {
	var set string
	for _, a := range attributes(tag) {
		if a.Name == attr {
			if len(a.Values) > 0 {
				return a.Values
			}
			set = a.ValueSet
			break
		}
	}
	if set == "" || set == "v" {
		return nil
	}
	if vs := webdata.Find(webdata.HTML().ValueSets, set); vs != nil {
		return vs.Values
	}
	return nil
}

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	off := doc.Offset(pos)
	d := Parse(doc.Text)
	n := d.NodeAt(off)
	if n == nil {
		return nil, nil
	}
	hover := func(md string, start, end int) (*protocol.Hover, error) {
		if md == "" {
			return nil, nil
		}
		r := doc.Range(start, end)
		return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
	}
	if n.Tag == "style" && n.ContentStart >= 0 && off >= n.ContentStart && off <= n.ContentEnd {
		if md, a, b, ok := css.Hover(doc.Text[n.ContentStart:n.ContentEnd], off-n.ContentStart, false); ok {
			return hover(md, n.ContentStart+a, n.ContentStart+b)
		}
		return nil, nil
	}
	t := tagData(n.Tag)
	if off >= n.NameStart && off <= n.NameEnd || n.CloseNameStart >= 0 && off >= n.CloseNameStart && off <= n.CloseNameStart+len(n.Tag) {
		if t == nil {
			return nil, nil
		}
		start := n.NameStart
		if off < n.NameStart || off > n.NameEnd {
			start = n.CloseNameStart
		}
		return hover(t.Markdown(), start, start+len(n.Tag))
	}
	for _, a := range n.Attrs {
		if off >= a.NStart && off <= a.NEnd {
			for _, e := range attributes(n.Tag) {
				if e.Name == a.Name {
					return hover(e.Markdown(), a.NStart, a.NEnd)
				}
			}
		}
		if a.Name == "style" && a.VStart >= 0 && off >= a.VStart && off <= a.VEnd {
			if md, x, y, ok := css.Hover(a.Value, off-a.VStart, true); ok {
				return hover(md, a.VStart+x, a.VStart+y)
			}
		}
	}
	return nil, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	d := Parse(doc.Text)
	var build func([]*Node) []protocol.DocumentSymbol
	build = func(nodes []*Node) []protocol.DocumentSymbol {
		var out []protocol.DocumentSymbol
		for _, n := range nodes {
			name := n.Tag
			if a := n.Attr("id"); a != nil && a.Value != "" {
				name += "#" + a.Value
			}
			if a := n.Attr("class"); a != nil {
				for _, c := range strings.Fields(a.Value) {
					name += "." + c
				}
			}
			sym := protocol.DocumentSymbol{Name: name, Kind: protocol.SymbolField,
				Range: doc.Range(n.Start, n.End), SelectionRange: doc.Range(n.NameStart, n.NameEnd),
				Children: build(n.Children)}
			if n.Tag == "style" && n.ContentStart >= 0 {
				sym.Children = css.ToProtocol(doc, css.Symbols(doc.Text[n.ContentStart:n.ContentEnd]), n.ContentStart)
			}
			out = append(out, sym)
		}
		return out
	}
	return build(d.Roots), nil
}

// LinkedEditingRanges returns the start and end tag names of the element under the cursor, so
// renaming one renames the other.
func (s *Server) LinkedEditingRanges(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.LinkedEditingRanges, error) {
	off := doc.Offset(pos)
	d := Parse(doc.Text)
	var hit *Node
	d.Walk(func(n *Node) {
		if n.CloseNameStart < 0 {
			return
		}
		closeEnd := n.CloseNameStart + len(n.Tag)
		if (off >= n.NameStart && off <= n.NameEnd) || (off >= n.CloseNameStart && off <= closeEnd) {
			hit = n
		}
	})
	if hit == nil || !strings.EqualFold(doc.Text[hit.CloseNameStart:hit.CloseNameStart+len(hit.Tag)], hit.Tag) {
		return nil, nil
	}
	return &protocol.LinkedEditingRanges{
		Ranges: []protocol.Range{doc.Range(hit.NameStart, hit.NameEnd),
			doc.Range(hit.CloseNameStart, hit.CloseNameStart+len(hit.Tag))},
		WordPattern: `[-_.:A-Za-z0-9]+`,
	}, nil
}
