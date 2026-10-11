package dockerfile

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "# syntax=docker/dockerfile:1\nFROM alpine:3 AS base\nRUN apk add x \\\n  && y\nCOPY --from=base /a /b\nCMD [\"a\", \"b\"]\n", "FROM", "RUN [\"", "ARG x=${y:-z}\nENV a=b c", "HEALTHCHECK --interval=1s CMD x", "\\\n\\\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { Parse(s) })
}
