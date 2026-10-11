package ini

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "[Unit]\nDescription=x\n[Service]\nType=simple\nExecStart=/bin/true \\\n  --flag\n", "[a\nb=", "=", "; c\n# d\nk = v ; x", "[Container]\nImage=x\n", "k=\"\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s, "x.ini")
		Parse(s, "x.service")
		Parse(s, "x.container")
	})
}
