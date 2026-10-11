package confdata

import (
	"strings"
	"testing"
)

func TestTables(t *testing.T) {
	a := Apache()
	if d := a.Lookup("documentroot"); len(d) != 1 || d[0].Name != "DocumentRoot" || len(d[0].Contexts) == 0 {
		t.Fatalf("apache DocumentRoot = %+v", d)
	}
	if d := a.Lookup("VirtualHost"); len(d) != 1 || !d[0].Container {
		t.Fatalf("apache VirtualHost = %+v", d)
	}
	n := Nginx()
	if d := n.Lookup("proxy_pass"); len(d) < 2 { // http and stream
		t.Fatalf("nginx proxy_pass = %+v", d)
	}
	if d := n.Lookup("gzip"); len(d) == 0 || !strings.Contains(d[0].Markdown(), "nginx.org") {
		t.Fatalf("nginx gzip = %+v", d)
	}
}
