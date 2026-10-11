package nginx

import (
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

const conf = `user nginx;
worker_processes auto;
events { worker_connections 1024; }
http {
    map $http_upgrade $connection_upgrade { default upgrade; '' close; }
    upstream app { server 127.0.0.1:8080; }
    server {
        listen 80;
        server_name example.com;
        location / {
            proxy_pass http://app;
            if ($host = "x") { return 301 https://$host$request_uri; }
        }
        proxy_pass http://app;
        frobnicate on;
    }
}
`

func TestParseAndDiagnostics(t *testing.T) {
	cfg := Parse(conf)
	if len(cfg.Problems) != 0 {
		t.Fatalf("problems = %+v", cfg.Problems)
	}
	if len(cfg.Stmts) != 4 || cfg.Stmts[3].Name != "http" || cfg.Stmts[3].Block[2].Name != "server" {
		t.Fatalf("tree = %+v", cfg.Stmts)
	}

	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///etc/nginx")
	uri := c.Open("file:///etc/nginx/nginx.conf", "nginx", conf)
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("frobnicate")))
	if !strings.Contains(msgs, `Unknown directive "frobnicate"`) || !strings.Contains(msgs, `"proxy_pass" is not allowed in server`) {
		t.Fatalf("diagnostics:\n%s", msgs)
	}
	for _, bad := range []string{"default", "return", `"if"`, "server_name", "worker_connections"} {
		if strings.Contains(msgs, bad) {
			t.Errorf("false positive %q:\n%s", bad, msgs)
		}
	}

	c.Replace(uri, "http {\n    server {\n        listen 80\n    }\n")
	msgs = lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("Missing")))
	if !strings.Contains(msgs, `Missing ';' after "listen"`) || !strings.Contains(msgs, `Block "http" is not closed`) {
		t.Fatalf("syntax diagnostics:\n%s", msgs)
	}

	// A fragment (sites-enabled/x.conf) has no main context: top-level server is fine.
	frag := c.Open("file:///etc/nginx/sites-enabled/x.conf", "nginx", "server {\n    listen 80;\n}\n")
	if d := c.WaitDiagnostics(frag, func([]protocol.Diagnostic) bool { return true }); len(d) != 0 {
		t.Fatalf("fragment diagnostics = %+v", d)
	}
}

func TestFeatures(t *testing.T) {
	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///etc/nginx")
	uri := c.Open("file:///etc/nginx/nginx.conf", "nginx", "")
	labels := func(text string, line, char uint32) map[string]protocol.CompletionItem {
		c.Replace(uri, text)
		m := map[string]protocol.CompletionItem{}
		if l := c.Complete(uri, line, char); l != nil {
			for _, it := range l.Items {
				m[it.Label] = it
			}
		}
		return m
	}
	got := labels("http {\n    server {\n        location / {\n            prox\n        }\n    }\n}\n", 3, 16)
	if _, ok := got["proxy_pass"]; !ok {
		t.Error("proxy_pass not offered in location")
	}
	if _, ok := got["worker_processes"]; ok {
		t.Error("main-only directive offered in location")
	}
	if it := got["proxy_pass"]; it.TextEdit == nil || it.TextEdit.NewText != "proxy_pass $0;" {
		t.Errorf("snippet = %+v", it.TextEdit)
	}
	got = labels("http {\n    gzip \n}\n", 1, 9)
	if _, ok := got["on"]; !ok {
		t.Errorf("choices = %v", got)
	}
	got = labels("http {\n    server { return 301 $re }\n}\n", 1, 27)
	if _, ok := got["$request_uri"]; !ok {
		t.Errorf("variables = %v", len(got))
	}

	c.Replace(uri, conf)
	if h := c.Hover(uri, 10, 15); h == nil || !strings.Contains(h.Contents.Value, "proxied server") {
		t.Errorf("hover = %+v", h)
	}
	var locs []protocol.Location
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Line: 10, Character: 32}}}, &locs)
	if len(locs) != 1 || locs[0].Range.Start.Line != 5 {
		t.Errorf("upstream definition = %+v", locs)
	}
	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 2 || syms[1].Name != "http" || syms[1].Children[2].Detail != "80 example.com" {
		t.Errorf("symbols = %+v", syms)
	}
}

func TestReindent(t *testing.T) {
	src := "http {\nserver {\n  listen 80;   \n\n\n location ~ ^/(a|b)\\{ {\nset $x \"{\";\n}\n}\n}"
	want := "http {\n    server {\n        listen 80;\n\n        location ~ ^/(a|b)\\{ {\n            set $x \"{\";\n        }\n    }\n}\n"
	if got := reindent(src, "    "); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	_ = document.New
}
