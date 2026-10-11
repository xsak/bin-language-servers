// Package apache is the Apache httpd configuration module (httpd.conf, sites, .htaccess):
// container structure, unknown directives and directives in the wrong context (from the
// httpd.apache.org reference), completion, hover, outline, re-indenting formatter and
// go-to-definition for Include files.
package apache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xsak/bin-language-servers/internal/confdata"
	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "apache",
		Description: "Apache httpd config / .htaccess: directive checks by context (httpd reference), completion, hover",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	Env lang.Env
}

func (s *Server) Name() string { return "apache" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{"<", "/"} }

// Node is a directive line or a <Container> with children.
type Node struct {
	Name                 string // as written
	NStart, NEnd         int
	Args                 []Word
	Start, End           int
	Container            bool
	Children             []*Node
	Parent               *Node
	CloseStart, CloseEnd int // -1 if unclosed
}

// Word is an argument.
type Word struct {
	Text       string
	Start, End int
}

type problem struct {
	start, end int
	msg        string
	sev        protocol.DiagnosticSeverity
}

// Parse parses an httpd configuration file.
func Parse(src string) ([]*Node, []problem) {
	var roots []*Node
	var probs []problem
	var stack []*Node
	add := func(n *Node) {
		if len(stack) > 0 {
			n.Parent = stack[len(stack)-1]
			n.Parent.Children = append(n.Parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	off := 0
	lines := strings.SplitAfter(src, "\n")
	for li := 0; li < len(lines); li++ {
		start := off
		off += len(lines[li])
		line := strings.TrimRight(lines[li], "\r\n")
		// Continuation lines.
		for strings.HasSuffix(line, "\\") && li+1 < len(lines) {
			li++
			off += len(lines[li])
			line = line[:len(line)-1] + " " + strings.TrimRight(lines[li], "\r\n")
		}
		t := strings.TrimSpace(line)
		lead := len(line) - len(strings.TrimLeft(line, " \t"))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		pos := start + lead
		switch {
		case strings.HasPrefix(t, "</"):
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "</"), ">"))
			if len(stack) == 0 || !strings.EqualFold(stack[len(stack)-1].Name, name) {
				probs = append(probs, problem{pos, pos + len(t), fmt.Sprintf("Unexpected </%s>.", name), protocol.SeverityError})
				continue
			}
			n := stack[len(stack)-1]
			n.CloseStart, n.CloseEnd, n.End = pos, pos+len(t), pos+len(t)
			stack = stack[:len(stack)-1]
		case strings.HasPrefix(t, "<"):
			if !strings.HasSuffix(t, ">") {
				probs = append(probs, problem{pos, pos + len(t), "Container start tag is missing '>'.", protocol.SeverityError})
				continue
			}
			inner := strings.TrimSuffix(t[1:], ">")
			n := &Node{Container: true, Start: pos, CloseStart: -1, CloseEnd: -1, End: pos + len(t)}
			fillWords(n, inner, pos+1)
			add(n)
			stack = append(stack, n)
		default:
			n := &Node{Start: pos, End: pos + len(t), CloseStart: -1, CloseEnd: -1}
			fillWords(n, t, pos)
			add(n)
		}
	}
	for _, n := range stack {
		probs = append(probs, problem{n.NStart, n.NEnd, fmt.Sprintf("<%s> is not closed.", n.Name), protocol.SeverityError})
		n.End = len(src)
	}
	return roots, probs
}

// fillWords splits text (at offset base) into the name and arguments; quotes group words.
func fillWords(n *Node, text string, base int) {
	i := 0
	var words []Word
	for i < len(text) {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i >= len(text) {
			break
		}
		start := i
		if text[i] == '"' || text[i] == '\'' {
			q := text[i]
			i++
			for i < len(text) && text[i] != q {
				if text[i] == '\\' {
					i++
				}
				i++
			}
			i = min(i+1, len(text))
			words = append(words, Word{Text: strings.Trim(text[start:i], string(q)), Start: base + start, End: base + i})
			continue
		}
		for i < len(text) && text[i] != ' ' && text[i] != '\t' {
			i++
		}
		words = append(words, Word{Text: text[start:i], Start: base + start, End: base + i})
	}
	if len(words) == 0 {
		return
	}
	n.Name, n.NStart, n.NEnd = words[0].Text, words[0].Start, words[0].End
	n.Args = words[1:]
}

// Walk visits every node.
func Walk(nodes []*Node, fn func(*Node)) {
	for _, n := range nodes {
		fn(n)
		Walk(n.Children, fn)
	}
}

var directoryContainers = set("directory directorymatch location locationmatch files filesmatch proxy proxymatch")

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// contextOf names the httpd context of a node: "server config", "virtual host", "directory" or
// ".htaccess". Conditional containers (<IfModule>, <If>, ...) are transparent.
func contextOf(n *Node, htaccess bool) string {
	for p := n.Parent; p != nil; p = p.Parent {
		name := strings.ToLower(p.Name)
		if directoryContainers[name] {
			return "directory"
		}
		if name == "virtualhost" {
			return "virtual host"
		}
	}
	if htaccess {
		return ".htaccess"
	}
	return "server config"
}

func isHtaccess(doc *document.Doc) bool { return strings.HasSuffix(doc.Path(), ".htaccess") }

func allowedIn(d confdata.Directive, ctx string) bool {
	for _, c := range d.Contexts {
		if c == ctx {
			return true
		}
	}
	return false
}

func (s *Server) Diagnose(_ context.Context, doc *document.Doc) []protocol.Diagnostic {
	roots, probs := Parse(doc.Text)
	var out []protocol.Diagnostic
	add := func(start, end int, sev protocol.DiagnosticSeverity, msg string) {
		out = append(out, protocol.Diagnostic{Range: doc.Range(start, end), Severity: sev, Source: "bls", Message: msg})
	}
	for _, p := range probs {
		add(p.start, p.end, p.sev, p.msg)
	}
	ht := isHtaccess(doc)
	table := confdata.Apache()
	Walk(roots, func(n *Node) {
		if n.Name == "" {
			return
		}
		entries := table.Lookup(n.Name)
		if len(entries) == 0 {
			add(n.NStart, n.NEnd, protocol.SeverityWarning, fmt.Sprintf("Unknown directive %q (not in the httpd reference; third-party module?).", n.Name))
			return
		}
		ctx := contextOf(n, ht)
		for _, e := range entries {
			if allowedIn(e, ctx) || len(e.Contexts) == 0 {
				return
			}
		}
		add(n.NStart, n.NEnd, protocol.SeverityWarning, fmt.Sprintf("%s is not allowed in %s context (allowed in: %s).",
			entries[0].Name, ctx, strings.Join(entries[0].Contexts, ", ")))
	})
	return out
}

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	roots, _ := Parse(doc.Text)
	off := doc.Offset(pos)
	lineStart := strings.LastIndexByte(doc.Text[:off], '\n') + 1
	before := doc.Text[lineStart:off]
	t := strings.TrimLeft(before, " \t")
	ws := off
	for ws > lineStart && !strings.ContainsRune(" \t</", rune(doc.Text[ws-1])) {
		ws--
	}
	r := doc.Range(ws, off)
	var items []protocol.CompletionItem
	table := confdata.Apache()

	// Find the open container at off.
	var open *Node
	Walk(roots, func(n *Node) {
		if n.Container && off > n.Start && (n.CloseStart < 0 || off <= n.CloseStart) {
			open = n
		}
	})

	switch {
	case strings.HasPrefix(t, "</"):
		if open != nil {
			items = append(items, protocol.CompletionItem{Label: "/" + open.Name, Kind: protocol.CompletionKindKeyword,
				TextEdit: &protocol.TextEdit{Range: r, NewText: open.Name + ">"}})
		}
	case strings.Contains(strings.TrimSpace(t), " ") || strings.HasSuffix(t, " ") && strings.TrimSpace(t) != "":
		// Arguments: choices from the syntax ("On|Off").
		name := strings.Fields(strings.TrimPrefix(t, "<"))[0]
		for _, d := range table.Lookup(name) {
			for _, s := range d.Syntax {
				f := strings.Fields(s)
				if len(f) == 2 && strings.Contains(f[1], "|") {
					for _, c := range strings.Split(f[1], "|") {
						items = append(items, protocol.CompletionItem{Label: c, Kind: protocol.CompletionKindValue,
							TextEdit: &protocol.TextEdit{Range: r, NewText: c}})
					}
				}
			}
		}
	default:
		n := &Node{Parent: open}
		ctx := contextOf(n, isHtaccess(doc))
		container := strings.HasPrefix(t, "<")
		for _, d := range table.All {
			if !allowedIn(d, ctx) || d.Container != container && container {
				continue
			}
			text, snip := d.Name+" ", false
			if d.Container {
				text = "<" + d.Name + " $1>\n\t$0\n</" + d.Name + ">"
				if container {
					text = d.Name + " $1>\n\t$0\n</" + d.Name + ">"
				}
				snip = true
			}
			if snip && !s.Env.SnippetSupport {
				continue
			}
			it := protocol.CompletionItem{Label: d.Name, Kind: protocol.CompletionKindKeyword, Detail: d.Module,
				Documentation: &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: d.Markdown()},
				TextEdit:      &protocol.TextEdit{Range: r, NewText: text}}
			if snip {
				it.InsertTextFormat = protocol.InsertTextSnippet
				it.InsertTextMode = protocol.InsertTextAdjustIndentation
			}
			items = append(items, it)
		}
	}
	return &protocol.CompletionList{Items: items}, nil
}

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	roots, _ := Parse(doc.Text)
	off := doc.Offset(pos)
	var found *Node
	start, end := 0, 0
	Walk(roots, func(n *Node) {
		if off >= n.NStart && off <= n.NEnd {
			found, start, end = n, n.NStart, n.NEnd
		}
		if n.CloseStart >= 0 && off >= n.CloseStart && off <= n.CloseEnd {
			found, start, end = n, n.CloseStart, n.CloseEnd
		}
	})
	if found == nil {
		return nil, nil
	}
	entries := confdata.Apache().Lookup(found.Name)
	if len(entries) == 0 {
		return nil, nil
	}
	r := doc.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: entries[0].Markdown()}, Range: &r}, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	roots, _ := Parse(doc.Text)
	var build func([]*Node) []protocol.DocumentSymbol
	build = func(nodes []*Node) []protocol.DocumentSymbol {
		var out []protocol.DocumentSymbol
		for _, n := range nodes {
			if !n.Container {
				continue
			}
			name := n.Name
			for _, a := range n.Args {
				name += " " + a.Text
			}
			detail := ""
			for _, c := range n.Children {
				if strings.EqualFold(c.Name, "ServerName") && len(c.Args) > 0 {
					detail = c.Args[0].Text
				}
			}
			out = append(out, protocol.DocumentSymbol{Name: name, Detail: detail, Kind: protocol.SymbolNamespace,
				Range: doc.Range(n.Start, n.End), SelectionRange: doc.Range(n.NStart, n.NEnd), Children: build(n.Children)})
		}
		return out
	}
	return build(roots), nil
}

// Format re-indents by container depth, trims trailing whitespace and collapses blank-line runs.
func (s *Server) Format(_ context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	if _, probs := Parse(doc.Text); len(probs) > 0 {
		return nil, nil
	}
	indent := "    "
	if !opts.InsertSpaces && opts.TabSize != 0 {
		indent = "\t"
	} else if opts.TabSize > 0 {
		indent = strings.Repeat(" ", int(opts.TabSize))
	}
	eol := "\n"
	if strings.Contains(doc.Text, "\r\n") {
		eol = "\r\n"
	}
	var out []string
	depth, blank := 0, 0
	cont := false
	for _, l := range strings.Split(strings.ReplaceAll(doc.Text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if cont {
			out = append(out, strings.TrimRight(l, " \t"))
			cont = strings.HasSuffix(t, "\\")
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
		if strings.HasPrefix(t, "</") {
			depth = max(depth-1, 0)
		}
		out = append(out, strings.Repeat(indent, depth)+t)
		if strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "</") {
			depth++
		}
		cont = strings.HasSuffix(t, "\\")
	}
	text := strings.Join(out, eol) + eol
	if text == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: text}}, nil
}

// Definition opens the files of Include / IncludeOptional (globs, relative to ServerRoot or the
// file's directory).
func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	roots, _ := Parse(doc.Text)
	off := doc.Offset(pos)
	root := filepath.Dir(doc.Path())
	Walk(roots, func(n *Node) {
		if strings.EqualFold(n.Name, "ServerRoot") && len(n.Args) > 0 {
			root = n.Args[0].Text
		}
	})
	var out []protocol.Location
	Walk(roots, func(n *Node) {
		if !strings.EqualFold(n.Name, "Include") && !strings.EqualFold(n.Name, "IncludeOptional") {
			return
		}
		for _, a := range n.Args {
			if off < a.Start || off > a.End {
				continue
			}
			pattern := a.Text
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(root, pattern)
			}
			matches, _ := filepath.Glob(pattern)
			for _, m := range matches {
				if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
					out = append(out, protocol.Location{URI: document.PathToURI(m)})
				}
			}
		}
	})
	return out, nil
}
