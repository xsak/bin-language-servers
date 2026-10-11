package css

import (
	"fmt"
	"strings"

	"github.com/xsak/bin-language-servers/internal/webdata"
)

// globalKeywords are valid for every property.
var globalKeywords = []string{"inherit", "initial", "unset", "revert", "revert-layer"}

// descriptorAtRules contain descriptors, not properties, so the property check is skipped.
var descriptorAtRules = map[string]bool{"@font-face": true, "@page": true, "@counter-style": true,
	"@property": true, "@font-feature-values": true, "@font-palette-values": true, "@view-transition": true,
	"@color-profile": true, "@viewport": true}

func vendorPrefixed(name string) bool {
	for _, p := range []string{"-webkit-", "-moz-", "-ms-", "-o-", "-epub-", "-apple-", "-khtml-", "-xv-"} {
		if strings.HasPrefix(strings.ToLower(name), p) {
			return true
		}
	}
	return false
}

// Check returns syntax and lint problems for a stylesheet (or inline declarations).
func Check(src string, inline bool) []Problem {
	sheet := Parse(src, inline)
	out := append([]Problem{}, sheet.Problems...)
	data := webdata.CSS()
	Walk(sheet.Items, func(it Item, parents []*Rule) {
		switch v := it.(type) {
		case *Decl:
			if v.Name == "" || strings.HasPrefix(v.Name, "--") || vendorPrefixed(v.Name) || strings.HasPrefix(v.Name, "$") {
				return
			}
			for _, p := range parents {
				if descriptorAtRules[p.Name] {
					return
				}
			}
			prop := webdata.Find(data.Properties, v.Name)
			if prop == nil {
				out = append(out, Problem{v.NStart, v.NEnd, fmt.Sprintf("Unknown property: '%s'.", v.Name), SevWarning})
				return
			}
			if prop.Status == "obsolete" {
				out = append(out, Problem{v.NStart, v.NEnd, fmt.Sprintf("'%s' is obsolete.", v.Name), SevHint})
			}
			if bad := badEnumValue(prop, v.Value); bad != "" {
				out = append(out, Problem{v.VStart, v.VStart + len(v.Value),
					fmt.Sprintf("'%s' is not a valid value for '%s'.", bad, v.Name), SevWarning})
			}
		case *Rule:
			if v.At {
				if !vendorPrefixed(v.Name[1:]) && webdata.Find(data.AtDirectives, v.Name) == nil && v.Name != "@layer" &&
					v.Name != "@container" && v.Name != "@scope" && v.Name != "@starting-style" {
					out = append(out, Problem{v.PStart, v.PStart + len(v.Name), fmt.Sprintf("Unknown at-rule %s.", v.Name), SevWarning})
				}
				return
			}
			out = append(out, checkSelector(src, v)...)
		}
	})
	return out
}

// badEnumValue returns the value if the property only takes keywords and this one isn't among
// them; "" otherwise. Only single identifiers are judged.
func badEnumValue(prop *webdata.Entry, value string) string {
	if len(prop.Values) == 0 || len(prop.Restrictions) != 1 || prop.Restrictions[0] != "enum" {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || !isPlainIdent(v) {
		return ""
	}
	for _, g := range globalKeywords {
		if v == g {
			return ""
		}
	}
	for _, e := range prop.Values {
		if strings.EqualFold(e.Name, v) {
			return ""
		}
	}
	return value
}

func isPlainIdent(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isIdent(s[i]) {
			return false
		}
	}
	return s != "" && !vendorPrefixed(s) && !strings.HasPrefix(s, "--")
}

// checkSelector reports unknown pseudo-classes and pseudo-elements.
func checkSelector(src string, r *Rule) []Problem {
	var out []Problem
	data := webdata.CSS()
	for _, ps := range pseudos(src, r.PStart, r.PEnd) {
		list := data.PseudoClasses
		if strings.HasPrefix(ps.name, "::") {
			list = data.PseudoElements
		}
		if vendorPrefixed(strings.TrimLeft(ps.name, ":")) || findPseudo(list, ps.name) != nil {
			continue
		}
		out = append(out, Problem{ps.start, ps.start + len(ps.name), fmt.Sprintf("Unknown pseudo-selector %s.", ps.name), SevWarning})
	}
	return out
}

func findPseudo(list []webdata.Entry, name string) *webdata.Entry {
	if e := webdata.Find(list, name); e != nil {
		return e
	}
	return webdata.Find(list, name+"()")
}

type pseudo struct {
	name  string // with the leading colon(s)
	start int
}

// pseudos finds :name and ::name in a selector, skipping strings and attribute selectors.
func pseudos(src string, start, end int) []pseudo {
	var out []pseudo
	depth := 0
	for i := start; i < end; i++ {
		switch c := src[i]; c {
		case '"', '\'':
			for i++; i < end && src[i] != c; i++ {
			}
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth > 0 {
				continue
			}
			j := i + 1
			if j < end && src[j] == ':' {
				j++
			}
			k := j
			for k < end && isIdent(src[k]) {
				k++
			}
			if k > j {
				out = append(out, pseudo{name: strings.ToLower(src[i:k]), start: i})
			}
			i = k - 1
		}
	}
	return out
}

// Specificity computes (ids, classes/attributes/pseudo-classes, elements/pseudo-elements) of
// one complex selector. :is/:not/:has take their most specific argument; :where counts zero.
func Specificity(sel string) [3]int {
	var s [3]int
	for i := 0; i < len(sel); i++ {
		c := sel[i]
		switch {
		case c == '#':
			s[0]++
			i = skipIdent(sel, i+1) - 1
		case c == '.':
			s[1]++
			i = skipIdent(sel, i+1) - 1
		case c == '[':
			s[1]++
			for i < len(sel) && sel[i] != ']' {
				i++
			}
		case c == ':':
			if i+1 < len(sel) && sel[i+1] == ':' {
				s[2]++
				i = skipIdent(sel, i+2) - 1
				continue
			}
			j := skipIdent(sel, i+1)
			name := strings.ToLower(sel[i+1 : j])
			if j < len(sel) && sel[j] == '(' {
				arg, k := parenArg(sel, j)
				switch name {
				case "where":
				case "is", "not", "has", "matches":
					best := [3]int{}
					for _, a := range splitTop(arg, ',') {
						if sp := Specificity(a); less(best, sp) {
							best = sp
						}
					}
					for n := range s {
						s[n] += best[n]
					}
				default:
					s[1]++
				}
				i = k
				continue
			}
			if name == "before" || name == "after" || name == "first-line" || name == "first-letter" {
				s[2]++ // legacy single-colon pseudo-elements
			} else {
				s[1]++
			}
			i = j - 1
		case isIdent(c) && c != '-' || c == '-' && i+1 < len(sel) && isIdent(sel[i+1]):
			if i == 0 || strings.IndexByte(" >+~(,\t\n", sel[i-1]) >= 0 {
				s[2]++
			}
			i = skipIdent(sel, i) - 1
		}
	}
	return s
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func skipIdent(s string, i int) int {
	for i < len(s) && isIdent(s[i]) {
		i++
	}
	return i
}

// parenArg returns the text inside the parentheses starting at s[open] and the index of ')'.
func parenArg(s string, open int) (string, int) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i
			}
		}
	}
	return s[open+1:], len(s) - 1
}

// splitTop splits on sep outside parentheses, brackets and strings.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\'':
			for i++; i < len(s) && s[i] != c; i++ {
			}
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
