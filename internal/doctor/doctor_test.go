package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	bls := filepath.Join(home, ".local/bin/bls")
	write(t, bls, "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(home, ".config/gram/settings.jsonc"), `{
  // comment
  "lsp": {
    "json-language-server": { "binary": { "path": "`+bls+`", "arguments": ["json"] } },
    "yaml-language-server": { "binary": { "path": "`+bls+`", "arguments": ["pyth"] } },
    "bash-language-server": { "binary": { "path": "/nope/bls", "arguments": ["bash"] } },
  },
}`, 0o644)
	write(t, filepath.Join(home, ".local/share/gram/extensions/installed/nginx/extension.toml"), "", 0o644)
	write(t, filepath.Join(home, ".local/share/gram/logs/Gram.log"),
		"2026 ERROR language server \"ruff\": Download disabled for language server 'ruff'\n", 0o644)
	write(t, filepath.Join(home, ".config/zed/settings.json"), `{"lsp": {}}`, 0o644)
	write(t, filepath.Join(home, ".local/share/zed/node/node-v24/bin/node"), "", 0o755)

	var b strings.Builder
	code := Run(&b, Options{Version: "test", Languages: []string{"json", "yaml", "bash", "nginx"}, Home: home})
	out := b.String()
	for _, want := range []string{
		"✓ json-language-server → bls json",
		`✗ yaml-language-server → bls pyth: this bls has no "pyth" language`,
		"✗ bash-language-server → /nope/bls: file not found",
		`! extension "nginx" is installed but its server "nginx" isn't pointed at bls`,
		"bls extension not installed",
		"Download disabled for language server 'ruff'",
		"Zed has downloaded Node.js",
		"Prettier (Node) is allowed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
