package compose

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

const sample = `services:
  web:
    image: nginx:1.27
    depends_on:
      - db
      - cache
    networks: [front, back]
    volumes:
      - data:/var/lib/data
      - ./conf:/etc/conf
      - type: volume
        source: logs
        target: /logs
    secrets:
      - token
    network_mode: "service:db"
  db:
    image: postgres:16
    volumes_from:
      - web:ro
networks:
  front: {}
volumes:
  data: {}
secrets:
  token:
    file: ./token
`

func setup(t *testing.T) (*lsptest.Client, protocol.DocumentURI) {
	t.Helper()
	t.Setenv("BLS_OFFLINE", "1") // no schema download in tests
	dir := t.TempDir()
	c := lsptest.Start(t, &Server{})
	c.Initialize(document.PathToURI(dir))
	return c, c.Open(document.PathToURI(filepath.Join(dir, "compose.yaml")), "dockercompose", sample)
}

func TestReferenceDiagnostics(t *testing.T) {
	c, uri := setup(t)
	diags := c.WaitDiagnostics(uri, lsptest.Contains("cache"))
	msgs := lsptest.Messages(diags)
	for _, want := range []string{`Service "cache" is not defined.`, `Network "back" is not defined`, `Volume "logs" is not defined`} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
	for _, unwanted := range []string{`"db"`, `"front"`, `"data"`, `"token"`, `"web"`, "conf"} {
		if strings.Contains(msgs, unwanted) {
			t.Errorf("false positive %s in:\n%s", unwanted, msgs)
		}
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "cache") && (d.Range.Start.Line != 5 || d.Range.Start.Character != 8 || d.Range.End.Character != 13) {
			t.Errorf("cache range = %+v", d.Range)
		}
	}
}

func TestNavigationAndCompletion(t *testing.T) {
	c, uri := setup(t)
	pos := func(l, ch uint32) protocol.TextDocumentPositionParams {
		return protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position: protocol.Position{Line: l, Character: ch}}
	}
	var locs []protocol.Location
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: pos(4, 9)}, &locs)
	if len(locs) != 1 || locs[0].Range.Start.Line != 16 {
		t.Fatalf("depends_on db -> %+v", locs)
	}
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: pos(15, 28)}, &locs)
	if len(locs) != 1 || locs[0].Range.Start.Line != 16 {
		t.Fatalf("network_mode service:db -> %+v", locs)
	}

	var refs []protocol.Location
	rp := protocol.ReferenceParams{TextDocumentPositionParams: pos(16, 3)}
	rp.Context.IncludeDeclaration = true
	c.Call("textDocument/references", rp, &refs)
	if len(refs) != 3 {
		t.Fatalf("refs to db = %+v", refs)
	}

	if h := c.Hover(uri, 4, 9); h == nil || !strings.Contains(h.Contents.Value, "postgres:16") {
		t.Fatalf("hover = %+v", h)
	}

	c.Replace(uri, "services:\n  web:\n    image: x\n  db:\n    image: y\n  worker:\n    depends_on:\n      - ")
	got := map[string]bool{}
	for _, it := range c.Complete(uri, 7, 8).Items {
		got[it.Label] = true
	}
	if !got["web"] || !got["db"] || got["worker"] {
		t.Fatalf("depends_on completion = %v", got)
	}
}

func TestSectionKeysWhileTyping(t *testing.T) {
	// The document is broken (unclosed flow), but names still come from the lines.
	c, uri := setup(t)
	c.Replace(uri, "services:\n  a:\n    image: x\n  b:\n    networks:\n      - \nnetworks:\n  n1:\n  n2: {\n")
	got := map[string]bool{}
	for _, it := range c.Complete(uri, 5, 8).Items {
		got[it.Label] = true
	}
	if !got["n1"] || !got["n2"] {
		t.Fatalf("network names = %v", got)
	}
}
