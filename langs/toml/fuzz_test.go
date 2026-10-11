package toml

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "[a.b]\nx = 1\ny = [1, 2.0, 1979-05-27T07:32:00Z]\n[[arr]]\nz = { k = \"v\" }\n", "a = \"\"\"\nmulti\n\"\"\"", "a = \x27\x27\x27x", "[a\nb=", "a.b = 1\na = 2", "x = 0x_1", "k = \"\\u00e9\\U0001F600\""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s)
		Format(s)
	})
}
