package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func TestDetectDialect(t *testing.T) {
	tests := []struct {
		text, path string
		want       syntax.LangVariant
		sc         string
	}{
		{"#!/bin/sh\n", "x", syntax.LangPOSIX, "sh"},
		{"#!/usr/bin/env bash\n", "x", syntax.LangBash, "bash"},
		{"#!/usr/bin/env -S zsh -f\n", "x", syntax.LangZsh, ""},
		{"echo\n", "/t/x.bats", syntax.LangBats, "bash"},
		{"echo\n", "/t/x.sh", syntax.LangBash, "bash"},
		{"#!/bin/mksh\n", "x", syntax.LangMirBSDKorn, "ksh"},
	}
	for _, tt := range tests {
		d := detectDialect(tt.text, tt.path, "")
		if d.variant != tt.want || d.shellcheck != tt.sc {
			t.Errorf("%q %s = %v %q", tt.text, tt.path, d.variant, d.shellcheck)
		}
	}
	if d := detectDialect("#!/bin/bash\n", "x", "posix"); d.variant != syntax.LangPOSIX {
		t.Errorf("override ignored: %v", d.variant)
	}
}

func TestAnalyze(t *testing.T) {
	src := "#!/bin/bash\nsource ./lib.sh\nNAME=x\n# greet says hi\ngreet() {\n  local who=$1\n  echo \"hi ${who} $NAME\"\n}\nfor f in a b; do greet \"$f\"; done\nread -r line\n"
	a := analyze(src, dialect{syntax.LangBash, "bash"})
	if len(a.problems) != 0 {
		t.Fatalf("problems: %+v", a.problems)
	}
	for _, v := range []string{"NAME", "who", "f", "line"} {
		if len(a.varDef[v]) == 0 {
			t.Errorf("variable %s not found", v)
		}
	}
	if len(a.varRef["who"]) != 1 || len(a.varRef["NAME"]) != 1 {
		t.Errorf("refs = %v", a.varRef)
	}
	if len(a.funcDef["greet"]) != 1 || len(a.calls["greet"]) != 1 {
		t.Errorf("greet def %v calls %v", a.funcDef["greet"], a.calls["greet"])
	}
	if len(a.sources) != 1 || a.sources[0].name != "./lib.sh" {
		t.Errorf("sources = %v", a.sources)
	}
	if c := commentBefore(src, a.funcDef["greet"][0].start); c != "greet says hi" {
		t.Errorf("comment = %q", c)
	}
}

func TestParseErrorRecovers(t *testing.T) {
	a := analyze("f() {\n  echo hi\n}\nif true; then\n  echo x\n", dialect{syntax.LangBash, "bash"})
	if len(a.problems) != 1 {
		t.Fatalf("problems = %+v", a.problems)
	}
	if len(a.funcDef["f"]) != 1 {
		t.Fatal("symbols lost after a parse error")
	}
}

// fakeShellcheck writes a script that prints canned json1 output.
func fakeShellcheck(t *testing.T, dir string) string {
	t.Helper()
	out := `{"comments":[{"file":"-","line":2,"endLine":2,"column":6,"endColumn":8,"level":"info","code":2086,` +
		`"message":"Double quote to prevent globbing and word splitting.","fix":{"replacements":[` +
		`{"line":2,"endLine":2,"column":6,"endColumn":6,"replacement":"\""},` +
		`{"line":2,"endLine":2,"column":8,"endColumn":8,"replacement":"\""}]}}]}`
	path := filepath.Join(dir, "shellcheck")
	script := "#!/bin/sh\ncat >/dev/null\necho '" + out + "'\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setup(t *testing.T) (*lsptest.Client, string) {
	t.Helper()
	dir := t.TempDir()
	c := lsptest.Start(t, &Server{})
	c.Initialize(document.PathToURI(dir))
	c.Notify("workspace/didChangeConfiguration", protocol.DidChangeConfigurationParams{
		Settings: []byte(`{"bashIde": {"shellcheckPath": "` + fakeShellcheck(t, dir) + `"}}`)})
	return c, dir
}

func TestShellcheckDiagnosticsAndFixes(t *testing.T) {
	c, dir := setup(t)
	uri := c.Open(document.PathToURI(filepath.Join(dir, "a.sh")), "shellscript", "#!/bin/bash\necho $x\n")
	diags := c.WaitDiagnostics(uri, lsptest.Contains("Double quote"))
	if len(diags) != 1 || diags[0].Code != "SC2086" || diags[0].Range.Start.Character != 5 || diags[0].Range.End.Character != 7 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	var actions []protocol.CodeAction
	c.Call("textDocument/codeAction", protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: diags[0].Range,
	}, &actions)
	if len(actions) != 2 {
		t.Fatalf("actions = %+v", actions)
	}
	fix := actions[0].Edit.Changes[uri]
	if len(fix) != 2 || fix[0].NewText != `"` || fix[1].Range.Start.Character != 7 {
		t.Fatalf("fix edits = %+v", fix)
	}
	disable := actions[1].Edit.Changes[uri]
	if disable[0].NewText != "# shellcheck disable=SC2086\n" || disable[0].Range.Start.Line != 1 {
		t.Fatalf("disable edit = %+v", disable)
	}

	// A parse error replaces shellcheck output.
	c.Replace(uri, "if true; then\n")
	if d := c.WaitDiagnostics(uri, func(d []protocol.Diagnostic) bool {
		return len(d) == 1 && d[0].Source == "bls"
	}); d[0].Severity != protocol.SeverityError {
		t.Fatalf("parse error = %+v", d)
	}
}

func TestFormat(t *testing.T) {
	c, dir := setup(t)
	uri := c.Open(document.PathToURI(filepath.Join(dir, "f.sh")), "shellscript", "f(){\necho   hi|grep h\n}\n")
	format := func(spaces bool) string {
		var edits []protocol.TextEdit
		c.Call("textDocument/formatting", protocol.DocumentFormattingParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Options:      protocol.FormattingOptions{TabSize: 4, InsertSpaces: spaces},
		}, &edits)
		if len(edits) != 1 {
			t.Fatalf("edits = %+v", edits)
		}
		return edits[0].NewText
	}
	if got := format(false); got != "f() {\n\techo hi | grep h\n}\n" {
		t.Errorf("tabs: %q", got)
	}
	if got := format(true); got != "f() {\n    echo hi | grep h\n}\n" {
		t.Errorf("spaces: %q", got)
	}

	// .editorconfig wins over the editor's options, like shfmt.
	if err := os.WriteFile(filepath.Join(dir, ".editorconfig"), []byte("[*.sh]\nindent_style = space\nindent_size = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := format(false); got != "f() {\n  echo hi | grep h\n}\n" {
		t.Errorf("editorconfig: %q", got)
	}
}

func TestNavigationAcrossSource(t *testing.T) {
	c, dir := setup(t)
	lib := "# Logs a message.\nlog_msg() {\n  echo \"$*\"\n}\nLIB_VAR=1\n"
	if err := os.WriteFile(filepath.Join(dir, "lib.sh"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "#!/bin/bash\nsource ./lib.sh\nlocal_fn() { :; }\nlog_msg hi\nlocal_fn\nlocal_fn\necho \"$LIB_VAR\"\n"
	uri := c.Open(document.PathToURI(filepath.Join(dir, "main.sh")), "shellscript", src)

	var locs []protocol.Location
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: pos(uri, 3, 2)}, &locs)
	if len(locs) != 1 || !strings.HasSuffix(string(locs[0].URI), "/lib.sh") || locs[0].Range.Start.Line != 1 {
		t.Fatalf("definition = %+v", locs)
	}
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: pos(uri, 1, 10)}, &locs)
	if len(locs) != 1 || !strings.HasSuffix(string(locs[0].URI), "/lib.sh") {
		t.Fatalf("source definition = %+v", locs)
	}

	var refs []protocol.Location
	params := protocol.ReferenceParams{TextDocumentPositionParams: pos(uri, 4, 1)}
	params.Context.IncludeDeclaration = true
	c.Call("textDocument/references", params, &refs)
	if len(refs) != 3 {
		t.Fatalf("references = %+v", refs)
	}

	h := c.Hover(uri, 3, 2)
	if h == nil || !strings.Contains(h.Contents.Value, "Logs a message.") || !strings.Contains(h.Contents.Value, "lib.sh") {
		t.Fatalf("hover = %+v", h)
	}

	got := map[string]bool{}
	for _, it := range c.Complete(uri, 6, 8).Items {
		got[it.Label] = true
	}
	if !got["LIB_VAR"] {
		t.Errorf("variable completion missing LIB_VAR: %v", got)
	}
	c.Replace(uri, src+"lo")
	got = map[string]bool{}
	for _, it := range c.Complete(uri, 7, 2).Items {
		got[it.Label] = true
	}
	if !got["log_msg"] || !got["local_fn"] || !got["local"] {
		t.Errorf("command completion = %v", got)
	}

	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 1 || syms[0].Name != "local_fn" {
		t.Fatalf("symbols = %+v", syms)
	}
}

func pos(uri protocol.DocumentURI, line, char uint32) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position: protocol.Position{Line: line, Character: char}}
}
