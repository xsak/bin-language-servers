package ini

import (
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func TestParse(t *testing.T) {
	src := "; top\nroot=1\n[core]\n  editor = vim\n  bare\n[remote \"origin\"]\n\turl = x\n[broken\nExecStart=/bin/a \\\n  --flag\n"
	f := Parse(src, "/home/u/.gitconfig")
	if len(f.Sections) != 3 || f.Sections[1].Name != "core" || f.Sections[2].Name != `remote "origin"` {
		t.Fatalf("sections = %+v", f.Sections)
	}
	if e := f.Sections[1].Entries[0]; e.Key != "editor" || e.Value != "vim" || src[e.KStart:e.KEnd] != "editor" {
		t.Fatalf("entry = %+v", e)
	}
	if len(f.Problems) != 1 || !strings.Contains(f.Problems[0].msg, "missing ']'") {
		t.Fatalf("problems = %+v (bare keys allowed in git config)", f.Problems)
	}
	if v := f.Sections[2].Entries[1].Value; v != "/bin/a  --flag" {
		t.Fatalf("continuation = %q", v)
	}
}

func TestSystemd(t *testing.T) {
	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///w")
	uri := c.Open("file:///w/app.service", "ini", "Description=x\n[Service]\nType: simple\n")
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("Section")))
	if !strings.Contains(msgs, "must be inside a [Section]") || !strings.Contains(msgs, "Key=Value, not ':'") {
		t.Fatalf("diagnostics:\n%s", msgs)
	}

	c.Replace(uri, "[Service]\nType=simpel\nRestart=always\nPrivateTmp=true\n")
	if msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("simpel"))); !strings.Contains(msgs, "Type=simpel: expected one of") ||
		strings.Contains(msgs, "Restart") || strings.Contains(msgs, "PrivateTmp") {
		t.Fatalf("value check:\n%s", msgs)
	}

	labels := func(l *protocol.CompletionList) map[string]protocol.CompletionItem {
		m := map[string]protocol.CompletionItem{}
		for _, it := range l.Items {
			m[it.Label] = it
		}
		return m
	}
	c.Replace(uri, "[Unit]\nDescription=x\n[Service]\nRes")
	got := labels(c.Complete(uri, 3, 3))
	if it, ok := got["Restart"]; !ok || it.TextEdit.NewText != "Restart=" {
		t.Fatalf("directive completion = %v", got)
	}
	c.Replace(uri, "[Service]\nRestart=")
	if _, ok := labels(c.Complete(uri, 1, 8))["on-failure"]; !ok {
		t.Fatal("value completion")
	}
	c.Replace(uri, "[Unit]\n[")
	if _, ok := labels(c.Complete(uri, 1, 1))["Service"]; !ok {
		t.Fatal("section completion")
	}
	c.Replace(uri, "[Service]\nExecStart=/bin/true\n")
	if h := c.Hover(uri, 1, 2); h == nil || !strings.Contains(h.Contents.Value, "Command run to start") {
		t.Fatalf("hover = %+v", h)
	}

	q := c.Open("file:///w/web.container", "ini", "[Container]\nIm")
	if _, ok := labels(c.Complete(q, 1, 2))["Image"]; !ok {
		t.Fatal("quadlet completion")
	}
}

func TestFormat(t *testing.T) {
	s := &Server{}
	format := func(path, src string) string {
		doc := document.New(document.PathToURI(path), "ini", 1, src, "")
		edits, _ := s.Format(nil, doc, protocol.FormattingOptions{})
		if len(edits) == 0 {
			return src
		}
		return edits[0].NewText
	}
	if got := format("/x/a.ini", "[ main ]\na = 1   \nb=2\n\n\n\nc  =  3\n"); got != "[main]\na = 1\nb = 2\n\nc = 3\n" {
		t.Errorf("ini: %q", got)
	}
	if got := format("/x/a.service", "[Service]\nType = simple\nExecStart = /bin/a \\\n    --x\n"); got != "[Service]\nType=simple\nExecStart=/bin/a \\\n    --x\n" {
		t.Errorf("systemd: %q", got)
	}
}
