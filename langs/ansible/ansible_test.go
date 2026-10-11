package ansible

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/tree"
)

const copyPy = `#!/usr/bin/python
# -*- coding: utf-8 -*-

DOCUMENTATION = r"""
---
module: copy
short_description: Copy files to remote locations
description:
  - The copy module copies a file to a location on the remote machine.
options:
  src:
    description: Local path to a file to copy.
    type: path
  dest:
    description: Remote absolute path.
    type: path
    required: true
  backup:
    description: Create a backup file.
    type: bool
    default: no
  state:
    description: Desired state.
    choices: [present, absent]
"""

EXAMPLES = r"""
"""
`

const ufwPy = "DOCUMENTATION = '''\nmodule: ufw\nshort_description: Manage firewall with UFW\noptions:\n  rule:\n    type: str\n    choices: [allow, deny]\n'''\n"

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeInstall creates an ansible-core venv layout and a collection; returns the ansible path.
func fakeInstall(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("ANSIBLE_COLLECTIONS_PATH", filepath.Join(base, "colls"))
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("BLS_OFFLINE", "1")
	write(t, filepath.Join(base, "venv/bin/ansible"), "#!/bin/sh\n")
	write(t, filepath.Join(base, "venv/lib/python3.12/site-packages/ansible/modules/copy.py"), copyPy)
	write(t, filepath.Join(base, "venv/lib/python3.12/site-packages/ansible/modules/__init__.py"), "")
	write(t, filepath.Join(base, "colls/ansible_collections/community/general/plugins/modules/ufw.py"), ufwPy)
	return filepath.Join(base, "venv/bin/ansible")
}

func TestIndex(t *testing.T) {
	exe := fakeInstall(t)
	core := coreModuleDirs(exe)
	ix := NewIndex(core, collectionPaths(core))
	if got := ix.Names(); len(got) != 2 || got[0] != "ansible.builtin.copy" || got[1] != "community.general.ufw" {
		t.Fatalf("names = %v", got)
	}
	if ix.Resolve("copy") != "ansible.builtin.copy" || ix.Resolve("ansible.legacy.copy") != "ansible.builtin.copy" || ix.Resolve("ufw") != "community.general.ufw" {
		t.Fatal("resolve failed")
	}
	d := ix.Doc("ansible.builtin.copy")
	if d == nil || d.ShortDescription != "Copy files to remote locations" || !d.Options["dest"].Required() || len(d.Options["state"].Choices) != 2 {
		t.Fatalf("doc = %+v", d)
	}
	if !strings.Contains(d.Markdown(), "`dest` (path, required)") {
		t.Errorf("markdown = %s", d.Markdown())
	}
	if ix.Doc("community.general.ufw").Options["rule"].Type != "str" {
		t.Error("ufw doc")
	}
}

func TestMarkup(t *testing.T) {
	got := markup("Use O(dest) with C(mode=0644), see M(ansible.builtin.file) and L(docs,https://x.y). I(it) B(b) HORIZONTALLINE")
	want := "Use `dest` with `mode=0644`, see `ansible.builtin.file` and [docs](https://x.y). *it* **b** ---"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if !truthy("yes") || truthy("no") || !truthy(true) {
		t.Fatal("truthy")
	}
}

func TestFileKind(t *testing.T) {
	tasks := &tree.Node{Kind: tree.Array, Items: []*tree.Node{{Kind: tree.Object}}}
	play := &tree.Node{Kind: tree.Array, Items: []*tree.Node{{Kind: tree.Object, Props: []*tree.Prop{{Key: "hosts"}}}}}
	tests := []struct {
		path string
		root *tree.Node
		want string
	}{
		{"/r/roles/web/tasks/main.yml", nil, "ansible.json#/$defs/tasks"},
		{"/r/roles/web/handlers/main.yml", nil, "ansible.json#/$defs/tasks"},
		{"/r/roles/web/defaults/main.yml", nil, "vars.json"},
		{"/r/group_vars/all.yml", nil, "vars.json"},
		{"/r/roles/web/meta/main.yml", nil, "meta.json"},
		{"/r/requirements.yml", nil, "requirements.json"},
		{"/r/site.yml", play, "ansible.json#/$defs/playbook"},
		{"/r/deploy.yml", tasks, "ansible.json#/$defs/tasks"},
		{"/r/inventory.yml", nil, "inventory.json"},
		{"/r/random.yml", &tree.Node{Kind: tree.Object}, ""},
	}
	for _, tt := range tests {
		if got := fileKind(tt.path, tt.root); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestParseCodeclimate(t *testing.T) {
	doc := document.New("file:///p.yml", "ansible", 1, "- hosts: all\n  tasks:\n    - copy:\n", "")
	diags := parseCodeclimate(doc, []byte(`[{"check_name": "fqcn[action-core]", "url": "https://x/fqcn/",
		"severity": "major", "description": "Use FQCN for builtin module actions (copy).",
		"location": {"path": "p.yml", "positions": {"begin": {"line": 3, "column": 7}}}}]`))
	if len(diags) != 1 || diags[0].Code != "fqcn[action-core]" || diags[0].Range.Start.Character != 6 ||
		diags[0].CodeDescription.Href != "https://x/fqcn/" {
		t.Fatalf("diags = %+v", diags)
	}
}

func setup(t *testing.T) (*lsptest.Client, string) {
	t.Helper()
	exe := fakeInstall(t)
	ws := t.TempDir()
	c := lsptest.Start(t, &Server{})
	c.Initialize(document.PathToURI(ws))
	c.Notify("workspace/didChangeConfiguration", protocol.DidChangeConfigurationParams{
		Settings: []byte(`{"ansible": {"ansible": {"path": "` + exe + `"}, "validation": {"lint": {"enabled": false}}}}`)})
	return c, ws
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

func TestCompletionHoverDiagnostics(t *testing.T) {
	c, ws := setup(t)
	path := filepath.Join(ws, "site.yml")
	src := "- hosts: all\n  tasks:\n    - name: copy it\n      ansible.builtin.copy:\n        dest: /tmp/x\n    - name: bad\n      ansible.builtin.cpy:\n        x: 1\n    - name: other collection\n      foo.bar.baz: {}\n    - copy:\n        src: a\n        state: gone\n        bogus: 1\n    - community.general.ufw:\n        rule: allw\n"
	uri := c.Open(document.PathToURI(path), "ansible", src)

	diags := c.WaitDiagnostics(uri, lsptest.Contains("cpy"))
	msgs := lsptest.Messages(diags)
	if !strings.Contains(msgs, `Couldn't resolve module/action "ansible.builtin.cpy"`) || strings.Contains(msgs, "foo.bar.baz") {
		t.Fatalf("diagnostics:\n%s", msgs)
	}
	for _, want := range []string{`Value "gone" is not one of: present, absent.`, `Unsupported parameter "bogus"`,
		"Missing required parameter(s) of ansible.builtin.copy: dest.", `Value "allw" is not one of: allow, deny.`} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}

	h := c.Hover(uri, 3, 10)
	if h == nil || !strings.Contains(h.Contents.Value, "Copy files to remote locations") {
		t.Fatalf("module hover = %+v", h)
	}
	if h := c.Hover(uri, 4, 9); h == nil || !strings.Contains(h.Contents.Value, "Remote absolute path") {
		t.Fatalf("option hover = %+v", h)
	}

	// Module names at a task key position.
	c.Replace(uri, "- hosts: all\n  tasks:\n    - name: x\n      ")
	got := labels(c.Complete(uri, 3, 6))
	if it, ok := got["ansible.builtin.copy"]; !ok || it.Detail != "Copy files to remote locations" {
		t.Fatalf("module completion = %v", got)
	}
	if _, ok := got["community.general.ufw"]; !ok {
		t.Fatal("collection module missing")
	}
	// Options of the module.
	c.Replace(uri, "- hosts: all\n  tasks:\n    - ansible.builtin.copy:\n        ")
	got = labels(c.Complete(uri, 3, 8))
	if !(got["dest"].Label == "dest" && got["backup"].Label == "backup") {
		t.Fatalf("option completion = %v", got)
	}
	// Choices of an option.
	c.Replace(uri, "- hosts: all\n  tasks:\n    - ansible.builtin.copy:\n        state: ")
	got = labels(c.Complete(uri, 3, 15))
	if _, ok := got["absent"]; !ok {
		t.Fatalf("choices = %v", got)
	}
	// Tasks file (role) at top level.
	tf := document.PathToURI(filepath.Join(ws, "roles/web/tasks/main.yml"))
	c.Open(tf, "ansible", "- name: x\n  ")
	if got := labels(c.Complete(tf, 1, 2)); got["ansible.builtin.copy"].Label == "" {
		t.Fatalf("tasks file completion = %v", got)
	}
}

func TestDefinition(t *testing.T) {
	c, ws := setup(t)
	write(t, filepath.Join(ws, "roles/web/tasks/main.yml"), "- name: hi\n  ansible.builtin.debug: {}\n")
	write(t, filepath.Join(ws, "roles/web/tasks/extra.yml"), "- name: extra\n")
	write(t, filepath.Join(ws, "roles/web/templates/app.conf.j2"), "x\n")
	write(t, filepath.Join(ws, "roles/web/handlers/main.yml"), "- name: restart web\n  ansible.builtin.service: {}\n")

	site := "- hosts: all\n  roles:\n    - web\n  tasks:\n    - ansible.builtin.include_role:\n        name: web\n  handlers:\n    - name: reload\n      listen: reload all\n"
	uri := c.Open(document.PathToURI(filepath.Join(ws, "site.yml")), "ansible", site)
	def := func(uri protocol.DocumentURI, line, char uint32) []protocol.Location {
		var locs []protocol.Location
		c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Line: line, Character: char}}}, &locs)
		return locs
	}
	if l := def(uri, 2, 7); len(l) != 1 || !strings.HasSuffix(string(l[0].URI), "roles/web/tasks/main.yml") {
		t.Fatalf("roles item -> %+v", l)
	}
	if l := def(uri, 5, 15); len(l) != 1 || !strings.HasSuffix(string(l[0].URI), "roles/web/tasks/main.yml") {
		t.Fatalf("include_role name -> %+v", l)
	}

	tasks := "- ansible.builtin.include_tasks: extra.yml\n- ansible.builtin.template:\n    src: app.conf.j2\n    dest: /etc/app.conf\n  notify: restart web\n"
	tf := c.Open(document.PathToURI(filepath.Join(ws, "roles/web/tasks/main.yml")), "ansible", tasks)
	if l := def(tf, 0, 36); len(l) != 1 || !strings.HasSuffix(string(l[0].URI), "roles/web/tasks/extra.yml") {
		t.Fatalf("include_tasks -> %+v", l)
	}
	if l := def(tf, 2, 10); len(l) != 1 || !strings.HasSuffix(string(l[0].URI), "roles/web/templates/app.conf.j2") {
		t.Fatalf("template src -> %+v", l)
	}
	if l := def(tf, 4, 12); len(l) != 1 || !strings.HasSuffix(string(l[0].URI), "roles/web/handlers/main.yml") || l[0].Range.Start.Line != 0 {
		t.Fatalf("notify -> %+v", l)
	}

	play := "- hosts: all\n  tasks:\n    - ansible.builtin.command: x\n      notify: reload all\n  handlers:\n    - name: reload\n      listen: reload all\n"
	pu := c.Open(document.PathToURI(filepath.Join(ws, "play.yml")), "ansible", play)
	if l := def(pu, 3, 16); len(l) != 1 || l[0].Range.Start.Line != 6 {
		t.Fatalf("notify listen in playbook -> %+v", l)
	}
}
