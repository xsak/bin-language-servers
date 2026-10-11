package html

import (
	"context"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// Format re-indents an HTML document by element nesting. Text, tags and attributes stay on the
// lines they are on (no reflow); continuation lines of a multi-line start tag are indented one
// level deeper than the tag; <pre> and <textarea> content is kept verbatim; <script> and
// <style> content is shifted as a block, keeping its relative indentation. Documents with
// structural errors are not formatted.
func Format(src, indent, eol string) (string, bool) {
	text := strings.ReplaceAll(src, "\r\n", "\n")
	d := Parse(text)
	for _, p := range d.Problems {
		if p.Severity == 1 {
			return "", false
		}
	}
	var nodes []*Node
	d.Walk(func(n *Node) { nodes = append(nodes, n) })

	closeOf := func(n *Node) int {
		if n.CloseStart >= 0 {
			return n.CloseStart
		}
		return n.End
	}
	depthAt := func(off int) int {
		depth := 0
		for _, n := range nodes {
			if voidElements[n.Tag] || n.SelfClosed {
				continue
			}
			if n.OpenEnd <= off && off < closeOf(n) {
				depth++
			}
		}
		return depth
	}
	// verbatim returns the element whose raw content contains off.
	verbatim := func(off int) *Node {
		for _, n := range nodes {
			if (n.Tag == "pre" || n.Tag == "textarea") && n.OpenEnd <= off && off <= closeOf(n) { // text before the end tag is content too
				return n
			}
		}
		return nil
	}
	shifted := func(off int) *Node {
		for _, n := range nodes {
			if (n.Tag == "script" || n.Tag == "style") && n.ContentStart >= 0 && n.ContentStart <= off && off < n.ContentEnd {
				return n
			}
		}
		return nil
	}

	lines := strings.Split(text, "\n")
	var out []string
	blank := 0
	off := 0
	// Indentation of each script/style block: removed common prefix, applied depth.
	blockBase := map[*Node]string{}
	for _, l := range lines {
		lineStart := off
		off += len(l) + 1
		lead := len(l) - len(strings.TrimLeft(l, " \t"))
		first := lineStart + lead
		t := strings.TrimSpace(l)

		if n := verbatim(lineStart); n != nil && lineStart > n.OpenEnd-1 {
			out = append(out, l)
			blank = 0
			continue
		}
		if t == "" {
			blank++
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0

		if n := shifted(first); n != nil && !strings.HasPrefix(t, "</"+n.Tag) {
			base, ok := blockBase[n]
			if !ok {
				base = commonIndent(text[n.ContentStart:n.ContentEnd])
				blockBase[n] = base
			}
			rel := strings.TrimPrefix(strings.TrimRight(l, " \t"), base)
			out = append(out, strings.Repeat(indent, depthAt(n.ContentStart))+rel)
			continue
		}

		depth := depthAt(first)
		// Continuation line of a multi-line start tag.
		for _, n := range nodes {
			if n.Start < first && first < n.OpenEnd {
				depth = depthAt(n.Start) + 1
			}
		}
		out = append(out, strings.Repeat(indent, depth)+strings.TrimRight(t, " \t"))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, eol) + eol, true
}

// commonIndent returns the whitespace prefix shared by all non-blank lines.
func commonIndent(s string) string {
	prefix := ""
	set := false
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lead := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if !set {
			prefix, set = lead, true
			continue
		}
		for !strings.HasPrefix(lead, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
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
