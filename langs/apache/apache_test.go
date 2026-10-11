package apache

import (
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

const conf = `ServerRoot "/etc/httpd"
Listen 80
<IfModule mod_ssl.c>
    Listen 443
</IfModule>
<VirtualHost *:80>
    ServerName example.com
    DocumentRoot /var/www/html
    <Directory /var/www/html>
        Options -Indexes
        AllowOverride None
        Require all granted
        Listen 8080
    </Directory>
    Frobnicate on
</VirtualHost>
`

func TestDiagnostics(t *testing.T) {
	roots, probs := Parse(conf)
	if len(probs) != 0 || len(roots) != 4 || !roots[3].Container || len(roots[3].Children) != 4 {
		t.Fatalf("roots %+v probs %+v", roots, probs)
	}
	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///etc/httpd")
	uri := c.Open("file:///etc/httpd/conf/httpd.conf", "apache", conf)
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("Frobnicate")))
	if !strings.Contains(msgs, `Unknown directive "Frobnicate"`) || !strings.Contains(msgs, "Listen is not allowed in directory context") {
		t.Fatalf("diagnostics:\n%s", msgs)
	}
	if strings.Contains(msgs, "Require") || strings.Contains(msgs, "ServerName") || strings.Contains(msgs, "Listen is not allowed in server") {
		t.Fatalf("false positive:\n%s", msgs)
	}
	c.Replace(uri, "<VirtualHost *:80>\n</Directory>\n")
	msgs = lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("Unexpected")))
	if !strings.Contains(msgs, "Unexpected </Directory>") || !strings.Contains(msgs, "<VirtualHost> is not closed") {
		t.Fatalf("structure:\n%s", msgs)
	}
	ht := c.Open("file:///var/www/.htaccess", "apache", "RewriteEngine On\nListen 80\n")
	msgs = lsptest.Messages(c.WaitDiagnostics(ht, lsptest.Contains("Listen")))
	if !strings.Contains(msgs, "Listen is not allowed in .htaccess") || strings.Contains(msgs, "RewriteEngine") {
		t.Fatalf(".htaccess:\n%s", msgs)
	}
}

func TestFeatures(t *testing.T) {
	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///etc/httpd")
	uri := c.Open("file:///etc/httpd/conf/httpd.conf", "apache", "")
	labels := func(text string, line, char uint32) map[string]protocol.CompletionItem {
		c.Replace(uri, text)
		m := map[string]protocol.CompletionItem{}
		for _, it := range c.Complete(uri, line, char).Items {
			m[it.Label] = it
		}
		return m
	}
	got := labels("<VirtualHost *:80>\n    Serv\n</VirtualHost>\n", 1, 8)
	if _, ok := got["ServerName"]; !ok {
		t.Error("ServerName not offered in virtual host")
	}
	if _, ok := got["ServerRoot"]; ok {
		t.Error("server-config-only directive offered in virtual host")
	}
	got = labels("<Dir", 0, 4)
	if it, ok := got["Directory"]; !ok || !strings.Contains(it.TextEdit.NewText, "</Directory>") {
		t.Errorf("container completion = %+v", it)
	}
	got = labels("<Directory /x>\n    Options x\n</", 2, 2)
	if it, ok := got["/Directory"]; !ok || it.TextEdit.NewText != "Directory>" {
		t.Errorf("close completion = %v", got)
	}
	got = labels("AcceptPathInfo ", 0, 15)
	if _, ok := got["Off"]; !ok {
		t.Errorf("choices = %v", got)
	}

	c.Replace(uri, conf)
	if h := c.Hover(uri, 6, 6); h == nil || !strings.Contains(h.Contents.Value, "ServerName") {
		t.Errorf("hover = %+v", h)
	}
	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 2 || syms[1].Name != "VirtualHost *:80" || syms[1].Detail != "example.com" || len(syms[1].Children) != 1 {
		t.Errorf("symbols = %+v", syms)
	}
	s := &Server{}
	doc := document.New("file:///x.conf", "apache", 1, "<VirtualHost *:80>\nServerName a\n  <Directory /x>\nRequire all granted\n</Directory>\n\n\n</VirtualHost>\n", "")
	edits, _ := s.Format(nil, doc, protocol.FormattingOptions{TabSize: 4, InsertSpaces: true})
	want := "<VirtualHost *:80>\n    ServerName a\n    <Directory /x>\n        Require all granted\n    </Directory>\n\n</VirtualHost>\n"
	if len(edits) != 1 || edits[0].NewText != want {
		t.Errorf("format = %+v", edits)
	}
}
