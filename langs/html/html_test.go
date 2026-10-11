package html

import (
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

const page = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>T</title>
  <style>
    .a { colr: red }
  </style>
</head>
<body>
  <ul>
    <li>one
    <li>two
  </ul>
  <p>para
  <div id="x" class="card big" style="dispaly: block">
    <img src="a.png">
    <span id="x">dup</span>
    <frob></frob>
    <my-widget></my-widget>
    <svg><circle r="1"/></svg>
  </div>
</body>
</html>
`

func TestParseStructure(t *testing.T) {
	d := Parse(page)
	var tags []string
	d.Walk(func(n *Node) { tags = append(tags, n.Tag) })
	if got := strings.Join(tags, " "); got != "html head meta title style body ul li li p div img span frob my-widget svg circle" {
		t.Fatalf("tags = %s", got)
	}
	for _, p := range d.Problems {
		t.Errorf("unexpected problem %q at %q", p.Msg, page[p.Start:p.End])
	}
	style := d.NodeAt(strings.Index(page, "colr"))
	if style.Tag != "style" || !strings.Contains(page[style.ContentStart:style.ContentEnd], "colr") {
		t.Fatalf("style content = %+v", style)
	}
	div := d.NodeAt(strings.Index(page, "card"))
	if a := div.Attr("class"); a == nil || page[a.VStart:a.VEnd] != "card big" {
		t.Fatalf("class attr = %+v", a)
	}

	bad := Parse("<div><span></div></span><b>")
	msgs := ""
	for _, p := range bad.Problems {
		msgs += p.Msg + "\n"
	}
	for _, want := range []string{"<span> is not closed before </div>", "Unexpected closing tag </span>", "<b> is not closed"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
}

func setup(t *testing.T, text string) (*lsptest.Client, protocol.DocumentURI) {
	t.Helper()
	c := lsptest.Start(t, &Server{})
	c.Initialize("file:///w")
	return c, c.Open("file:///w/index.html", "html", text)
}

func TestDiagnostics(t *testing.T) {
	c, uri := setup(t, page)
	msgs := lsptest.Messages(c.WaitDiagnostics(uri, lsptest.Contains("frob")))
	for _, want := range []string{"Unknown element <frob>.", `Duplicate id "x".`, "alt attribute",
		"Unknown property: 'colr'.", "Unknown property: 'dispaly'."} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
	for _, bad := range []string{"my-widget", "circle", "<li>", "<p>"} {
		if strings.Contains(msgs, bad) {
			t.Errorf("false positive %q in:\n%s", bad, msgs)
		}
	}
}

func labels(l *protocol.CompletionList) map[string]protocol.CompletionItem {
	m := map[string]protocol.CompletionItem{}
	if l != nil {
		for _, it := range l.Items {
			m[it.Label] = it
		}
	}
	return m
}

func TestCompletionHoverLinked(t *testing.T) {
	c, uri := setup(t, "")
	complete := func(text string) map[string]protocol.CompletionItem {
		i := strings.Index(text, "|")
		c.Replace(uri, text[:i]+text[i+1:])
		d := document.New(uri, "html", 0, text[:i]+text[i+1:], "")
		p := d.Position(i)
		return labels(c.Complete(uri, p.Line, p.Character))
	}
	if it, ok := complete("<body><di|</body>")["div"]; !ok || it.TextEdit.NewText != "div>$0</div>" {
		t.Errorf("tag completion = %+v", it)
	}
	if it, ok := complete("<br|")["br"]; !ok || it.TextEdit.NewText != "br$0>" {
		t.Errorf("void tag completion = %+v", it)
	}
	got := complete(`<input typ| name="a">`)
	if it, ok := got["type"]; !ok || it.TextEdit.NewText != `type="$1"` {
		t.Errorf("attribute completion = %+v", it)
	}
	if _, ok := got["name"]; ok {
		t.Error("present attribute offered again")
	}
	if _, ok := got["class"]; !ok {
		t.Error("global attribute missing")
	}
	if _, ok := complete(`<input type="ch|">`)["checkbox"]; !ok {
		t.Error("attribute value completion")
	}
	if it, ok := complete("<section><p>x</p></|")["/section"]; !ok || it.TextEdit.NewText != "section>" {
		t.Errorf("close tag completion = %+v", it)
	}
	if _, ok := complete("<style>.a { disp| }</style>")["display"]; !ok {
		t.Error("css in <style>")
	}
	if _, ok := complete(`<p style="col|">`)["color"]; !ok {
		t.Error("css in style attribute")
	}
	if _, ok := complete("a &co|")["&copy;"]; !ok {
		t.Error("entity completion")
	}

	c.Replace(uri, `<div class="x"><a href="#">l</a></div>`)
	if h := c.Hover(uri, 0, 2); h == nil || !strings.Contains(h.Contents.Value, "MDN") {
		t.Errorf("tag hover = %+v", h)
	}
	if h := c.Hover(uri, 0, 20); h == nil || !strings.Contains(strings.ToLower(h.Contents.Value), "url") {
		t.Errorf("attribute hover = %+v", h)
	}
	var ranges *protocol.LinkedEditingRanges
	c.Call("textDocument/linkedEditingRange", protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Line: 0, Character: 2}}, &ranges)
	if ranges == nil || len(ranges.Ranges) != 2 || ranges.Ranges[1].Start.Character != 34 {
		t.Fatalf("linked ranges = %+v", ranges)
	}

	var syms []protocol.DocumentSymbol
	c.Replace(uri, `<main id="m"><section class="a b"><style>.c{}</style></section></main>`)
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 1 || syms[0].Name != "main#m" || syms[0].Children[0].Name != "section.a.b" ||
		syms[0].Children[0].Children[0].Children[0].Name != ".c" {
		t.Fatalf("symbols = %+v", syms)
	}
}
