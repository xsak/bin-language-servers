package toml

import (
	"context"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// Format normalizes layout without touching values: "key = value" with single spaces around
// '=', "[table]" headers without inner padding, no trailing whitespace, at most one blank line in
// a row, a blank line before each header. Lines inside multi-line strings and arrays are left
// as they are. Documents with syntax errors are not formatted.
func Format(src string) (string, bool) {
	if len(Parse(src).Problems) > 0 {
		return "", false
	}
	eol := "\n"
	if strings.Contains(src, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	inString := "" // `"""` or `'''` while inside a multi-line string
	depth := 0     // open array brackets carried over from previous lines
	blank := 0
	for _, l := range lines {
		raw := l
		l = strings.TrimRight(l, " \t")
		if inString != "" || depth > 0 {
			out = append(out, raw)
			inString, depth = scanState(raw, inString, depth)
			continue
		}
		t := strings.TrimSpace(l)
		if t == "" {
			blank++
			continue
		}
		if strings.HasPrefix(t, "[") && !strings.ContainsAny(t, `"'`) {
			// Header: one blank line before it (not at the top of the file).
			if len(out) > 0 {
				out = append(out, "")
			}
			blank = 0
			out = append(out, normalizeHeader(t))
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if strings.HasPrefix(t, "#") {
			out = append(out, indent+t)
			continue
		}
		formatted := normalizeKeyValue(t)
		out = append(out, indent+formatted)
		inString, depth = scanState(formatted, "", 0)
	}
	return strings.Join(out, eol) + eol, true
}

// normalizeHeader removes padding inside "[ a . b ]" / "[[ x ]]" headers (unquoted only).
func normalizeHeader(t string) string {
	comment := ""
	if i := strings.Index(t, "#"); i >= 0 {
		comment = " " + strings.TrimSpace(t[i:])
		t = strings.TrimSpace(t[:i])
	}
	array := strings.HasPrefix(t, "[[")
	inner := strings.Trim(t, "[] \t")
	parts := strings.Split(inner, ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if array {
		return "[[" + strings.Join(parts, ".") + "]]" + comment
	}
	return "[" + strings.Join(parts, ".") + "]" + comment
}

// normalizeKeyValue puts single spaces around the first '=' outside quotes.
func normalizeKeyValue(t string) string {
	var quote byte
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '=':
			return strings.TrimRight(t[:i], " \t") + " = " + strings.TrimLeft(t[i+1:], " \t")
		}
	}
	return t
}

// scanState tracks multi-line strings and array brackets across a line.
func scanState(l, inString string, depth int) (string, int) {
	for i := 0; i < len(l); i++ {
		if inString != "" {
			if strings.HasPrefix(l[i:], inString) {
				i += len(inString) - 1
				inString = ""
			}
			continue
		}
		switch {
		case strings.HasPrefix(l[i:], `"""`) || strings.HasPrefix(l[i:], "'''"):
			inString = l[i : i+3]
			i += 2
		case l[i] == '"' || l[i] == '\'':
			q := l[i]
			for i++; i < len(l) && l[i] != q; i++ {
				if l[i] == '\\' && q == '"' {
					i++
				}
			}
		case l[i] == '#':
			return inString, depth
		case l[i] == '[':
			depth++
		case l[i] == ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return inString, depth
}

func (s *Server) Format(_ context.Context, doc *document.Doc, _ protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	if !s.Settings().Format {
		return nil, nil
	}
	out, ok := Format(doc.Text)
	if !ok || out == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: out}}, nil
}
