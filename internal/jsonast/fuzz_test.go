package jsonast

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "{", "{\"a\": [1, 2.5e3, true, null, \"x\\u00e9\"], // c\n \"b\": {}}", "[1,,2]", "\"\\", "{\"a\":1}}", "/* x */ 1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s, Options{Comments: true})
		Parse(s, Options{})
		Format(s, FormatOptions{})
	})
}
