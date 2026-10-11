package nginx

import (
	"context"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "http {\n  server {\n    listen 80;\n    location / { proxy_pass http://up; }\n  }\n  upstream up { server 127.0.0.1:1; }\n}\n", "a b \"c d\" \x27e;\x27 {", "}}", "# c\nevents {}", "map $x $y { ~^a 1; default 0; }", "if ($x) { return 301 \\{; }"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s)
		doc := document.New("file:///x/nginx.conf", "nginx", 1, s, "")
		(&Server{}).Format(context.Background(), doc, protocol.FormattingOptions{TabSize: 4, InsertSpaces: true})
	})
}
