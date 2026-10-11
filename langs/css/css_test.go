package css

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := "/* c */\n@import url(a.css);\n.a, .b:hover { color: red; background: url(\"x;y\") !important }\n@media (min-width: 10px) { .c { --x: 1 } }\n"
	sh := Parse(src, false)
	if len(sh.Problems) != 0 {
		t.Fatalf("problems: %+v", sh.Problems)
	}
	if len(sh.Items) != 4 {
		t.Fatalf("items = %d", len(sh.Items))
	}
	rule := sh.Items[2].(*Rule)
	if rule.Prelude != ".a, .b:hover" || len(rule.Block.Items) != 2 {
		t.Fatalf("rule = %+v", rule)
	}
	bg := rule.Block.Items[1].(*Decl)
	if bg.Value != `url("x;y")` || !bg.Important || src[bg.NStart:bg.NEnd] != "background" {
		t.Fatalf("decl = %+v", bg)
	}
	media := sh.Items[3].(*Rule)
	if !media.At || media.Name != "@media" || media.Block.Items[0].(*Rule).Block.Items[0].(*Decl).Name != "--x" {
		t.Fatalf("media = %+v", media)
	}
}

func TestCheck(t *testing.T) {
	src := ".a { colr: red; display: flexx; color: ; -webkit-foo: 1; --ok: x }\n.b:hovr {}\n@medai screen {}\n@font-face { src: url(a); }\n.c { width: 1px\n"
	var msgs []string
	for _, p := range Check(src, false) {
		msgs = append(msgs, src[p.Start:p.End]+" => "+p.Msg)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{"colr => Unknown property: 'colr'.", "flexx => 'flexx' is not a valid value for 'display'.",
		"Value expected", ":hovr => Unknown pseudo-selector :hovr.", "@medai => Unknown at-rule @medai.", "Expected '}'"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, bad := range []string{"-webkit-foo", "--ok", "'src'"} {
		if strings.Contains(all, bad) {
			t.Errorf("false positive %q in:\n%s", bad, all)
		}
	}
}

func labels(items []CompletionItem) map[string]CompletionItem {
	m := map[string]CompletionItem{}
	for _, it := range items {
		m[it.Label] = it
	}
	return m
}

func TestComplete(t *testing.T) {
	at := func(src string) (string, int) { i := strings.Index(src, "|"); return src[:i] + src[i+1:], i }

	src, off := at(".a { dis| }")
	if it, ok := labels(Complete(src, off, false, true))["display"]; !ok || it.Insert != "display: $0;" || it.Start != 5 {
		t.Errorf("property completion = %+v", it)
	}
	src, off = at(".a { display: fl| }")
	got := labels(Complete(src, off, false, true))
	if _, ok := got["flex"]; !ok {
		t.Errorf("value completion missing flex")
	}
	if _, ok := got["inherit"]; !ok {
		t.Errorf("global keyword missing")
	}
	src, off = at(":root { --main: red } .a { color: var(--| }")
	if _, ok := labels(Complete(src, off, false, true))["--main"]; !ok {
		t.Errorf("custom property completion")
	}
	src, off = at(".a:ho| {}")
	if it, ok := labels(Complete(src, off, false, true))[":hover"]; !ok || it.Insert != "hover" {
		t.Errorf("pseudo-class completion = %+v", it)
	}
	src, off = at("a::bef| {}")
	if _, ok := labels(Complete(src, off, false, true))["::before"]; !ok {
		t.Errorf("pseudo-element completion")
	}
	src, off = at("@med| ")
	if _, ok := labels(Complete(src, off, false, true))["@media"]; !ok {
		t.Errorf("at-rule completion")
	}
	src, off = at(".card {} .c| {}")
	if _, ok := labels(Complete(src, off, false, true))["card"]; !ok {
		t.Errorf("class completion")
	}
	src, off = at("di| {}")
	if _, ok := labels(Complete(src, off, false, true))["div"]; !ok {
		t.Errorf("element completion")
	}
	src, off = at("colo|")
	if _, ok := labels(Complete(src, off, true, true))["color"]; !ok {
		t.Errorf("inline completion")
	}
}

func TestHoverAndSpecificity(t *testing.T) {
	src := "#nav .item:hover > a::before, li { display: flex }"
	md, s, e, ok := Hover(src, strings.Index(src, "display")+2, false)
	if !ok || src[s:e] != "display" || !strings.Contains(md, "MDN") {
		t.Errorf("property hover = %q %q", md, src[s:e])
	}
	md, _, _, ok = Hover(src, 2, false)
	if !ok || !strings.Contains(md, "(1, 2, 2)") {
		t.Errorf("specificity hover = %q", md)
	}
	md, _, _, ok = Hover(src, strings.Index(src, ":hover")+2, false)
	if !ok || !strings.Contains(strings.ToLower(md), "pointing") && !strings.Contains(md, "hover") {
		t.Errorf("pseudo hover = %q", md)
	}
	for sel, want := range map[string][3]int{
		"a": {0, 0, 1}, ".x.y": {0, 2, 0}, "#a #b": {2, 0, 0}, "ul li:not(.x)": {0, 1, 2},
		":where(#x) p": {0, 0, 1}, "input[type=text]": {0, 1, 1}, "a::after": {0, 0, 2}, "*": {0, 0, 0},
	} {
		if got := Specificity(sel); got != want {
			t.Errorf("Specificity(%q) = %v, want %v", sel, got, want)
		}
	}
}

func TestFormat(t *testing.T) {
	src := "/* top */\n.a,.b   >  c{color:red;background : url( \"a  b\" )  !important; /* note */\n}\n@media screen{.c{margin:0}}\n\n\n.d{}"
	got, ok := Format(src, "  ", "\n")
	if !ok {
		t.Fatal("refused")
	}
	want := "/* top */\n\n.a,\n.b > c {\n  color: red;\n  background: url( \"a  b\" ) !important; /* note */\n}\n\n@media screen {\n  .c {\n    margin: 0;\n  }\n}\n\n.d {}\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, ok := Format(".a { color: red", "  ", "\n"); ok {
		t.Error("formatted a broken sheet")
	}
}

func TestSymbols(t *testing.T) {
	syms := Symbols(":root { --c: red }\n@media print {\n  .a,\n  .b { color: red }\n}\n")
	if len(syms) != 2 || syms[0].Children[0].Name != "--c" || syms[1].Name != "@media print" || syms[1].Children[0].Name != ".a, .b" {
		t.Fatalf("symbols = %+v", syms)
	}
}
