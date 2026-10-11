package json_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	jsonlang "github.com/xsak/bin-language-servers/langs/json"
)

const appSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string", "description": "Application name"},
    "mode": {"enum": ["fast", "slow"]},
    "nested": {"type": "object", "properties": {"deep": {"type": "boolean"}}}
  }
}`

func setup(t *testing.T) (*lsptest.Client, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.schema.json", appSchema)
	write("catalog.json", `{"schemas": [{"name": "app", "fileMatch": ["app.json"], "url": "`+
		schema.ResolveURL("app.schema.json", dir)+`"}]}`)
	t.Setenv("BLS_OFFLINE", "1")
	t.Setenv("BLS_SCHEMA_CATALOG", schema.ResolveURL("catalog.json", dir))

	c := lsptest.Start(t, &jsonlang.Server{})
	c.Initialize(document.PathToURI(dir))
	return c, dir
}

func TestDiagnosticsFromCatalogAndSchemaKey(t *testing.T) {
	c, dir := setup(t)
	uri := c.Open(document.PathToURI(filepath.Join(dir, "app.json")), "json", `{"name": 1, "mode": "x", "other": true}`)

	// The catalog and schema load in the background; waitFor re-sends until they are used.
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("not allowed")))
	for _, want := range []string{"Incorrect type. Expected string", "Value is not accepted", "Property other is not allowed"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}

	other := c.Open(document.PathToURI(filepath.Join(dir, "x.json")), "json",
		`{"$schema": "./app.schema.json", "mode": "slow", "bad": 1}`)
	if msgs := lsptest.Messages(c.WaitDiagnostics(other, lsptest.Contains("bad"))); !strings.Contains(msgs, "Property bad is not allowed") {
		t.Fatalf("$schema not used: %s", msgs)
	}
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

func TestCompletionHoverFormatSymbols(t *testing.T) {
	c, dir := setup(t)
	path := filepath.Join(dir, "app.json")
	uri := c.Open(document.PathToURI(path), "json", "{\n  \"mode\": \"fast\",\n  \n}")

	got := labels(c.Complete(uri, 2, 2))
	if _, ok := got["mode"]; ok {
		t.Error("existing key offered again")
	}
	name, ok := got["name"]
	if !ok || name.InsertText != `"name": "$1"` || name.InsertTextFormat != protocol.InsertTextSnippet {
		t.Fatalf("name item = %+v (all: %v)", name, got)
	}
	if nested := got["nested"]; nested.InsertText != `"nested": {$1}` {
		t.Errorf("nested snippet = %q", nested.InsertText)
	}

	// Value completion replaces the current value.
	vals := labels(c.Complete(uri, 1, 12))
	if _, ok := vals[`"slow"`]; !ok || vals[`"slow"`].TextEdit == nil {
		t.Fatalf("values = %v", vals)
	}

	// Key completion inside a partially typed key.
	c.Replace(uri, "{\"nested\": {\"de\": }}")
	deep := labels(c.Complete(uri, 0, 15))
	if it, ok := deep["deep"]; !ok || it.TextEdit == nil || it.TextEdit.NewText != `"deep"` {
		t.Fatalf("nested key completion = %+v", deep)
	}

	h := c.Hover(uri, 0, 3)
	if h == nil || !strings.Contains(h.Contents.Value, "Type: `object`") {
		t.Fatalf("hover = %+v", h)
	}

	c.Replace(uri, `{"name":"a","nested":{"deep":true}}`)
	var edits []protocol.TextEdit
	c.Call("textDocument/formatting", protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Options:      protocol.FormattingOptions{TabSize: 2, InsertSpaces: true},
	}, &edits)
	if len(edits) != 1 || edits[0].NewText != "{\n  \"name\": \"a\",\n  \"nested\": {\n    \"deep\": true\n  }\n}" {
		t.Fatalf("format = %+v", edits)
	}

	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 2 || syms[1].Name != "nested" || len(syms[1].Children) != 1 {
		t.Fatalf("symbols = %+v", syms)
	}
}
