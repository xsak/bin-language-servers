package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xsak/bin-language-servers/internal/jsonast"
	"github.com/xsak/bin-language-servers/internal/tree"
)

const testSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name"],
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string", "description": "The name.", "markdownDescription": "The **name**."},
    "mode": {"enum": ["fast", "slow"], "enumDescriptions": ["Go fast", "Go slow"], "default": "fast"},
    "port": {"type": "integer", "minimum": 1},
    "tags": {"type": "array", "items": {"$ref": "#/$defs/tag"}},
    "server": {"anyOf": [
      {"type": "string"},
      {"type": "object", "properties": {"host": {"type": "string"}, "tls": {"type": "boolean"}}}
    ]},
    "pattern": {"type": "string", "pattern": "^(?!x)"}
  },
  "$defs": {"tag": {"type": "object", "properties": {"key": {"type": "string", "title": "Tag key"}}, "required": ["key"]}}
}`

func newTestStore(t *testing.T, files map[string]string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(Options{CacheDir: filepath.Join(dir, "cache"), Offline: true}), dir
}

func get(t *testing.T, s *Store, url string) *Schema {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sch, err := s.Get(ctx, url)
	if err != nil {
		t.Fatalf("Get(%s): %v", url, err)
	}
	return sch
}

func parse(t *testing.T, src string) *tree.Node {
	t.Helper()
	res := jsonast.Parse(src, jsonast.Options{})
	if res.Root == nil {
		t.Fatalf("no tree for %s", src)
	}
	return res.Root
}

func TestValidateRanges(t *testing.T) {
	store, dir := newTestStore(t, map[string]string{"s.json": testSchema})
	sch := get(t, store, ResolveURL("s.json", dir))

	src := `{"mode": "medium", "port": 0, "extra": 1, "tags": [{"nokey": 1}], "server": 5}`
	probs := sch.Validate(parse(t, src))
	want := []struct{ at, msg string }{
		{`{"mode"`, `Missing property "name"`},
		{`"medium"`, "Value is not accepted"},
		{`0,`, "Minimum: got 0, want 1"},
		{`"extra"`, "Property extra is not allowed"},
		{`{"nokey"`, `Missing property "key"`},
		{`5}`, "Incorrect type. Expected one of string, object"},
	}
	if len(probs) != len(want) {
		t.Fatalf("got %d problems, want %d: %+v", len(probs), len(want), probs)
	}
	for i, w := range want {
		p := probs[i]
		if !strings.HasPrefix(src[p.Start:], w.at) || !strings.Contains(p.Message, w.msg) {
			t.Errorf("problem %d = %q at %q, want %q at %q", i, p.Message, src[p.Start:p.End], w.msg, w.at)
		}
	}
}

func TestFalseSchemaMessage(t *testing.T) {
	store, dir := newTestStore(t, map[string]string{"f.json": `{"type": "object",
		"patternProperties": {"^x-": {}}, "properties": {"a": {}}, "additionalProperties": false,
		"$defs": {}, "items": false}`})
	sch := get(t, store, ResolveURL("f.json", dir))
	probs := sch.Validate(parse(t, `{"a": 1, "x-ok": 2, "b": 3}`))
	if len(probs) != 1 || probs[0].Message != "Property b is not allowed." {
		t.Fatalf("problems = %+v", probs)
	}
	nested, dir2 := newTestStore(t, map[string]string{"n.json": `{"properties": {"s": {"properties": {"p": false}}}}`})
	probs = get(t, nested, ResolveURL("n.json", dir2)).Validate(parse(t, `{"s": {"p": 1}}`))
	if len(probs) != 1 || probs[0].Message != "Property p is not allowed." {
		t.Fatalf("false subschema = %+v", probs)
	}
}

func TestLenientRegexpDoesNotBreakSchema(t *testing.T) {
	store, dir := newTestStore(t, map[string]string{"s.json": testSchema})
	sch := get(t, store, ResolveURL("s.json", dir))
	if probs := sch.Validate(parse(t, `{"name": "n", "pattern": "x"}`)); len(probs) != 0 {
		t.Fatalf("lookahead pattern should match anything: %+v", probs)
	}
}

func TestNavigation(t *testing.T) {
	store, dir := newTestStore(t, map[string]string{"s.json": testSchema})
	sch := get(t, store, ResolveURL("s.json", dir))

	props := sch.Properties(nil, nil)
	names := map[string]PropertyInfo{}
	for _, p := range props {
		names[p.Name] = p
	}
	if !names["name"].Required || names["name"].Description != "The **name**." {
		t.Fatalf("name info = %+v", names["name"])
	}
	if len(names["mode"].Enum) != 2 {
		t.Fatalf("mode enum = %+v", names["mode"])
	}

	// Array items through $ref.
	itemProps := sch.Properties(tree.Path{tree.KeySeg("tags"), tree.IndexSeg(-1)}, nil)
	if len(itemProps) != 1 || itemProps[0].Name != "key" || !itemProps[0].Required {
		t.Fatalf("tag props = %+v", itemProps)
	}

	// anyOf: object branch chosen from the current value.
	value := map[string]any{"server": map[string]any{}}
	srv := sch.Properties(tree.Path{tree.KeySeg("server")}, value)
	if len(srv) != 2 {
		t.Fatalf("server props = %+v", srv)
	}

	d := sch.Describe(sch.At(tree.Path{tree.KeySeg("mode")}, nil))
	vals := d.Values()
	if len(vals) != 2 || d.EnumDocs[0] != "Go fast" {
		t.Fatalf("mode values %v docs %v", vals, d.EnumDocs)
	}
	if md := d.Markdown(); !strings.Contains(md, "`\"fast\"`: Go fast") || !strings.Contains(md, "Default") {
		t.Fatalf("markdown = %s", md)
	}
}

func TestGlobs(t *testing.T) {
	tests := []struct {
		globs []string
		path  string
		want  bool
	}{
		{[]string{"package.json"}, "/a/b/package.json", true},
		{[]string{"package.json"}, "/a/b/xpackage.json", false},
		{[]string{"*.schema.json"}, "/x/y.schema.json", true},
		{[]string{".github/workflows/*.yml"}, "/repo/.github/workflows/ci.yml", true},
		{[]string{".github/workflows/*.yml"}, "/repo/.github/ci.yml", false},
		{[]string{"**/k8s/**/*.yaml"}, "/r/k8s/apps/x.yaml", true},
		{[]string{"**/k8s/**/*.yaml"}, "/r/k8s/x.yaml", true},
		{[]string{"docker-compose.{yml,yaml}"}, "/r/docker-compose.yaml", true},
		{[]string{"*.json", "!tsconfig.json"}, "/r/tsconfig.json", false},
		{[]string{"/abs/only.json"}, "/abs/only.json", true},
		{[]string{"/abs/only.json"}, "/other/abs/only.json", false},
	}
	for _, tt := range tests {
		if got := NewGlobSet(tt.globs).Match(tt.path); got != tt.want {
			t.Errorf("%v.Match(%s) = %v, want %v", tt.globs, tt.path, got, tt.want)
		}
	}
}

func TestWorkspaceGlobsWindowsRoot(t *testing.T) {
	// A Windows workspace root (slash form, as produced by filepath.ToSlash on Windows) must
	// anchor the same as a Unix one.
	gs := workspaceGlobsSlash([]string{"/k8s/*.yaml"}, "C:/ws")
	if !gs.Match("C:/ws/k8s/app.yaml") {
		t.Error("anchored glob should match a file in the same workspace")
	}
	if gs.Match("C:/other/k8s/app.yaml") {
		t.Error("anchored glob must not match outside the workspace")
	}
}

func TestSettingsShapes(t *testing.T) {
	store := NewStore(Options{Offline: true})
	js, err := ParseSettings(json.RawMessage(`{"json": {"validate": {"enable": false},
		"schemas": [{"fileMatch": ["/conf/*.json"], "url": "./s.json"},
		            {"fileMatch": ["inline.json"], "schema": {"type": "object"}}]}}`), "json", "/ws", store)
	if err != nil {
		t.Fatal(err)
	}
	if js.Validate || len(js.Associations) != 2 || js.Associations[0].URL != "file:///ws/s.json" {
		t.Fatalf("json settings = %+v", js)
	}
	if !js.Associations[0].Globs.Match("/ws/conf/a.json") || js.Associations[0].Globs.Match("/elsewhere/conf/a.json") {
		t.Fatal("workspace-anchored glob wrong")
	}
	if _, err := store.Get(context.Background(), js.Associations[1].URL); err != nil {
		t.Fatalf("inline schema: %v", err)
	}

	ys, err := ParseSettings(json.RawMessage(`{"yaml": {"schemaStore": {"enable": false},
		"schemas": {"https://x/y.json": ["*.y.yaml"], "kubernetes": "k8s/*.yaml"},
		"kubernetes": {"version": "1.33.0"}}}`), "yaml", "/ws", store)
	if err != nil {
		t.Fatal(err)
	}
	if ys.SchemaStore || len(ys.Associations) != 1 || ys.Kubernetes.Globs == nil ||
		!strings.HasSuffix(ys.Kubernetes.SchemaBase, "/v1.33.0-standalone-strict") {
		t.Fatalf("yaml settings = %+v", ys)
	}
}

func TestHTTPFetchCacheAndCatalog(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/catalog.json":
			_, _ = w.Write([]byte(`{"schemas": [
				{"name": "app", "fileMatch": ["app.json"], "url": "` + "http://" + r.Host + `/app.json"},
				{"name": "y", "fileMatch": ["app.yaml"], "url": "http://` + r.Host + `/y.json"}]}`))
		case "/app.json":
			_, _ = w.Write([]byte(`{"type": "object"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	var loaded atomic.Int32
	store := NewStore(Options{CacheDir: cache, OnLoaded: func() { loaded.Add(1) }})
	cat := NewCatalog(store, srv.URL+"/catalog.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := cat.Match(ctx, "/w/app.json", ForJSON)
	if err != nil || e.Name != "app" {
		t.Fatalf("catalog match = %+v, %v", e, err)
	}
	if e, _ := cat.Match(ctx, "/w/app.yaml", ForJSON); e.Name != "" {
		t.Fatalf("YAML entry offered for JSON: %+v", e)
	}
	if _, err := store.Get(ctx, e.URL); err != nil && e.URL != "" {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, srv.URL+"/app.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, srv.URL+"/missing.json"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}

	// A new store with the same cache dir must not hit the network for cached documents.
	before := hits.Load()
	store2 := NewStore(Options{CacheDir: cache})
	if _, err := store2.Get(ctx, srv.URL+"/app.json"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Fatal("cached schema was downloaded again")
	}
	if loaded.Load() == 0 {
		t.Fatal("OnLoaded never called")
	}
}

func TestRemoteSchemaCannotReadLocalFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"$ref": "file://%s"}`, filepath.ToSlash(secret))
	}))
	defer srv.Close()
	store := NewStore(Options{})
	if _, err := store.Get(context.Background(), srv.URL+"/schema.json"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("remote $ref to a local file was followed: %v", err)
	}
	// A local root schema may still reference local files.
	local := filepath.Join(dir, "root.json")
	if err := os.WriteFile(local, []byte(`{"$ref": "secret.json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "file://"+filepath.ToSlash(local)); err != nil {
		t.Fatalf("local $ref: %v", err)
	}
}
