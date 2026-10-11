package jsonast

import (
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/tree"
)

func TestParseValid(t *testing.T) {
	src := `{"a": 1, "b": [true, null, "xA"], "c": {"d": -1.5e3}}`
	res := Parse(src, Options{})
	if len(res.Problems) != 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	v := res.Root.ToValue().(map[string]any)
	if v["b"].([]any)[2] != "xA" {
		t.Fatalf("value = %#v", v)
	}
	p := res.Root.Prop("c")
	if src[p.KeyStart:p.KeyEnd] != `"c"` || src[p.Value.Start:p.Value.End] != `{"d": -1.5e3}` {
		t.Fatalf("ranges wrong: key %q value %q", src[p.KeyStart:p.KeyEnd], src[p.Value.Start:p.Value.End])
	}
}

func TestParseRecovers(t *testing.T) {
	tests := []struct {
		src  string
		want string // substring of a problem message
	}{
		{`{"a": 1,}`, "Trailing comma"},
		{`{"a" 1}`, "Colon expected"},
		{`{"a": }`, "Value expected"},
		{`{"a": 1 "b": 2}`, "Expected comma"},
		{`{"a": 1`, "Expected '}'"},
		{`// c` + "\n{}", "Comments are not permitted"},
		{`{"a": 1, "a": 2}`, "Duplicate object key"},
		{`{a: 1}`, "doublequoted"},
		{`[1,,2]`, "Value expected"},
		{`{"a": "unterminated}`, "Unterminated string"},
	}
	for _, tt := range tests {
		res := Parse(tt.src, Options{})
		found := false
		for _, p := range res.Problems {
			if strings.Contains(p.Message, tt.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want problem %q, got %+v", tt.src, tt.want, res.Problems)
		}
		if res.Root == nil {
			t.Errorf("%s: no tree", tt.src)
		}
	}
}

func TestJSONCAllowsCommentsAndTrailingCommas(t *testing.T) {
	res := Parse("{\n // c\n \"a\": [1,], /* b */\n}", Options{Comments: true})
	if len(res.Problems) != 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if res.Root.Prop("a").Value.Kind != tree.Array {
		t.Fatal("bad tree")
	}
}

func TestIncompleteKeepsProps(t *testing.T) {
	src := `{"name": "x", "ver`
	res := Parse(src, Options{})
	if len(res.Root.Props) != 2 || res.Root.Props[1].Key != "ver" {
		t.Fatalf("props = %+v", res.Root.Props)
	}
}

func TestFormat(t *testing.T) {
	src := `{"a":1,"b":[ ],"c":{"d":[1,2]}, // note
"e":{}}`
	got, ok := Format(src, FormatOptions{Indent: "  ", FinalNewline: true})
	if !ok {
		t.Fatal("format refused")
	}
	want := `{
  "a": 1,
  "b": [],
  "c": {
    "d": [
      1,
      2
    ]
  }, // note
  "e": {}
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, ok := Format(`{"a":`, FormatOptions{Indent: "  "}); ok {
		t.Fatal("formatted a broken document")
	}
}

func TestFormatKeepsBlankLineAndBlockComment(t *testing.T) {
	src := "{\n  \"a\": 1,\n\n  /* x */ \"b\": 2\n}"
	got, _ := Format(src, FormatOptions{Indent: "  "})
	if got != "{\n  \"a\": 1,\n\n  /* x */ \"b\": 2\n}" {
		t.Fatalf("got %q", got)
	}
}
