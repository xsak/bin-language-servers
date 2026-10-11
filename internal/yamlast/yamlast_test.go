package yamlast

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/tree"
)

func TestRangesAndValues(t *testing.T) {
	src := "é: \"héllo\"   # c\nlist:\n  - a: 1\n    b: [x, y]\n  - plain text\nlit: |\n  line1\n  line2\nempty:\nnum: 1.5\n"
	res := Parse(src)
	if len(res.Problems) != 0 || len(res.Docs) != 1 {
		t.Fatalf("problems %+v docs %d", res.Problems, len(res.Docs))
	}
	root := res.Docs[0].Root
	check := func(what string, start, end int, want string) {
		t.Helper()
		if got := src[start:end]; got != want {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
	p := root.Prop("é")
	check("key é", p.KeyStart, p.KeyEnd, "é")
	check("value", p.Value.Start, p.Value.End, `"héllo"`)

	list := root.Prop("list").Value
	if list.Kind != tree.Array || len(list.Items) != 2 {
		t.Fatalf("list = %+v", list)
	}
	item := list.Items[0]
	check("item key b", item.Prop("b").KeyStart, item.Prop("b").KeyEnd, "b")
	check("flow seq", item.Prop("b").Value.Start, item.Prop("b").Value.End, "[x, y]")
	check("plain", list.Items[1].Start, list.Items[1].End, "plain text")

	lit := root.Prop("lit").Value
	if lit.Value != "line1\nline2\n" {
		t.Errorf("literal value %q", lit.Value)
	}
	check("literal", lit.Start, lit.End, "|\n  line1\n  line2")

	empty := root.Prop("empty")
	if empty.Value.Kind != tree.Null || empty.Value.Start != empty.Colon+1 {
		t.Errorf("empty value %+v colon %d", empty.Value, empty.Colon)
	}
	if root.Prop("num").Value.Value != json.Number("1.5") {
		t.Errorf("num = %#v", root.Prop("num").Value.Value)
	}
}

func TestMultiDocAndAnchors(t *testing.T) {
	src := "base: &b\n  x: 1\nuse:\n  <<: *b\n  y: 2\nref: *b\n---\nkind: Pod\n"
	res := Parse(src)
	if len(res.Docs) != 2 {
		t.Fatalf("docs = %d, problems %+v", len(res.Docs), res.Problems)
	}
	v := res.Docs[0].Root.ToValue().(map[string]any)
	use := v["use"].(map[string]any)
	if use["x"] != json.Number("1") || use["y"] != json.Number("2") {
		t.Fatalf("merge failed: %#v", use)
	}
	if v["ref"].(map[string]any)["x"] != json.Number("1") {
		t.Fatalf("alias failed: %#v", v["ref"])
	}
	if res.Docs[1].Root.StringProp("kind") != "Pod" || res.Docs[1].Start != strings.Index(src, "---") {
		t.Fatalf("second doc wrong: %+v", res.Docs[1])
	}
}

func TestSyntaxErrorAndDuplicates(t *testing.T) {
	res := Parse("a: 1\n  b: 2\n")
	if len(res.Problems) != 1 || res.Problems[0].Warning {
		t.Fatalf("problems = %+v", res.Problems)
	}
	res = Parse("a: 1\na: 2\n")
	if len(res.Problems) != 1 || !res.Problems[0].Warning || !strings.Contains(res.Problems[0].Message, "repeated") {
		t.Fatalf("duplicate: %+v", res.Problems)
	}
}
