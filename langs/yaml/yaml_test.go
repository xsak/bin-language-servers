package yaml

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/tree"
)

const appSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string", "description": "Application name"},
    "mode": {"enum": ["fast", "slow", "yes"]},
    "server": {"anyOf": [{"type": "string"},
      {"type": "object", "properties": {"host": {"type": "string"}, "tls": {"type": "boolean"}}}]},
    "tags": {"type": "array", "items": {"type": "object", "properties": {"key": {"type": "string"}, "value": {"type": "string"}}}},
    "list": {"type": "array", "items": {"enum": ["a", "b"]}}
  }
}`

const deploymentSchema = `{
  "type": "object",
  "properties": {
    "apiVersion": {"type": "string"}, "kind": {"type": "string"},
    "spec": {"type": "object", "additionalProperties": false,
      "properties": {"replicas": {"type": "integer", "description": "Number of pods."}}}
  }
}`

func setup(t *testing.T) (*lsptest.Client, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.schema.json"), []byte(appSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLS_OFFLINE", "")
	t.Setenv("BLS_SCHEMA_CATALOG", schema.ResolveURL("no-catalog.json", dir))
	c := lsptest.Start(t, &Server{})
	c.Initialize(document.PathToURI(dir))
	return c, dir
}

func labels(l *protocol.CompletionList) map[string]protocol.CompletionItem {
	out := map[string]protocol.CompletionItem{}
	if l != nil {
		for _, it := range l.Items {
			out[it.Label] = it
		}
	}
	return out
}

const modeline = "# yaml-language-server: $schema=./app.schema.json\n"

func TestModelineDiagnostics(t *testing.T) {
	c, dir := setup(t)
	src := modeline + "name: 1\nmode: medium\nextra: x\nserver:\n  port: 1\n"
	uri := c.Open(document.PathToURI(filepath.Join(dir, "a.yaml")), "yaml", src)
	diags := c.WaitDiagnostics(uri, lsptest.Contains("extra"))
	msgs := lsptest.Messages(diags)
	for _, want := range []string{"Incorrect type. Expected string", "Value is not accepted", "Property extra is not allowed"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "extra") && (d.Range.Start.Line != 3 || d.Range.Start.Character != 0 || d.Range.End.Character != 5) {
			t.Errorf("extra range = %+v", d.Range)
		}
	}

	c.Replace(uri, "a: 1\n  b: 2\n")
	if d := c.WaitDiagnostics(uri, func(d []protocol.Diagnostic) bool { return len(d) == 1 }); d[0].Severity != protocol.SeverityError {
		t.Fatalf("syntax error = %+v", d)
	}
}

func TestCompletion(t *testing.T) {
	c, dir := setup(t)
	uri := document.PathToURI(filepath.Join(dir, "a.yaml"))

	tests := []struct {
		name      string
		text      string
		line, col uint32
		want      []string
		notWant   []string
		insert    map[string]string // label -> NewText
	}{
		{name: "root keys", text: modeline + "name: x\n", line: 2, col: 0,
			want: []string{"mode", "server", "tags"}, notWant: []string{"name"},
			insert: map[string]string{"mode": "mode: ${1|fast,slow,\"yes\"|}", "tags": "tags:\n  - $0"}},
		{name: "partial key", text: modeline + "na", line: 1, col: 2,
			want: []string{"name"}, insert: map[string]string{"name": "name: $0"}},
		{name: "nested anyOf object", text: modeline + "server:\n  ", line: 2, col: 2,
			want: []string{"host", "tls"}},
		{name: "sequence item keys", text: modeline + "tags:\n  - key: a\n    ", line: 3, col: 4,
			want: []string{"value"}, notWant: []string{"key"}},
		{name: "new sequence item", text: modeline + "tags:\n- ", line: 2, col: 2,
			want: []string{"key", "value"}},
		{name: "enum value", text: modeline + "mode: ", line: 1, col: 6,
			want: []string{"fast", "slow", `"yes"`}},
		{name: "scalar item values", text: modeline + "list:\n  - ", line: 2, col: 4,
			want: []string{"a", "b"}},
		{name: "existing key with colon", text: modeline + "mo: fast\n", line: 1, col: 2,
			want: []string{"mode"}, insert: map[string]string{"mode": "mode"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if i == 0 {
				c.Open(uri, "yaml", tt.text)
			} else {
				c.Replace(uri, tt.text)
			}
			got := labels(c.Complete(uri, tt.line, tt.col))
			for _, w := range tt.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing %q in %v", w, keys(got))
				}
			}
			for _, w := range tt.notWant {
				if _, ok := got[w]; ok {
					t.Errorf("unexpected %q", w)
				}
			}
			for label, text := range tt.insert {
				if it := got[label]; it.TextEdit == nil || it.TextEdit.NewText != text {
					t.Errorf("%s inserts %+v, want %q", label, it.TextEdit, text)
				}
			}
		})
	}
}

func keys(m map[string]protocol.CompletionItem) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestKubernetes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/deployment-apps-v1.json" {
			_, _ = w.Write([]byte(deploymentSchema))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c, dir := setup(t)
	c.Notify("workspace/didChangeConfiguration", protocol.DidChangeConfigurationParams{
		Settings: []byte(`{"yaml": {"kubernetes": {"schemaBase": "` + srv.URL + `"}}}`)})

	src := "apiVersion: apps/v1\nkind: Deployment\nspec:\n  replicas: many\n  foo: 1\n---\napiVersion: example.com/v1\nkind: Widget\nspec: {}\n"
	uri := c.Open(document.PathToURI(filepath.Join(dir, "deploy.yaml")), "yaml", src)
	diags := c.WaitDiagnostics(uri, lsptest.Contains("foo"))
	msgs := lsptest.Messages(diags)
	if !strings.Contains(msgs, "Incorrect type. Expected integer") || !strings.Contains(msgs, "Property foo is not allowed") {
		t.Fatalf("diagnostics:\n%s", msgs)
	}
	for _, d := range diags {
		if d.Range.Start.Line > 5 {
			t.Errorf("CRD document got a diagnostic: %+v", d)
		}
	}

	h := c.Hover(uri, 3, 4)
	if h == nil || !strings.Contains(h.Contents.Value, "Number of pods.") {
		t.Fatalf("hover = %+v", h)
	}

	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 2 || syms[0].Name != "Deployment" || syms[1].Name != "Widget" {
		t.Fatalf("symbols = %+v", syms)
	}
}

func TestKubernetesURL(t *testing.T) {
	k := schema.DefaultSettings().Kubernetes
	root := func(apiVersion, kind string) *tree.Node {
		return &tree.Node{Kind: tree.Object, Props: []*tree.Prop{
			{Key: "apiVersion", Value: &tree.Node{Kind: tree.String, Value: apiVersion}},
			{Key: "kind", Value: &tree.Node{Kind: tree.String, Value: kind}},
		}}
	}
	tests := map[string]string{
		"v1/Service":                   "/service-v1.json",
		"apps/v1/Deployment":           "/deployment-apps-v1.json",
		"networking.k8s.io/v1/Ingress": "/ingress-networking-v1.json",
		"batch/v1/CronJob":             "/cronjob-batch-v1.json",
	}
	for in, want := range tests {
		i := strings.LastIndex(in, "/")
		if got := kubernetesURL(k, "/x.yaml", root(in[:i], in[i+1:])); got != k.SchemaBase+want {
			t.Errorf("%s -> %s, want %s", in, got, k.SchemaBase+want)
		}
	}
	k.AutoDetect = false
	if got := kubernetesURL(k, "/x.yaml", root("v1", "Pod")); got != "" {
		t.Errorf("autodetect off but got %s", got)
	}
}

func TestParentPath(t *testing.T) {
	lines := strings.Split("a:\n  b:\n    - c: 1\n      d:\n        \nitems:\n- x: 1\n  ", "\n")
	if got := parentPath(lines, 4, 8, nil).Pointer(); got != "/a/b/-1/d" {
		t.Errorf("deep path = %s", got)
	}
	if got := parentPath(lines, 7, 2, nil).Pointer(); got != "/items/-1" {
		t.Errorf("same-indent sequence path = %s", got)
	}
}
