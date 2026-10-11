package css

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "a { color: red; }", "@media (max-width: 1px) { .x:hover::before { content: \"}\"; } }", "a { b: c", "/* x", "@import url(\"x\");", ":root { --v: 1 } a { w: var(--v) }", "a{b:\"\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s, false)
		Parse(s, true)
		Check(s, false)
		Format(s, "  ", "\n")
	})
}
