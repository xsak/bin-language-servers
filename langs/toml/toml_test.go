package toml

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
)

const sample = `# config
title = "TOML"
num = 1_000
hex = 0xff
when = 1979-05-27T07:32:00Z
multi = """
line1
line2"""

[owner]
name = 'Tom'
dotted.key = true

[[servers]]
host = "a"
ports = [ 80,
  443, ]

[[servers]]
host = "b"
inline = { x = 1, y.z = "q" }
`

func TestParse(t *testing.T) {
	res := Parse(sample)
	if len(res.Problems) != 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	got, _ := json.Marshal(res.Root.ToValue())
	want := `{"hex":255,"multi":"line1\nline2","num":1000,"owner":{"dotted":{"key":true},"name":"Tom"},"servers":[{"host":"a","ports":[80,443]},{"host":"b","inline":{"x":1,"y":{"z":"q"}}}],"title":"TOML","when":"1979-05-27T07:32:00Z"}`
	if string(got) != want {
		t.Fatalf("value:\n%s\nwant:\n%s", got, want)
	}
	name := res.Root.Prop("owner").Value.Prop("name")
	if sample[name.KeyStart:name.KeyEnd] != "name" || sample[name.Value.Start:name.Value.End] != "'Tom'" {
		t.Fatal("ranges")
	}
	if len(res.Headers) != 3 || !res.Headers[1].Array {
		t.Fatalf("headers = %+v", res.Headers)
	}
}

func TestProblems(t *testing.T) {
	for src, want := range map[string]string{
		"a = 1\na = 2\n":       "defined more than once",
		"[t]\n[t]\n":           "defined more than once",
		"a 1\n":                "Expected '='",
		"a = \n":               "Value expected",
		"a = \"x\n":            "Unterminated string",
		"a = [1, 2\n":          "close this array",
		"a = 1 b\n":            "Unexpected text",
		"a = bogus\n":          "Invalid value",
		"x = 1\n[x]\n":         "already defined",
		"a = { b = 1\nc = 2\n": "inline table",
	} {
		found := false
		for _, p := range Parse(src).Problems {
			if strings.Contains(p.Msg, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want %q, got %+v", src, want, Parse(src).Problems)
		}
	}
}

func TestFormat(t *testing.T) {
	src := "a   =1\n\n\n\n[ tool . x ]  # c\nb= \"x  =  y\"   \n arr = [\n  1,  2,\n]\n[next]\nc=3"
	got, ok := Format(src)
	want := "a = 1\n\n[tool.x] # c\nb = \"x  =  y\"\n arr = [\n  1,  2,\n]\n\n[next]\nc = 3\n"
	if !ok || got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

const projSchema = `{
  "type": "object",
  "properties": {
    "project": {"type": "object", "additionalProperties": false, "properties": {
      "name": {"type": "string", "description": "Project name"},
      "license": {"enum": ["MIT", "Apache-2.0"]},
      "version": {"type": "string"}}},
    "tool": {"type": "object", "properties": {"ruff": {"type": "object", "properties": {
      "line-length": {"type": "integer", "default": 88}}}}}
  }
}`

func TestModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(projSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLS_OFFLINE", "1")
	t.Setenv("BLS_SCHEMA_CATALOG", schema.ResolveURL("none.json", dir))
	c := lsptest.Start(t, &Server{})
	c.Initialize(document.PathToURI(dir))
	head := "#:schema ./s.json\n"
	uri := c.Open(document.PathToURI(filepath.Join(dir, "pyproject.toml")), "toml", head+"[project]\nname = 1\nlicense = \"GPL\"\nbogus = 2\n")
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("bogus")))
	for _, want := range []string{"Incorrect type. Expected string", "Value is not accepted", "Property bogus is not allowed"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}

	labels := func(l *protocol.CompletionList) map[string]protocol.CompletionItem {
		m := map[string]protocol.CompletionItem{}
		if l != nil {
			for _, it := range l.Items {
				m[it.Label] = it
			}
		}
		return m
	}
	c.Replace(uri, head+"[project]\nname = \"x\"\nlic")
	got := labels(c.Complete(uri, 3, 3))
	if it, ok := got["license"]; !ok || it.TextEdit.NewText != `license = ${1|"MIT","Apache-2.0"|}` {
		t.Errorf("key completion = %+v", got)
	}
	if _, ok := got["name"]; ok {
		t.Error("existing key offered")
	}
	c.Replace(uri, head+"[project]\nlicense = ")
	if _, ok := labels(c.Complete(uri, 2, 10))[`"MIT"`]; !ok {
		t.Error("value completion")
	}
	c.Replace(uri, head+"[tool.")
	if _, ok := labels(c.Complete(uri, 1, 6))["ruff"]; !ok {
		t.Error("header completion")
	}
	c.Replace(uri, head+"tool.ruff.line")
	if it, ok := labels(c.Complete(uri, 1, 14))["line-length"]; !ok || it.TextEdit.NewText != "line-length = ${1:88}" {
		t.Errorf("dotted key completion = %+v", it)
	}

	c.Replace(uri, head+"[tool.ruff]\nline-length = 100\n")
	if h := c.Hover(uri, 1, 3); h == nil || !strings.Contains(h.Contents.Value, "Type: `object`") {
		t.Errorf("header hover = %+v", h)
	}
	c.Replace(uri, head+"[project]\nname = \"x\"\n")
	if h := c.Hover(uri, 2, 1); h == nil || !strings.Contains(h.Contents.Value, "Project name") {
		t.Errorf("key hover = %+v", h)
	}
}
