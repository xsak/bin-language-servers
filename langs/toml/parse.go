package toml

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xsak/bin-language-servers/internal/tree"
)

// The parser turns TOML into tree.Node values with byte ranges (so the shared schema engine
// can validate, complete and describe it). It keeps going after errors, one line at a time.

// Problem is a syntax or structure error.
type Problem struct {
	Start, End int
	Msg        string
}

// Header is a [table] or [[array table]] line.
type Header struct {
	Path       []string
	Start, End int
	Array      bool
}

// Result is a parsed document.
type Result struct {
	Root     *tree.Node
	Problems []Problem
	Headers  []Header
}

type parser struct {
	src  string
	i    int
	res  *Result
	root *tree.Node
	cur  *tree.Node // table that key/values go into
	// explicitly defined tables (to report redefinitions) and implicitly created ones
	defined map[*tree.Node]bool
}

// Parse parses src.
func Parse(src string) *Result {
	root := &tree.Node{Kind: tree.Object, Start: 0, End: len(src)}
	p := &parser{src: src, res: &Result{Root: root}, root: root, cur: root, defined: map[*tree.Node]bool{}}
	for p.i < len(p.src) {
		p.line()
	}
	return p.res
}

func (p *parser) problem(start, end int, format string, args ...any) {
	if end <= start {
		end = min(start+1, len(p.src))
		if end == start && start > 0 {
			start--
		}
	}
	p.res.Problems = append(p.res.Problems, Problem{start, end, fmt.Sprintf(format, args...)})
}

func (p *parser) skipWS() {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t') {
		p.i++
	}
}

// skipWSNL skips whitespace, newlines and comments (inside arrays).
func (p *parser) skipWSNL() {
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		case '#':
			p.toEOL()
		default:
			return
		}
	}
}

func (p *parser) toEOL() {
	for p.i < len(p.src) && p.src[p.i] != '\n' {
		p.i++
	}
}

// endLine expects only a comment or the end of line.
func (p *parser) endLine() {
	p.skipWS()
	if p.i < len(p.src) && p.src[p.i] == '#' {
		p.toEOL()
	}
	if p.i < len(p.src) && p.src[p.i] == '\r' {
		p.i++
	}
	if p.i < len(p.src) && p.src[p.i] != '\n' {
		start := p.i
		p.toEOL()
		p.problem(start, p.i, "Unexpected text after the value; expected a newline.")
	}
	if p.i < len(p.src) {
		p.i++
	}
}

func (p *parser) line() {
	p.skipWS()
	if p.i >= len(p.src) {
		return
	}
	switch p.src[p.i] {
	case '\n', '\r':
		p.toEOL()
		if p.i < len(p.src) {
			p.i++
		}
	case '#':
		p.toEOL()
		if p.i < len(p.src) {
			p.i++
		}
	case '[':
		p.header()
	default:
		p.keyValue(p.cur, true)
		p.endLine()
	}
}

type keyPart struct {
	name       string
	start, end int
}

// key reads a dotted key ending at '=' or ']' (not consumed).
func (p *parser) key() ([]keyPart, bool) {
	var parts []keyPart
	for {
		p.skipWS()
		if p.i >= len(p.src) {
			return parts, false
		}
		start := p.i
		var name string
		switch c := p.src[p.i]; {
		case c == '"':
			s, ok := p.basicString()
			if !ok {
				return parts, false
			}
			name = s
		case c == '\'':
			s, ok := p.literalString()
			if !ok {
				return parts, false
			}
			name = s
		case isBareKey(c):
			for p.i < len(p.src) && isBareKey(p.src[p.i]) {
				p.i++
			}
			name = p.src[start:p.i]
		default:
			p.problem(p.i, p.i+1, "Key expected.")
			return parts, false
		}
		parts = append(parts, keyPart{name, start, p.i})
		p.skipWS()
		if p.i < len(p.src) && p.src[p.i] == '.' {
			p.i++
			continue
		}
		return parts, true
	}
}

func isBareKey(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (p *parser) header() {
	start := p.i
	array := strings.HasPrefix(p.src[p.i:], "[[")
	p.i++
	if array {
		p.i++
	}
	parts, ok := p.key()
	if !ok {
		p.toEOL()
		return
	}
	closer := "]"
	if array {
		closer = "]]"
	}
	if !strings.HasPrefix(p.src[p.i:], closer) {
		p.problem(start, p.i, "Expected %s to close the table header.", closer)
		p.toEOL()
		return
	}
	p.i += len(closer)
	h := Header{Start: start, End: p.i, Array: array}
	for _, k := range parts {
		h.Path = append(h.Path, k.name)
	}
	p.res.Headers = append(p.res.Headers, h)

	// Walk/create the parent tables.
	t := p.root
	for _, k := range parts[:len(parts)-1] {
		t = p.child(t, k, start)
		if t == nil {
			p.toEOL()
			return
		}
	}
	last := parts[len(parts)-1]
	if array {
		arr := t.Prop(last.name)
		if arr == nil {
			arr = &tree.Prop{Key: last.name, KeyStart: last.start, KeyEnd: last.end, Colon: -1,
				Value: &tree.Node{Kind: tree.Array, Start: start, End: h.End}}
			t.Props = append(t.Props, arr)
		} else if arr.Value == nil || arr.Value.Kind != tree.Array {
			p.problem(last.start, last.end, "Key %q is already defined and isn't an array of tables.", last.name)
			p.toEOL()
			return
		}
		tbl := &tree.Node{Kind: tree.Object, Start: start, End: h.End}
		arr.Value.Items = append(arr.Value.Items, tbl)
		arr.Value.End = h.End
		p.defined[tbl] = true
		p.cur = tbl
	} else {
		existing := t.Prop(last.name)
		switch {
		case existing == nil:
			tbl := &tree.Node{Kind: tree.Object, Start: start, End: h.End}
			t.Props = append(t.Props, &tree.Prop{Key: last.name, KeyStart: last.start, KeyEnd: last.end, Colon: -1, Value: tbl})
			p.defined[tbl] = true
			p.cur = tbl
		case existing.Value != nil && existing.Value.Kind == tree.Object && !p.defined[existing.Value]:
			// Implicitly created by an earlier [a.b.c]; now defined explicitly.
			p.defined[existing.Value] = true
			p.cur = existing.Value
		case existing.Value != nil && existing.Value.Kind != tree.Object:
			p.problem(last.start, last.end, "Key %q is already defined as a value.", last.name)
			p.cur = &tree.Node{Kind: tree.Object}
		default:
			p.problem(last.start, last.end, "Table %q is defined more than once.", strings.Join(h.Path, "."))
			p.cur = &tree.Node{Kind: tree.Object} // keep parsing into a throwaway table
		}
	}
	p.endLine()
}

// child returns (creating if needed) the table under key k of t, following arrays of tables to
// their last element.
func (p *parser) child(t *tree.Node, k keyPart, at int) *tree.Node {
	pr := t.Prop(k.name)
	if pr == nil {
		n := &tree.Node{Kind: tree.Object, Start: at, End: at}
		t.Props = append(t.Props, &tree.Prop{Key: k.name, KeyStart: k.start, KeyEnd: k.end, Colon: -1, Value: n})
		return n
	}
	switch {
	case pr.Value != nil && pr.Value.Kind == tree.Object:
		return pr.Value
	case pr.Value != nil && pr.Value.Kind == tree.Array && len(pr.Value.Items) > 0 && pr.Value.Items[len(pr.Value.Items)-1].Kind == tree.Object:
		return pr.Value.Items[len(pr.Value.Items)-1]
	}
	p.problem(k.start, k.end, "Key %q is already defined as a value.", k.name)
	return nil
}

// keyValue parses "key = value" into t. With top, dotted keys create sub-tables.
func (p *parser) keyValue(t *tree.Node, top bool) {
	parts, ok := p.key()
	if !ok {
		p.toEOL()
		return
	}
	p.skipWS()
	if p.i >= len(p.src) || p.src[p.i] != '=' {
		last := parts[len(parts)-1]
		p.problem(parts[0].start, last.end, "Expected '=' after the key.")
		// Keep the key for completion while typing.
		t.Props = append(t.Props, &tree.Prop{Key: last.name, KeyStart: last.start, KeyEnd: last.end, Colon: -1})
		p.toEOL()
		return
	}
	eq := p.i
	p.i++
	p.skipWS()
	for _, k := range parts[:len(parts)-1] {
		t = p.child(t, k, parts[0].start)
		if t == nil {
			p.toEOL()
			return
		}
	}
	last := parts[len(parts)-1]
	prop := &tree.Prop{Key: last.name, KeyStart: last.start, KeyEnd: last.end, Colon: eq}
	if t.Prop(last.name) != nil {
		p.problem(last.start, last.end, "Key %q is defined more than once.", last.name)
	}
	if p.i >= len(p.src) || p.src[p.i] == '\n' || p.src[p.i] == '\r' || p.src[p.i] == '#' {
		p.problem(eq, eq+1, "Value expected.")
		prop.Value = &tree.Node{Kind: tree.Null, Start: eq + 1, End: eq + 1}
	} else {
		prop.Value = p.value()
	}
	t.Props = append(t.Props, prop)
	if t.End < p.i {
		t.End = p.i
	}
}

func (p *parser) value() *tree.Node {
	start := p.i
	c := p.src[p.i]
	switch {
	case c == '"' || c == '\'':
		var s string
		var ok bool
		if c == '"' {
			s, ok = p.basicString()
		} else {
			s, ok = p.literalString()
		}
		_ = ok // an unterminated string was reported; keep its text
		return &tree.Node{Kind: tree.String, Start: start, End: p.i, Value: s}
	case c == '[':
		return p.array()
	case c == '{':
		return p.inlineTable()
	}
	// Bare value: bool, number, date/time.
	for p.i < len(p.src) && !strings.ContainsRune(" \t\r\n,]}#", rune(p.src[p.i])) {
		p.i++
	}
	// Date-times may contain one space between date and time.
	if p.i+1 < len(p.src) && p.src[p.i] == ' ' && isDate(p.src[start:p.i]) && p.src[p.i+1] >= '0' && p.src[p.i+1] <= '9' {
		p.i++
		for p.i < len(p.src) && !strings.ContainsRune(" \t\r\n,]}#", rune(p.src[p.i])) {
			p.i++
		}
	}
	text := p.src[start:p.i]
	n := &tree.Node{Start: start, End: p.i}
	switch {
	case text == "true" || text == "false":
		n.Kind, n.Value = tree.Bool, text == "true"
	case text == "inf" || text == "+inf":
		n.Kind, n.Value = tree.Number, math.Inf(1)
	case text == "-inf":
		n.Kind, n.Value = tree.Number, math.Inf(-1)
	case text == "nan" || text == "+nan" || text == "-nan":
		n.Kind, n.Value = tree.Number, math.NaN()
	case isDate(text):
		n.Kind, n.Value = tree.String, text // dates/times validate as strings (format: date-time)
	default:
		if num, ok := number(text); ok {
			n.Kind, n.Value = tree.Number, num
		} else {
			p.problem(start, p.i, "Invalid value %q.", text)
			n.Kind, n.Value = tree.String, text
		}
	}
	return n
}

func isDate(s string) bool {
	return len(s) >= 5 && (s[4] == '-' && s[0] >= '0' && s[0] <= '9' || len(s) >= 8 && s[2] == ':')
}

// number parses TOML integers and floats into a JSON number.
func number(s string) (json.Number, bool) {
	if strings.Contains(s, "__") || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") {
		return "", false
	}
	clean := strings.ReplaceAll(s, "_", "")
	for _, pre := range []struct {
		prefix string
		base   int
	}{{"0x", 16}, {"0o", 8}, {"0b", 2}} {
		if strings.HasPrefix(clean, pre.prefix) {
			v, err := strconv.ParseInt(clean[2:], pre.base, 64)
			if err != nil {
				return "", false
			}
			return json.Number(strconv.FormatInt(v, 10)), true
		}
	}
	if !json.Valid([]byte(strings.TrimPrefix(clean, "+"))) {
		return "", false
	}
	return json.Number(strings.TrimPrefix(clean, "+")), true
}

func (p *parser) array() *tree.Node {
	n := &tree.Node{Kind: tree.Array, Start: p.i}
	p.i++
	for {
		p.skipWSNL()
		if p.i >= len(p.src) {
			p.problem(n.Start, n.Start+1, "Expected ']' to close this array.")
			n.End = p.i
			return n
		}
		if p.src[p.i] == ']' {
			p.i++
			n.End = p.i
			return n
		}
		n.Items = append(n.Items, p.value())
		p.skipWSNL()
		if p.i < len(p.src) && p.src[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.src) && p.src[p.i] != ']' {
			p.problem(p.i, p.i+1, "Expected ',' or ']' in the array.")
			p.toEOL()
		}
	}
}

func (p *parser) inlineTable() *tree.Node {
	n := &tree.Node{Kind: tree.Object, Start: p.i}
	p.i++
	for {
		p.skipWS()
		if p.i >= len(p.src) || p.src[p.i] == '\n' {
			p.problem(n.Start, n.Start+1, "Expected '}' to close this inline table (inline tables must be on one line).")
			n.End = p.i
			return n
		}
		if p.src[p.i] == '}' {
			p.i++
			n.End = p.i
			return n
		}
		before := p.i
		p.keyValue(n, false)
		p.skipWS()
		if p.i < len(p.src) && p.src[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.src) && p.src[p.i] != '}' {
			p.problem(p.i, p.i+1, "Expected ',' or '}' in the inline table.")
			if p.i == before {
				p.i++
			}
		}
	}
}

// basicString reads "..." or """...""" and returns the decoded value.
func (p *parser) basicString() (string, bool) {
	start := p.i
	if strings.HasPrefix(p.src[p.i:], `"""`) {
		end := strings.Index(p.src[p.i+3:], `"""`)
		if end < 0 {
			p.problem(start, start+3, "Unterminated multi-line string.")
			p.i = len(p.src)
			return "", false
		}
		raw := p.src[p.i+3 : p.i+3+end]
		p.i += 3 + end + 3
		for p.i < len(p.src) && p.src[p.i] == '"' { // up to two quotes may end the content
			raw += `"`
			p.i++
		}
		raw = strings.TrimPrefix(strings.TrimPrefix(raw, "\r"), "\n")
		return unescape(raw, true), true
	}
	p.i++
	for p.i < len(p.src) && p.src[p.i] != '"' && p.src[p.i] != '\n' {
		if p.src[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	if p.i >= len(p.src) || p.src[p.i] != '"' {
		p.problem(start, p.i, "Unterminated string.")
		return p.src[start+1 : min(p.i, len(p.src))], false
	}
	p.i++
	return unescape(p.src[start+1:p.i-1], false), true
}

func (p *parser) literalString() (string, bool) {
	start := p.i
	if strings.HasPrefix(p.src[p.i:], "'''") {
		end := strings.Index(p.src[p.i+3:], "'''")
		if end < 0 {
			p.problem(start, start+3, "Unterminated multi-line string.")
			p.i = len(p.src)
			return "", false
		}
		raw := p.src[p.i+3 : p.i+3+end]
		p.i += 3 + end + 3
		return strings.TrimPrefix(strings.TrimPrefix(raw, "\r"), "\n"), true
	}
	end := strings.IndexAny(p.src[p.i+1:], "'\n")
	if end < 0 || p.src[p.i+1+end] != '\'' {
		p.toEOL()
		p.problem(start, p.i, "Unterminated string.")
		return p.src[start+1 : p.i], false
	}
	p.i += 1 + end + 1
	return p.src[start+1 : p.i-1], true
}

// unescape decodes TOML basic-string escapes; multi-line strings also support line-ending
// backslashes.
func unescape(s string, multi bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'e':
			b.WriteByte(0x1b)
		case '"', '\\':
			b.WriteByte(s[i])
		case 'u', 'U':
			n := 4
			if s[i] == 'U' {
				n = 8
			}
			if i+n < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += n
					continue
				}
			}
			b.WriteByte(s[i])
		case ' ', '\t', '\r', '\n':
			if multi {
				for i < len(s) && strings.IndexByte(" \t\r\n", s[i]) >= 0 {
					i++
				}
				i--
				continue
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
