package css

import "strings"

// The parser is deliberately small: it splits a stylesheet into rules, declarations and
// comments with byte ranges, tolerating errors so editing features keep working. Selector and
// value texts are kept verbatim.

// Sheet is a parsed stylesheet (or, inline, a declaration list from a style="" attribute).
type Sheet struct {
	Items    []Item // top level, in source order
	Problems []Problem
}

// Item is *Rule, *Decl or *Comment.
type Item interface{ span() (int, int) }

// Rule is an at-rule or a qualified (style) rule.
type Rule struct {
	At           bool
	Name         string // at-keyword, e.g. "@media"
	Prelude      string // selector list or at-rule prelude, trimmed
	PStart, PEnd int
	Block        *Block // nil for statement at-rules (@import ...;)
	Start, End   int
}

// Block is the {...} part of a rule.
type Block struct {
	Open, Close int // Close is -1 when unclosed
	Items       []Item
}

// Decl is "name: value [!important];".
type Decl struct {
	Name         string
	NStart, NEnd int
	Colon        int // -1 if missing
	Value        string
	VStart, VEnd int
	Important    bool
	End          int // after the ';' if present
}

// Comment is /* ... */.
type Comment struct {
	Text       string
	Start, End int
}

func (r *Rule) span() (int, int)    { return r.Start, r.End }
func (d *Decl) span() (int, int)    { return d.NStart, d.End }
func (c *Comment) span() (int, int) { return c.Start, c.End }

// Severity of a problem.
const (
	SevError   = 1
	SevWarning = 2
	SevInfo    = 3
	SevHint    = 4
)

// Problem is a diagnostic with a byte range.
type Problem struct {
	Start, End int
	Msg        string
	Severity   int
}

// Parse parses a stylesheet; inline parses a declaration list.
func Parse(src string, inline bool) *Sheet {
	p := &parser{src: src, sheet: &Sheet{}}
	if inline {
		p.sheet.Items, _ = p.items(0, len(src), true, false)
	} else {
		p.sheet.Items, _ = p.items(0, len(src), false, true)
	}
	return p.sheet
}

type parser struct {
	src   string
	sheet *Sheet
}

func (p *parser) problem(start, end int, msg string) {
	if end <= start {
		end = min(start+1, len(p.src))
		if end == start && start > 0 {
			start--
		}
	}
	p.sheet.Problems = append(p.sheet.Problems, Problem{Start: start, End: end, Msg: msg, Severity: SevError})
}

// skipSpace skips whitespace and returns the new position.
func (p *parser) skipSpace(i, end int) int {
	for i < end && strings.IndexByte(" \t\r\n\f", p.src[i]) >= 0 {
		i++
	}
	return i
}

// scan returns the position of the first ';', '{' or '}' at paren depth 0 in [i, end),
// skipping strings and comments; end if none.
func (p *parser) scan(i, end int) int {
	depth := 0
	for i < end {
		switch c := p.src[i]; c {
		case '"', '\'':
			i = p.skipString(i, end)
			continue
		case '/':
			if i+1 < end && p.src[i+1] == '*' {
				i = p.skipComment(i, end)
				continue
			}
		case '\\':
			i += 2
			continue
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ';', '{', '}':
			if depth == 0 || c != ';' {
				return i
			}
		}
		i++
	}
	return end
}

func (p *parser) skipString(i, end int) int {
	q := p.src[i]
	i++
	for i < end && p.src[i] != q && p.src[i] != '\n' {
		if p.src[i] == '\\' {
			i++
		}
		i++
	}
	if i < end && p.src[i] == q {
		i++
	}
	return min(i, end)
}

func (p *parser) skipComment(i, end int) int {
	j := strings.Index(p.src[i+2:end], "*/")
	if j < 0 {
		p.problem(i, i+2, "Unterminated comment.")
		return end
	}
	return i + 2 + j + 2
}

// items parses rules/declarations/comments in [i, end) and returns them with the position where
// parsing stopped: the '}' that closes the enclosing block, or end. Declarations are allowed in
// blocks and inline style; top is the stylesheet level.
func (p *parser) items(i, end int, inline, top bool) ([]Item, int) {
	var out []Item
	for {
		i = p.skipSpace(i, end)
		if i >= end {
			return out, end
		}
		if p.src[i] == '/' && i+1 < end && p.src[i+1] == '*' {
			j := p.skipComment(i, end)
			out = append(out, &Comment{Text: p.src[i:j], Start: i, End: j})
			i = j
			continue
		}
		// HTML comment markers are allowed at the top level of a stylesheet.
		if top && (strings.HasPrefix(p.src[i:end], "<!--") || strings.HasPrefix(p.src[i:end], "-->")) {
			i += 4
			if p.src[i-1] == '>' {
				i--
			}
			continue
		}
		stop := p.scan(i, end)
		var c byte
		if stop < end {
			c = p.src[stop]
		}
		switch {
		case c == '}':
			if top || inline {
				p.problem(stop, stop+1, "Unexpected '}'.")
				if stop > i {
					out = append(out, p.decl(i, stop, stop))
				}
				i = stop + 1
				continue
			}
			// End of the enclosing block; a last declaration may lack its ';'.
			if strings.TrimSpace(p.src[i:stop]) != "" {
				out = append(out, p.decl(i, stop, stop))
			}
			return out, stop
		case c == '{':
			r := &Rule{Start: i, PStart: i, PEnd: stop}
			pre := p.src[i:stop]
			trimmed := strings.TrimRight(pre, " \t\r\n\f")
			r.PEnd = i + len(trimmed)
			r.Prelude = strings.TrimSpace(pre)
			if strings.HasPrefix(r.Prelude, "@") {
				r.At = true
				r.Name = atName(r.Prelude)
			} else if r.Prelude == "" {
				p.problem(stop, stop+1, "Selector expected.")
			}
			r.Block = &Block{Open: stop, Close: -1}
			var closeAt int
			r.Block.Items, closeAt = p.items(stop+1, end, false, false)
			if closeAt < end {
				r.Block.Close = closeAt
				r.End = closeAt + 1
			} else {
				p.problem(stop, stop+1, "Expected '}' to close this block.")
				r.End = end
			}
			out = append(out, r)
			i = r.End
		default: // ';' or end of input
			text := strings.TrimSpace(p.src[i:stop])
			semiEnd := stop
			if c == ';' {
				semiEnd = stop + 1
			}
			switch {
			case strings.HasPrefix(text, "@"):
				r := &Rule{At: true, Name: atName(text), Prelude: text, Start: i, PStart: i,
					PEnd: i + len(strings.TrimRight(p.src[i:stop], " \t\r\n\f")), End: semiEnd}
				out = append(out, r)
			case top:
				if text != "" {
					p.problem(i, i+len(strings.TrimRight(p.src[i:stop], " \t\r\n\f")), "'{' expected.")
				}
			default:
				if text != "" {
					out = append(out, p.decl(i, stop, semiEnd))
				}
			}
			i = semiEnd
			if c == 0 {
				return out, end
			}
		}
	}
}

func atName(prelude string) string {
	n := 1
	for n < len(prelude) && (isIdent(prelude[n]) || prelude[n] == '-') {
		n++
	}
	return strings.ToLower(prelude[:n])
}

func isIdent(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

// decl parses a declaration in [i, stop); end includes the ';'.
func (p *parser) decl(i, stop, end int) *Decl {
	i = p.skipSpace(i, stop)
	d := &Decl{NStart: i, Colon: -1, End: end}
	colon := -1
	for j := i; j < stop; j++ {
		c := p.src[j]
		if c == ':' {
			colon = j
			break
		}
		if c == '"' || c == '\'' {
			j = p.skipString(j, stop) - 1
		}
	}
	if colon < 0 {
		name := strings.TrimRight(p.src[i:stop], " \t\r\n\f")
		d.Name, d.NEnd = name, i+len(name)
		d.VStart, d.VEnd = d.NEnd, d.NEnd
		p.problem(d.NStart, d.NEnd, "Colon expected.")
		return d
	}
	name := strings.TrimRight(p.src[i:colon], " \t\r\n\f")
	d.Name, d.NEnd, d.Colon = name, i+len(name), colon
	vs := p.skipSpace(colon+1, stop)
	v := strings.TrimRight(p.src[vs:stop], " \t\r\n\f")
	d.VStart, d.VEnd = vs, vs+len(v)
	if k := strings.LastIndex(strings.ToLower(v), "!important"); k >= 0 && strings.TrimSpace(v[k+len("!important"):]) == "" {
		d.Important = true
		v = strings.TrimRight(v[:k], " \t\r\n\f")
	}
	d.Value = v
	if name == "" {
		p.problem(colon, colon+1, "Property name expected.")
	} else if v == "" && !strings.HasPrefix(name, "--") {
		p.problem(d.NStart, colon+1, "Value expected.")
	}
	return d
}

// Walk calls fn for every item, with the enclosing at-rule names.
func Walk(items []Item, fn func(it Item, parents []*Rule)) {
	var walk func([]Item, []*Rule)
	walk = func(items []Item, parents []*Rule) {
		for _, it := range items {
			fn(it, parents)
			if r, ok := it.(*Rule); ok && r.Block != nil {
				walk(r.Block.Items, append(parents, r))
			}
		}
	}
	walk(items, nil)
}
