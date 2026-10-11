package yamlast

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "a: 1\nb:\n  - x\n  - y: z\n---\nc: *d\n", "- [1, {a: b}]\n", "a: |\n  x\n y", "? a\n: b", "&a [*a]", "!!binary x", "a: \"\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { Parse(s) })
}
