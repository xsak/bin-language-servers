package document

import (
	"testing"

	"github.com/xsak/bin-language-servers/internal/protocol"
)

func pos(l, c uint32) protocol.Position { return protocol.Position{Line: l, Character: c} }

func TestOffsetPositionRoundTrip(t *testing.T) {
	// "é" is 2 bytes / 1 UTF-16 unit, "😀" is 4 bytes / 2 UTF-16 units.
	text := "ab\r\né😀x\nlast"
	tests := []struct {
		enc  string
		pos  protocol.Position
		want int
	}{
		{protocol.PositionEncodingUTF16, pos(0, 1), 1},
		{protocol.PositionEncodingUTF16, pos(0, 99), 2}, // clamped before \r\n
		{protocol.PositionEncodingUTF16, pos(1, 1), 6},  // after é
		{protocol.PositionEncodingUTF16, pos(1, 3), 10}, // after 😀
		{protocol.PositionEncodingUTF16, pos(1, 4), 11},
		{protocol.PositionEncodingUTF8, pos(1, 2), 6},
		{protocol.PositionEncodingUTF8, pos(1, 6), 10},
		{protocol.PositionEncodingUTF32, pos(1, 2), 10},
		{protocol.PositionEncodingUTF16, pos(2, 4), 16},
		{protocol.PositionEncodingUTF16, pos(9, 0), 16}, // past the end
	}
	for _, tt := range tests {
		d := New("file:///x", "txt", 1, text, tt.enc)
		if got := d.Offset(tt.pos); got != tt.want {
			t.Errorf("%s Offset(%v) = %d, want %d", tt.enc, tt.pos, got, tt.want)
		}
		if tt.pos.Line < 3 && tt.pos.Character < 99 {
			if got := d.Position(tt.want); got != tt.pos {
				t.Errorf("%s Position(%d) = %v, want %v", tt.enc, tt.want, got, tt.pos)
			}
		}
	}
}

func TestSurrogateMiddleStaysBeforeRune(t *testing.T) {
	d := New("file:///x", "txt", 1, "😀z", protocol.PositionEncodingUTF16)
	if got := d.Offset(pos(0, 1)); got != 0 {
		t.Fatalf("Offset in surrogate pair = %d, want 0", got)
	}
}

func TestLines(t *testing.T) {
	d := New("file:///x", "txt", 1, "a\nb\r\nc\rd\n", "")
	want := []string{"a", "b", "c", "d", ""}
	if d.LineCount() != len(want) {
		t.Fatalf("LineCount = %d, want %d", d.LineCount(), len(want))
	}
	for i, w := range want {
		if got := d.Line(i); got != w {
			t.Errorf("Line(%d) = %q, want %q", i, got, w)
		}
	}
}

func TestApplyIncremental(t *testing.T) {
	d := New("file:///x", "txt", 1, "hello world\nsecond", "")
	nd, err := d.Apply(2, []protocol.TextDocumentContentChangeEvent{
		{Range: &protocol.Range{Start: pos(0, 6), End: pos(0, 11)}, Text: "there"},
		{Range: &protocol.Range{Start: pos(1, 0), End: pos(1, 0)}, Text: "a "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if nd.Text != "hello there\na second" || nd.Version != 2 {
		t.Fatalf("got %q v%d", nd.Text, nd.Version)
	}
	if d.Text != "hello world\nsecond" {
		t.Fatal("original snapshot was mutated")
	}
	full, _ := nd.Apply(3, []protocol.TextDocumentContentChangeEvent{{Text: "new"}})
	if full.Text != "new" {
		t.Fatalf("full replace: got %q", full.Text)
	}
}

func TestURIPath(t *testing.T) {
	uri := PathToURI("/home/me/my file.yaml")
	if uri != "file:///home/me/my%20file.yaml" {
		t.Fatalf("PathToURI = %s", uri)
	}
	if p := URIToPath(uri); p != "/home/me/my file.yaml" {
		t.Fatalf("URIToPath = %s", p)
	}
	if p := URIToPath("untitled:1"); p != "" {
		t.Fatalf("non-file URI gave %q", p)
	}
}
