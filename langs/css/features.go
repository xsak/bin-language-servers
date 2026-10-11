package css

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/webdata"
)

// CompletionItem is a completion candidate with the byte range it replaces.
type CompletionItem struct {
	Label, Detail, Doc string
	Insert             string // text (a snippet if Snippet)
	Snippet            bool
	Kind               protocol.CompletionItemKind
	Start, End         int
}

// context kinds
const (
	ctxNone = iota
	ctxProperty
	ctxValue
	ctxSelector
)

type location struct {
	kind  int
	decl  *Decl // for property/value
	rule  *Rule // for selector
	inAt  []*Rule
	start int // word start
}

// locate finds what is being edited at off.
func locate(src string, sheet *Sheet, off int, inline bool) location {
	var walk func(items []Item, inBlock bool, parents []*Rule) location
	walk = func(items []Item, inBlock bool, parents []*Rule) location {
		for _, it := range items {
			switch v := it.(type) {
			case *Comment:
				if off > v.Start && off < v.End {
					return location{kind: ctxNone}
				}
			case *Decl:
				nameEnd := v.NEnd
				if v.Colon >= 0 {
					nameEnd = v.Colon
				}
				if off >= v.NStart && off <= nameEnd {
					return location{kind: ctxProperty, decl: v, inAt: parents}
				}
				if v.Colon >= 0 && off > v.Colon && off <= max(v.VEnd, v.Colon+1) && (v.End == v.VEnd || off < v.End) {
					return location{kind: ctxValue, decl: v, inAt: parents}
				}
			case *Rule:
				if v.Block != nil && off > v.Block.Open && (v.Block.Close < 0 || off <= v.Block.Close) {
					if v.Block.Close >= 0 && off == v.Block.Close+1 {
						break
					}
					return walk(v.Block.Items, true, append(parents, v))
				}
				if off >= v.PStart && off <= v.PEnd {
					return location{kind: ctxSelector, rule: v, inAt: parents}
				}
			}
		}
		if inBlock || inline {
			return location{kind: ctxProperty, inAt: parents}
		}
		return location{kind: ctxSelector, inAt: parents}
	}
	return walk(sheet.Items, false, nil)
}

func wordStart(src string, off int) int {
	i := off
	for i > 0 && isIdent(src[i-1]) {
		i--
	}
	return i
}

// Complete returns completion candidates at off.
func Complete(src string, off int, inline, snippets bool) []CompletionItem {
	sheet := Parse(src, inline)
	loc := locate(src, sheet, off, inline)
	data := webdata.CSS()
	ws := wordStart(src, off)
	var out []CompletionItem
	add := func(label, detail, doc, insert string, snippet bool, kind protocol.CompletionItemKind, start int) {
		out = append(out, CompletionItem{Label: label, Detail: detail, Doc: doc, Insert: insert, Snippet: snippet,
			Kind: kind, Start: start, End: off})
	}

	switch loc.kind {
	case ctxProperty:
		props := make([]webdata.Entry, 0, len(data.Properties))
		props = append(props, data.Properties...)
		sort.SliceStable(props, func(i, j int) bool { return props[i].Relevance > props[j].Relevance })
		for _, p := range props {
			if p.Status == "obsolete" {
				continue
			}
			insert, snip := p.Name+": ", false
			if snippets {
				insert, snip = p.Name+": $0;", true
			}
			add(p.Name, "property", p.Markdown(), insert, snip, protocol.CompletionKindProperty, ws)
		}
	case ctxValue:
		d := loc.decl
		prop := webdata.Find(data.Properties, d.Name)
		before := src[d.Colon+1 : off]
		if i := strings.LastIndex(before, "var("); i >= 0 && !strings.Contains(before[i:], ")") {
			for _, name := range customProperties(sheet) {
				add(name, "custom property", "", name, false, protocol.CompletionKindVariable, wordStart(src, off))
			}
			break
		}
		if prop != nil {
			for _, v := range prop.Values {
				insert := v.Name
				if strings.HasSuffix(insert, "()") && snippets {
					add(v.Name, "", v.Markdown(), strings.TrimSuffix(insert, ")")+"$1)", true, protocol.CompletionKindFunction, ws)
					continue
				}
				add(v.Name, "", v.Markdown(), insert, false, protocol.CompletionKindValue, ws)
			}
			if strings.Contains(prop.Syntax, "<color>") {
				for _, c := range namedColors {
					add(c, "color", "", c, false, protocol.CompletionKindValue, ws)
				}
			}
		}
		for _, g := range globalKeywords {
			add(g, "global keyword", "", g, false, protocol.CompletionKindKeyword, ws)
		}
		if snippets {
			add("var()", "custom property", "", "var(--$1)", true, protocol.CompletionKindFunction, ws)
		}
	case ctxSelector:
		start := ws
		switch {
		case start >= 2 && src[start-2:start] == "::":
			for _, p := range data.PseudoElements {
				add(p.Name, "pseudo-element", p.Markdown(), strings.TrimPrefix(p.Name, "::"), false, protocol.CompletionKindFunction, start)
			}
		case start >= 1 && src[start-1] == ':':
			for _, p := range data.PseudoClasses {
				name := strings.TrimPrefix(p.Name, ":")
				insert, snip := name, false
				if strings.HasSuffix(name, "()") && snippets {
					insert, snip = strings.TrimSuffix(name, ")")+"$1)", true
				}
				add(p.Name, "pseudo-class", p.Markdown(), insert, snip, protocol.CompletionKindFunction, start)
			}
		case start >= 1 && src[start-1] == '@':
			for _, a := range data.AtDirectives {
				add(a.Name, "at-rule", a.Markdown(), strings.TrimPrefix(a.Name, "@"), false, protocol.CompletionKindKeyword, start)
			}
		case start >= 1 && (src[start-1] == '.' || src[start-1] == '#'):
			for _, n := range selectorNames(sheet, src, src[start-1]) {
				add(n, "", "", n, false, protocol.CompletionKindValue, start)
			}
		default:
			for _, t := range webdata.HTML().Tags {
				add(t.Name, "element", t.Markdown(), t.Name, false, protocol.CompletionKindKeyword, start)
			}
		}
	}
	return out
}

// customProperties lists --names declared in the sheet.
func customProperties(sheet *Sheet) []string {
	var out []string
	seen := map[string]bool{}
	Walk(sheet.Items, func(it Item, _ []*Rule) {
		if d, ok := it.(*Decl); ok && strings.HasPrefix(d.Name, "--") && !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d.Name)
		}
	})
	return out
}

// selectorNames lists class (prefix '.') or id (prefix '#') names used in selectors.
func selectorNames(sheet *Sheet, src string, prefix byte) []string {
	var out []string
	seen := map[string]bool{}
	Walk(sheet.Items, func(it Item, _ []*Rule) {
		r, ok := it.(*Rule)
		if !ok || r.At {
			return
		}
		sel := src[r.PStart:r.PEnd]
		for i := 0; i < len(sel); i++ {
			if sel[i] == prefix {
				j := skipIdent(sel, i+1)
				if n := sel[i+1 : j]; n != "" && !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
				i = j - 1
			}
		}
	})
	return out
}

// Hover describes what is at off: a property, a keyword value, an at-rule, a pseudo-selector or
// the specificity of a selector.
func Hover(src string, off int, inline bool) (md string, start, end int, ok bool) {
	sheet := Parse(src, inline)
	loc := locate(src, sheet, off, inline)
	data := webdata.CSS()
	ws := wordStart(src, off)
	we := skipIdent(src, off)
	word := src[ws:we]
	switch loc.kind {
	case ctxProperty:
		if loc.decl == nil {
			return
		}
		if p := webdata.Find(data.Properties, loc.decl.Name); p != nil {
			return p.Markdown(), loc.decl.NStart, loc.decl.NEnd, true
		}
	case ctxValue:
		if p := webdata.Find(data.Properties, loc.decl.Name); p != nil && word != "" {
			if v := webdata.Find(p.Values, word); v != nil && v.Description != "" {
				return v.Markdown(), ws, we, true
			}
		}
	case ctxSelector:
		r := loc.rule
		if r == nil {
			return
		}
		if r.At {
			if a := webdata.Find(data.AtDirectives, r.Name); a != nil && off <= r.PStart+len(r.Name) {
				return a.Markdown(), r.PStart, r.PStart + len(r.Name), true
			}
			return
		}
		for _, ps := range pseudos(src, r.PStart, r.PEnd) {
			if off >= ps.start && off <= ps.start+len(ps.name) {
				list := data.PseudoClasses
				if strings.HasPrefix(ps.name, "::") {
					list = data.PseudoElements
				}
				if e := findPseudo(list, ps.name); e != nil {
					return e.Markdown(), ps.start, ps.start + len(ps.name), true
				}
			}
		}
		// The selector of the list item under the cursor, with its specificity.
		sel := src[r.PStart:r.PEnd]
		pos := r.PStart
		for _, part := range splitTop(sel, ',') {
			if off >= pos && off <= pos+len(part) {
				sp := Specificity(part)
				text := strings.Join(strings.Fields(part), " ")
				return fmt.Sprintf("```css\n%s\n```\n\n[Selector specificity](https://developer.mozilla.org/docs/Web/CSS/Specificity): (%d, %d, %d)",
					text, sp[0], sp[1], sp[2]), pos + (len(part) - len(strings.TrimLeft(part, " \t\r\n"))), pos + len(strings.TrimRight(part, " \t\r\n")), true
			}
			pos += len(part) + 1
		}
	}
	return
}

// Symbol is an outline entry with byte ranges.
type Symbol struct {
	Name             string
	Kind             protocol.SymbolKind
	Start, End       int
	SelStart, SelEnd int
	Children         []Symbol
}

// Symbols builds the outline: rules (nested), at-rules and custom properties.
func Symbols(src string) []Symbol {
	sheet := Parse(src, false)
	var build func(items []Item) []Symbol
	build = func(items []Item) []Symbol {
		var out []Symbol
		for _, it := range items {
			switch v := it.(type) {
			case *Rule:
				name := strings.Join(strings.Fields(v.Prelude), " ")
				if len(name) > 80 {
					name = name[:77] + "..."
				}
				kind := protocol.SymbolClass
				if v.At {
					kind = protocol.SymbolNamespace
				}
				sym := Symbol{Name: name, Kind: kind, Start: v.Start, End: v.End, SelStart: v.PStart, SelEnd: v.PEnd}
				if v.Block != nil {
					sym.Children = build(v.Block.Items)
				}
				out = append(out, sym)
			case *Decl:
				if strings.HasPrefix(v.Name, "--") {
					out = append(out, Symbol{Name: v.Name, Kind: protocol.SymbolVariable, Start: v.NStart, End: v.End,
						SelStart: v.NStart, SelEnd: v.NEnd})
				}
			}
		}
		return out
	}
	return build(sheet.Items)
}

// namedColors are the CSS named colors plus special color keywords.
var namedColors = strings.Fields(`currentcolor transparent aliceblue antiquewhite aqua aquamarine azure beige
bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral
cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki
darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray
darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite
forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred
indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan
lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue
lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine
mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise
mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange
orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum
powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna
silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise
violet wheat white whitesmoke yellow yellowgreen`)
