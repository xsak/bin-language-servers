// Package ini is the INI-style configuration module (.ini, .conf, .cfg, git config, systemd units,
// podman Quadlet files): syntax checks, outline, formatting, and for systemd/Quadlet files
// completion and hover of sections and common directives.
package ini

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "ini",
		Description: "INI / .conf / systemd units / podman Quadlet: checks, outline, formatting, directive completion",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	Env lang.Env
}

func (s *Server) Name() string { return "ini" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{"[", "="} }

// Section is a [name] block.
type Section struct {
	Name                   string
	Start, End             int // header start .. end of last entry
	HeaderStart, HeaderEnd int
	Entries                []Entry
}

// Entry is "key = value" (or "key: value", or a bare key).
type Entry struct {
	Key          string
	KStart, KEnd int
	Sep          int // offset of '=' or ':', -1 for a bare key
	Value        string
	VStart, VEnd int
	Line         int
}

// File is a parsed document. Entries before the first header belong to a section with no name.
type File struct {
	Sections []*Section
	Problems []problem
}

type problem struct {
	start, end int
	msg        string
	sev        protocol.DiagnosticSeverity
}

// unitFile reports whether the path is a systemd unit or Quadlet file.
func unitFile(path string) bool { return sectionsFor(filepath.Ext(path)) != nil }

// bareKeysAllowed: git config and MySQL option files accept keys without a value.
func bareKeysAllowed(path string) bool {
	b := filepath.Base(path)
	return b == ".gitconfig" || b == "config" && strings.Contains(path, "/.git/") || strings.HasSuffix(b, ".cnf") ||
		b == ".editorconfig" || b == ".gitmodules"
}

// Parse parses text.
func Parse(text, path string) *File {
	f := &File{}
	cur := &Section{Start: 0}
	f.Sections = append(f.Sections, cur)
	off := 0
	lines := strings.SplitAfter(text, "\n")
	for ln := 0; ln < len(lines); ln++ {
		raw := lines[ln]
		start := off
		off += len(raw)
		line := strings.TrimRight(raw, "\r\n")
		t := strings.TrimSpace(line)
		lead := len(line) - len(strings.TrimLeft(line, " \t"))
		if t == "" || t[0] == ';' || t[0] == '#' {
			continue
		}
		if t[0] == '[' {
			end := strings.IndexByte(t, ']')
			if end < 0 {
				f.Problems = append(f.Problems, problem{start + lead, start + len(line), "Section header is missing ']'.", protocol.SeverityError})
				continue
			}
			cur = &Section{Name: strings.TrimSpace(t[1:end]), Start: start + lead, HeaderStart: start + lead,
				HeaderEnd: start + lead + end + 1, End: start + lead + end + 1}
			f.Sections = append(f.Sections, cur)
			if rest := strings.TrimSpace(t[end+1:]); rest != "" && rest[0] != ';' && rest[0] != '#' {
				f.Problems = append(f.Problems, problem{start + lead + end + 1, start + len(line), "Unexpected text after the section header.", protocol.SeverityWarning})
			}
			continue
		}
		e := Entry{Line: ln, KStart: start + lead, Sep: -1, VStart: -1, VEnd: -1}
		sep := strings.IndexAny(line, "=:")
		if sep < 0 {
			e.Key, e.KEnd = t, start+lead+len(t)
			sev := protocol.SeverityWarning
			if bareKeysAllowed(path) {
				sev = 0
			}
			if sev != 0 {
				f.Problems = append(f.Problems, problem{e.KStart, e.KEnd, "Expected key=value.", sev})
			}
		} else {
			key := strings.TrimRight(line[lead:sep], " \t")
			e.Key, e.KEnd, e.Sep = key, start+lead+len(key), start+sep
			v := strings.TrimSpace(line[sep+1:])
			vs := start + sep + 1 + (len(line[sep+1:]) - len(strings.TrimLeft(line[sep+1:], " \t")))
			// systemd-style continuation lines
			for strings.HasSuffix(v, "\\") && ln+1 < len(lines) {
				ln++
				next := lines[ln]
				off += len(next)
				v = strings.TrimSuffix(v, "\\") + " " + strings.TrimSpace(next)
			}
			e.Value, e.VStart, e.VEnd = v, vs, start+len(strings.TrimRight(line, " \t"))
			if key == "" {
				f.Problems = append(f.Problems, problem{start + sep, start + sep + 1, "Key expected.", protocol.SeverityError})
			}
		}
		cur.Entries = append(cur.Entries, e)
		cur.End = max(cur.End, e.KEnd, e.VEnd)
	}
	return f
}

func (s *Server) Diagnose(_ context.Context, doc *document.Doc) []protocol.Diagnostic {
	f := Parse(doc.Text, doc.Path())
	var out []protocol.Diagnostic
	for _, p := range f.Problems {
		out = append(out, protocol.Diagnostic{Range: doc.Range(p.start, p.end), Severity: p.sev, Source: "bls", Message: p.msg})
	}
	if unitFile(doc.Path()) {
		if len(f.Sections[0].Entries) > 0 {
			e := f.Sections[0].Entries[0]
			out = append(out, protocol.Diagnostic{Range: doc.Range(e.KStart, e.KEnd), Severity: protocol.SeverityError,
				Source: "bls", Message: "Unit file settings must be inside a [Section]."})
		}
		for _, sec := range f.Sections[1:] {
			for _, e := range sec.Entries {
				if e.Sep >= 0 && doc.Text[e.Sep] == ':' {
					out = append(out, protocol.Diagnostic{Range: doc.Range(e.Sep, e.Sep+1), Severity: protocol.SeverityError,
						Source: "bls", Message: "systemd uses Key=Value, not ':'."})
				}
				if d := findDirective(sec.Name, e.Key); d != nil && closedValues[sec.Name+"."+e.Key] && e.Value != "" &&
					!strings.Contains(e.Value, "%") && !contains(d.values, e.Value) && !systemdBool(d.values, e.Value) {
					out = append(out, protocol.Diagnostic{Range: doc.Range(e.VStart, e.VEnd), Severity: protocol.SeverityWarning,
						Source: "bls", Message: fmt.Sprintf("%s=%s: expected one of %s.", e.Key, e.Value, strings.Join(d.values, ", "))})
				}
			}
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// systemdBool accepts all of systemd's boolean spellings where yes/no is expected.
func systemdBool(values []string, v string) bool {
	if !contains(values, "yes") {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "0", "true", "false", "on", "off", "yes", "no":
		return true
	}
	return false
}

func findDirective(section, key string) *directive {
	for _, d := range systemdSections[section] {
		if d.name == key {
			return &d
		}
	}
	return nil
}

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	f := Parse(doc.Text, doc.Path())
	off := doc.Offset(pos)
	lineStart := doc.Offset(protocol.Position{Line: pos.Line})
	before := doc.Text[lineStart:off]
	t := strings.TrimLeft(before, " \t")
	unit := unitFile(doc.Path())
	var items []protocol.CompletionItem
	add := func(label, detail, docMD, text string, start int, kind protocol.CompletionItemKind) {
		it := protocol.CompletionItem{Label: label, Kind: kind, Detail: detail,
			TextEdit: &protocol.TextEdit{Range: doc.Range(start, off), NewText: text}}
		if docMD != "" {
			it.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: docMD}
		}
		items = append(items, it)
	}
	section := currentSection(f, off)

	switch {
	case strings.HasPrefix(t, "["):
		start := off - len(strings.TrimPrefix(t, "["))
		var names []string
		if unit {
			names = sectionsFor(filepath.Ext(doc.Path()))
		}
		for _, sec := range f.Sections[1:] {
			names = append(names, sec.Name)
		}
		seen := map[string]bool{}
		for _, n := range names {
			if !seen[n] && n != "" {
				seen[n] = true
				add(n, "section", "", n+"]", start, protocol.CompletionKindModule)
			}
		}
	case strings.ContainsAny(t, "="):
		key := strings.TrimSpace(t[:strings.Index(t, "=")])
		start := off - len(strings.TrimLeft(t[strings.Index(t, "=")+1:], " \t"))
		if unit {
			if d := findDirective(section, key); d != nil {
				for _, v := range d.values {
					add(v, "", "", v, start, protocol.CompletionKindValue)
				}
			}
		}
	default:
		start := off - len(t)
		seen := map[string]bool{}
		if unit {
			for _, d := range systemdSections[section] {
				seen[d.name] = true
				add(d.name, section, d.doc, d.name+"=", start, protocol.CompletionKindProperty)
			}
		}
		// Keys used in sections with the same name elsewhere in the file.
		for _, sec := range f.Sections {
			if sec.Name != section {
				continue
			}
			for _, e := range sec.Entries {
				if !seen[e.Key] && e.Key != t {
					seen[e.Key] = true
					add(e.Key, "used in this file", "", e.Key+keySep(doc.Text, f), start, protocol.CompletionKindProperty)
				}
			}
		}
	}
	return &protocol.CompletionList{Items: items}, nil
}

// keySep returns the file's style of separator ("=", " = ").
func keySep(text string, f *File) string {
	spaced, tight := 0, 0
	for _, sec := range f.Sections {
		for _, e := range sec.Entries {
			if e.Sep < 0 {
				continue
			}
			if e.Sep > 0 && text[e.Sep-1] == ' ' {
				spaced++
			} else {
				tight++
			}
		}
	}
	if spaced > tight {
		return " = "
	}
	return "="
}

func currentSection(f *File, off int) string {
	name := ""
	for _, s := range f.Sections[1:] {
		if s.HeaderStart <= off {
			name = s.Name
		}
	}
	return name
}

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	if !unitFile(doc.Path()) {
		return nil, nil
	}
	f := Parse(doc.Text, doc.Path())
	off := doc.Offset(pos)
	for _, sec := range f.Sections {
		for _, e := range sec.Entries {
			if off >= e.KStart && off <= e.KEnd {
				if d := findDirective(sec.Name, e.Key); d != nil {
					md := fmt.Sprintf("**%s=** (`[%s]`)\n\n%s", d.name, sec.Name, d.doc)
					if len(d.values) > 0 {
						md += "\n\nValues: `" + strings.Join(d.values, "`, `") + "`"
					}
					r := doc.Range(e.KStart, e.KEnd)
					return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
				}
			}
		}
	}
	return nil, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	f := Parse(doc.Text, doc.Path())
	var out []protocol.DocumentSymbol
	entrySyms := func(sec *Section) []protocol.DocumentSymbol {
		var syms []protocol.DocumentSymbol
		for _, e := range sec.Entries {
			end := max(e.KEnd, e.VEnd)
			detail := e.Value
			if len(detail) > 40 {
				detail = detail[:37] + "..."
			}
			syms = append(syms, protocol.DocumentSymbol{Name: e.Key, Detail: detail, Kind: protocol.SymbolProperty,
				Range: doc.Range(e.KStart, end), SelectionRange: doc.Range(e.KStart, e.KEnd)})
		}
		return syms
	}
	out = append(out, entrySyms(f.Sections[0])...)
	for _, sec := range f.Sections[1:] {
		out = append(out, protocol.DocumentSymbol{Name: sec.Name, Kind: protocol.SymbolModule,
			Range: doc.Range(sec.Start, sec.End), SelectionRange: doc.Range(sec.HeaderStart, sec.HeaderEnd),
			Children: entrySyms(sec)})
	}
	return out, nil
}

// Format trims trailing whitespace, collapses blank-line runs, removes padding inside section
// headers and applies the file's prevailing separator style ("key=value" or "key = value") to
// every entry. Values and comments are not changed.
func (s *Server) Format(_ context.Context, doc *document.Doc, _ protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	f := Parse(doc.Text, doc.Path())
	if len(f.Problems) > 0 && f.Problems[0].sev == protocol.SeverityError {
		return nil, nil
	}
	sep := keySep(doc.Text, f)
	if unitFile(doc.Path()) {
		sep = "="
	}
	lines := strings.Split(strings.ReplaceAll(doc.Text, "\r\n", "\n"), "\n")
	entryAt := map[int]Entry{}
	for _, sec := range f.Sections {
		for _, e := range sec.Entries {
			entryAt[e.Line] = e
		}
	}
	var out []string
	blank := 0
	cont := false
	for i, l := range lines {
		if cont { // continuation line of the previous value
			out = append(out, strings.TrimRight(l, " \t"))
			cont = strings.HasSuffix(strings.TrimRight(l, " \t"), "\\")
			continue
		}
		t := strings.TrimSpace(l)
		if t == "" {
			blank++
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		switch e, ok := entryAt[i]; {
		case strings.HasPrefix(t, "[") && strings.Contains(t, "]"):
			j := strings.Index(t, "]")
			out = append(out, indent+"["+strings.TrimSpace(t[1:j])+"]"+t[j+1:])
		case ok && e.Sep >= 0 && e.Key != "":
			c := doc.Text[e.Sep]
			s := sep
			if c == ':' {
				s = strings.Replace(sep, "=", ":", 1)
			}
			value := strings.TrimSpace(l[strings.IndexAny(l, "=:")+1:])
			out = append(out, strings.TrimRight(indent+e.Key+s+value, " "))
			cont = strings.HasSuffix(value, "\\")
		default:
			out = append(out, strings.TrimRight(l, " \t"))
		}
	}
	text := strings.Join(out, "\n") + "\n"
	if strings.Contains(doc.Text, "\r\n") {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if text == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: text}}, nil
}
