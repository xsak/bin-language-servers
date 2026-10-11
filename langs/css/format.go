package css

import "strings"

// Format pretty-prints a stylesheet: one declaration per line, "name: value;", one selector of
// a list per line, a blank line between top-level rules; comments are kept, selector and value
// texts are only whitespace-normalized (outside strings). It refuses documents with errors.
func Format(src, indent, eol string) (string, bool) {
	sheet := Parse(src, false)
	for _, p := range sheet.Problems {
		if p.Severity == SevError {
			return "", false
		}
	}
	var b strings.Builder
	var write func(items []Item, depth int)
	write = func(items []Item, depth int) {
		pad := strings.Repeat(indent, depth)
		var prev Item
		for _, it := range items {
			if prev != nil {
				_, prevIsRule := prev.(*Rule)
				_, isRule := it.(*Rule)
				// Blank line between rules, or where the source had one.
				_, pe := prev.span()
				s, _ := it.span()
				if c, ok := it.(*Comment); ok && !strings.Contains(src[pe:s], "\n") {
					// A comment on the same line as the previous item stays there.
					out := strings.TrimSuffix(b.String(), eol)
					b.Reset()
					b.WriteString(out + " " + c.Text + eol)
					prev = it
					continue
				}
				if (depth == 0 && (prevIsRule || isRule)) || strings.Count(src[pe:s], "\n") > 1 {
					b.WriteString(eol)
				}
			}
			switch v := it.(type) {
			case *Comment:
				b.WriteString(pad + v.Text + eol)
			case *Decl:
				b.WriteString(pad + v.Name + ": " + normalize(v.Value))
				if v.Important {
					b.WriteString(" !important")
				}
				b.WriteString(";" + eol)
			case *Rule:
				if v.At {
					b.WriteString(pad + normalize(v.Prelude))
				} else {
					parts := splitTop(v.Prelude, ',')
					for i, part := range parts {
						if i > 0 {
							b.WriteString("," + eol)
						}
						b.WriteString(pad + normalize(part))
					}
				}
				if v.Block == nil {
					b.WriteString(";" + eol)
				} else if len(v.Block.Items) == 0 {
					b.WriteString(" {}" + eol)
				} else {
					b.WriteString(" {" + eol)
					write(v.Block.Items, depth+1)
					b.WriteString(pad + "}" + eol)
				}
			}
			prev = it
		}
	}
	write(sheet.Items, 0)
	return b.String(), true
}

// normalize collapses whitespace runs outside strings to one space and trims.
func normalize(s string) string {
	var b strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteString(s[i:min(j+1, len(s))])
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteByte(c)
		}
	}
	return b.String()
}
