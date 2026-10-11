package docker

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
)

func TestRouting(t *testing.T) {
	for name, want := range map[string]bool{
		"Dockerfile": true, "Containerfile": true, "Dockerfile.dev": true, "api.dockerfile": true,
		"compose.yaml": false, "docker-compose.yml": false,
	} {
		doc := document.New(document.PathToURI("/p/"+name), "", 1, "", "")
		if got := isDockerfile(doc); got != want {
			t.Errorf("%s: dockerfile=%v, want %v", name, got, want)
		}
	}

	t.Setenv("BLS_OFFLINE", "1")
	t.Setenv("PATH", "/nonexistent")
	c := lsptest.Start(t, &Server{})
	dir := t.TempDir()
	c.Initialize(document.PathToURI(dir))

	df := c.Open(document.PathToURI(filepath.Join(dir, "Dockerfile")), "dockerfile", "FROM alpine:3\nfrob\n")
	if msgs := lsptest.Messages(c.WaitDiagnostics(df, lsptest.Contains("FROB"))); !strings.Contains(msgs, "Unknown instruction FROB") {
		t.Errorf("dockerfile diagnostics: %s", msgs)
	}
	cf := c.Open(document.PathToURI(filepath.Join(dir, "compose.yaml")), "dockercompose",
		"services:\n  a:\n    depends_on: [nope]\n")
	if msgs := lsptest.Messages(c.WaitDiagnostics(cf, lsptest.Contains("nope"))); !strings.Contains(msgs, `Service "nope" is not defined.`) {
		t.Errorf("compose diagnostics: %s", msgs)
	}
}
