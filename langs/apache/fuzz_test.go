package apache

import (
	"context"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{"", "<VirtualHost *:80>\n  ServerName a\n  <Directory />\n    Require all granted\n  </Directory>\n</VirtualHost>\n", "<Directory", "</Directory>", "Listen 80 \\\n  https", "<IfModule !x.c>\n<IfModule", "Alias \"/a b\" /c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		Parse(s)
		doc := document.New("file:///x/httpd.conf", "apache", 1, s, "")
		(&Server{}).Format(context.Background(), doc, protocol.FormattingOptions{TabSize: 4, InsertSpaces: true})
	})
}
