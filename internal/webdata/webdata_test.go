package webdata

import (
	"strings"
	"testing"
)

func TestData(t *testing.T) {
	h := HTML()
	div := Find(h.Tags, "div")
	if div == nil || div.Description == "" || len(h.GlobalAttributes) < 20 || len(h.ValueSets) == 0 {
		t.Fatalf("html data incomplete: %d tags", len(h.Tags))
	}
	c := CSS()
	display := Find(c.Properties, "display")
	if display == nil || len(display.Values) == 0 || !strings.Contains(display.Markdown(), "MDN Reference") {
		t.Fatalf("css display = %+v", display)
	}
	if Find(c.AtDirectives, "@media") == nil || Find(c.PseudoClasses, ":hover") == nil || Find(c.PseudoElements, "::before") == nil {
		t.Fatal("css selectors data missing")
	}
}
