package html

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "<!DOCTYPE html><html><body><p>a<p>b<ul><li>x</ul><img src=x alt=\"\"></body></html>", "<div><span></div>", "<pre>\n  x\n</pre><script>if (a < b) {}</script><style>a{}</style>", "<a href=\"", "<!-- x", "<textarea></div></textarea>", "</p>", "<br/><input disabled>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s)
		Format(s, "  ", "\n")
	})
}
