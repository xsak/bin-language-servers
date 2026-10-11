package html

import (
	"fmt"
	"strings"
)

// The parser builds an element tree with exact byte ranges for tag names, attributes and values.
// It knows void elements, raw-text elements (script, style, textarea, title) and the common
// implied end tags, so ordinary HTML doesn't produce false "not closed" errors.

// Node is an element.
type Node struct {
	Tag                  string // lower case
	Start                int    // '<'
	NameStart, NameEnd   int
	Attrs                []Attr
	OpenEnd              int // after '>' of the start tag (or end of input)
	SelfClosed           bool
	CloseStart, CloseEnd int // the end tag; -1 if none
	CloseNameStart       int
	End                  int // end of the element including its end tag
	Children             []*Node
	Parent               *Node
	// Content range for raw-text elements (script, style, textarea, title).
	ContentStart, ContentEnd int
}

// Attr is a tag attribute.
type Attr struct {
	Name         string // lower case
	NStart, NEnd int
	Value        string
	VStart, VEnd int // inside the quotes; -1 without a value
}

// Document is a parsed HTML document.
type Document struct {
	Roots    []*Node
	Problems []Problem
}

// Problem is a diagnostic with a byte range.
type Problem struct {
	Start, End int
	Msg        string
	Severity   int // 1 error .. 4 hint
}

var voidElements = set("area base br col embed hr img input link meta param source track wbr keygen command basefont frame")
var rawText = set("script style textarea title")

// optionalEnd elements may omit their end tag.
var optionalEnd = set("p li dt dd tr td th option optgroup thead tbody tfoot colgroup caption html head body rt rp")

// closesP are elements whose start tag closes an open <p>.
var closesP = set(`address article aside blockquote details dialog div dl fieldset figcaption figure footer
form h1 h2 h3 h4 h5 h6 header hgroup hr main menu nav ol p pre section table ul`)

// impliedBy: opening the key closes an open element of these kinds (when it is the current one).
var impliedBy = map[string]map[string]bool{
	"li": set("li"), "dt": set("dt dd"), "dd": set("dt dd"), "tr": set("tr td th"),
	"td": set("td th"), "th": set("td th"), "option": set("option"), "optgroup": set("optgroup option"),
	"thead": set("tbody tfoot tr td th"), "tbody": set("thead tbody tfoot tr td th"), "tfoot": set("thead tbody tr td th"),
	"rt": set("rt rp"), "rp": set("rt rp"),
}

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// Parse parses src.
func Parse(src string) *Document {
	doc := &Document{}
	var stack []*Node
	add := func(n *Node) {
		if len(stack) > 0 {
			n.Parent = stack[len(stack)-1]
			n.Parent.Children = append(n.Parent.Children, n)
		} else {
			doc.Roots = append(doc.Roots, n)
		}
	}
	// closeTop ends the element on top of the stack at pos (implicitly).
	closeTop := func(pos int) {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n.End = pos
	}

	i := 0
	for i < len(src) {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		switch {
		case strings.HasPrefix(src[i:], "<!--"):
			j := strings.Index(src[i+4:], "-->")
			if j < 0 {
				doc.Problems = append(doc.Problems, Problem{i, i + 4, "Comment is not closed.", 1})
				i = len(src)
				continue
			}
			i += 4 + j + 3
		case strings.HasPrefix(src[i:], "<!") || strings.HasPrefix(src[i:], "<?"):
			j := strings.IndexByte(src[i:], '>')
			if j < 0 {
				i = len(src)
				continue
			}
			i += j + 1
		case strings.HasPrefix(src[i:], "</"):
			ns := i + 2
			ne := ns
			for ne < len(src) && isNameChar(src[ne]) {
				ne++
			}
			name := strings.ToLower(src[ns:ne])
			gt := strings.IndexByte(src[ne:], '>')
			end := len(src)
			if gt >= 0 {
				end = ne + gt + 1
			}
			if name == "" {
				i = ne
				continue
			}
			// Find the matching open element; elements above it are closed implicitly.
			match := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].Tag == name {
					match = k
					break
				}
			}
			if match < 0 {
				sev := 1
				if name == "p" { // browsers turn a stray </p> into an empty paragraph
					sev = 2
				}
				doc.Problems = append(doc.Problems, Problem{ns, ne, fmt.Sprintf("Unexpected closing tag </%s>.", name), sev})
				i = end
				continue
			}
			for len(stack)-1 > match {
				n := stack[len(stack)-1]
				if !optionalEnd[n.Tag] {
					doc.Problems = append(doc.Problems, Problem{n.NameStart, n.NameEnd,
						fmt.Sprintf("Element <%s> is not closed before </%s>.", n.Tag, name), 1})
				}
				closeTop(i)
			}
			n := stack[match]
			n.CloseStart, n.CloseNameStart, n.CloseEnd = i, ns, end
			closeTop(end)
			i = end
		default:
			ns := i + 1
			ne := ns
			for ne < len(src) && isNameChar(src[ne]) {
				ne++
			}
			if ne == ns || !isAlpha(src[ns]) {
				i++ // a lone '<' in text
				continue
			}
			n := &Node{Tag: strings.ToLower(src[ns:ne]), Start: i, NameStart: ns, NameEnd: ne,
				CloseStart: -1, CloseNameStart: -1, CloseEnd: -1, ContentStart: -1, ContentEnd: -1}
			j := parseAttrs(src, ne, n, doc)
			n.OpenEnd = j

			// Implied end tags.
			for len(stack) > 0 {
				top := stack[len(stack)-1].Tag
				if (top == "p" && closesP[n.Tag]) || impliedBy[n.Tag][top] {
					closeTop(i)
					continue
				}
				break
			}
			add(n)
			switch {
			case n.SelfClosed || voidElements[n.Tag]:
				n.End = j
			case rawText[n.Tag]:
				n.ContentStart = j
				k := indexFold(src[j:], "</"+n.Tag)
				if k < 0 {
					n.ContentEnd, n.End = len(src), len(src)
					doc.Problems = append(doc.Problems, Problem{n.NameStart, n.NameEnd, fmt.Sprintf("Element <%s> is not closed.", n.Tag), 1})
					j = len(src)
				} else {
					n.ContentEnd = j + k
					n.CloseStart, n.CloseNameStart = j+k, j+k+2
					gt := strings.IndexByte(src[j+k:], '>')
					n.CloseEnd = len(src)
					if gt >= 0 {
						n.CloseEnd = j + k + gt + 1
					}
					n.End = n.CloseEnd
					j = n.CloseEnd
				}
			default:
				stack = append(stack, n)
			}
			i = j
		}
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		if !optionalEnd[n.Tag] {
			doc.Problems = append(doc.Problems, Problem{n.NameStart, n.NameEnd, fmt.Sprintf("Element <%s> is not closed.", n.Tag), 1})
		}
		closeTop(len(src))
	}
	return doc
}

// parseAttrs reads attributes from i (just after the tag name) to the end of the start tag and
// returns the position after '>'.
func parseAttrs(src string, i int, n *Node, doc *Document) int {
	seen := map[string]bool{}
	for i < len(src) {
		for i < len(src) && isSpace(src[i]) {
			i++
		}
		if i >= len(src) {
			break
		}
		switch src[i] {
		case '>':
			return i + 1
		case '/':
			if i+1 < len(src) && src[i+1] == '>' {
				n.SelfClosed = true
				return i + 2
			}
			i++
			continue
		case '<':
			// Unterminated start tag; the next tag begins here.
			doc.Problems = append(doc.Problems, Problem{n.NameStart, n.NameEnd, fmt.Sprintf("Start tag <%s> is not closed with '>'.", n.Tag), 1})
			return i
		}
		a := Attr{NStart: i, VStart: -1, VEnd: -1}
		for i < len(src) && !isSpace(src[i]) && src[i] != '=' && src[i] != '>' && !(src[i] == '/' && i+1 < len(src) && src[i+1] == '>') && src[i] != '<' {
			i++
		}
		a.NEnd = i
		a.Name = strings.ToLower(src[a.NStart:a.NEnd])
		j := i
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if j < len(src) && src[j] == '=' {
			j++
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '"' || src[j] == '\'') {
				q := src[j]
				k := strings.IndexByte(src[j+1:], q)
				a.VStart = j + 1
				if k < 0 {
					a.VEnd = len(src)
					i = len(src)
				} else {
					a.VEnd = j + 1 + k
					i = a.VEnd + 1
				}
			} else {
				a.VStart = j
				for j < len(src) && !isSpace(src[j]) && src[j] != '>' {
					j++
				}
				a.VEnd = j
				i = j
			}
			a.Value = src[a.VStart:a.VEnd]
		}
		if a.Name != "" {
			if seen[a.Name] {
				doc.Problems = append(doc.Problems, Problem{a.NStart, a.NEnd, fmt.Sprintf("Duplicate attribute %q.", a.Name), 2})
			}
			seen[a.Name] = true
			n.Attrs = append(n.Attrs, a)
		}
		if i == a.NStart {
			i++
		}
	}
	return len(src)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isNameChar(c byte) bool {
	return isAlpha(c) || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' || c == '.'
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}

// Attr returns the attribute with that name.
func (n *Node) Attr(name string) *Attr {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			return &n.Attrs[i]
		}
	}
	return nil
}

// Walk visits every element depth-first.
func (d *Document) Walk(fn func(n *Node)) {
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			fn(n)
			walk(n.Children)
		}
	}
	walk(d.Roots)
}

// NodeAt returns the innermost element whose range contains off.
func (d *Document) NodeAt(off int) *Node {
	var found *Node
	d.Walk(func(n *Node) {
		if off >= n.Start && off <= n.End {
			found = n
		}
	})
	return found
}
