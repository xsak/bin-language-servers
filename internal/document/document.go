// Package document keeps the text of open documents and converts between byte offsets and LSP
// positions in any of the LSP position encodings.
package document

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/xsak/bin-language-servers/internal/protocol"
)

// Doc is an immutable snapshot of a document. Edits produce a new Doc, so a snapshot can be
// read from any goroutine without locking.
type Doc struct {
	URI        protocol.DocumentURI
	LanguageID string
	Version    int32
	Text       string

	enc        string
	lineStarts []int // byte offset of the start of each line
}

// New builds a snapshot. enc is the negotiated position encoding ("" means UTF-16).
func New(uri protocol.DocumentURI, languageID string, version int32, text, enc string) *Doc {
	if enc == "" {
		enc = protocol.PositionEncodingUTF16
	}
	return &Doc{URI: uri, LanguageID: languageID, Version: version, Text: text, enc: enc,
		lineStarts: lineStarts(text)}
}

// Encoding returns the position encoding used by this document.
func (d *Doc) Encoding() string { return d.enc }

// LineCount returns the number of lines (a trailing newline starts an empty last line).
func (d *Doc) LineCount() int { return len(d.lineStarts) }

// Line returns the text of line n without its terminator.
func (d *Doc) Line(n int) string {
	if n < 0 || n >= len(d.lineStarts) {
		return ""
	}
	return d.Text[d.lineStarts[n]:d.lineEnd(n)]
}

// lineEnd returns the byte offset of the end of line n, excluding \r\n, \n or \r.
func (d *Doc) lineEnd(n int) int {
	end := len(d.Text)
	if n+1 < len(d.lineStarts) {
		end = d.lineStarts[n+1]
	}
	if end > d.lineStarts[n] && d.Text[end-1] == '\n' {
		end--
	}
	if end > d.lineStarts[n] && d.Text[end-1] == '\r' {
		end--
	}
	return end
}

// Offset converts a position to a byte offset. Positions past the end of a line are clamped
// to the line end, and lines past the end of the document to the document end, as LSP requires.
func (d *Doc) Offset(p protocol.Position) int {
	line := int(p.Line)
	if line >= len(d.lineStarts) {
		return len(d.Text)
	}
	off, end := d.lineStarts[line], d.lineEnd(line)
	units := 0
	for off < end && units < int(p.Character) {
		r, size := utf8.DecodeRuneInString(d.Text[off:end])
		units += unitLen(r, size, d.enc)
		if units > int(p.Character) {
			// Position points into the middle of a surrogate pair; stay before the rune.
			break
		}
		off += size
	}
	return off
}

// Position converts a byte offset to a position. Offsets inside a multi-byte rune are moved
// back to the rune start.
func (d *Doc) Position(off int) protocol.Position {
	if off < 0 {
		off = 0
	}
	if off > len(d.Text) {
		off = len(d.Text)
	}
	line := sort.Search(len(d.lineStarts), func(i int) bool { return d.lineStarts[i] > off }) - 1
	start := d.lineStarts[line]
	units := 0
	for i := start; i < off; {
		r, size := utf8.DecodeRuneInString(d.Text[i:])
		if i+size > off {
			break
		}
		units += unitLen(r, size, d.enc)
		i += size
	}
	return protocol.Position{Line: uint32(line), Character: uint32(units)}
}

// Range converts a byte span to an LSP range.
func (d *Doc) Range(start, end int) protocol.Range {
	return protocol.Range{Start: d.Position(start), End: d.Position(end)}
}

// Apply returns a new snapshot with the changes applied in order.
func (d *Doc) Apply(version int32, changes []protocol.TextDocumentContentChangeEvent) (*Doc, error) {
	cur := d
	for i, ch := range changes {
		if ch.Range == nil {
			cur = New(d.URI, d.LanguageID, version, ch.Text, d.enc)
			continue
		}
		start, end := cur.Offset(ch.Range.Start), cur.Offset(ch.Range.End)
		if start > end {
			return nil, fmt.Errorf("change %d: start after end", i)
		}
		cur = New(d.URI, d.LanguageID, version, cur.Text[:start]+ch.Text+cur.Text[end:], d.enc)
	}
	if cur == d {
		cur = New(d.URI, d.LanguageID, version, d.Text, d.enc)
	}
	return cur, nil
}

// Path returns the local file path for a file:// URI, or "" for other schemes.
func (d *Doc) Path() string { return URIToPath(d.URI) }

func unitLen(r rune, size int, enc string) int {
	switch enc {
	case protocol.PositionEncodingUTF8:
		return size
	case protocol.PositionEncodingUTF32:
		return 1
	default:
		if r >= 0x10000 {
			return 2
		}
		return 1
	}
}

func lineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			starts = append(starts, i+1)
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		}
	}
	return starts
}

// URIToPath converts a file:// URI to a local path; it returns "" for other schemes.
func URIToPath(uri protocol.DocumentURI) string {
	u, err := url.Parse(string(uri))
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.FromSlash(u.Path)
}

// PathToURI converts an absolute local path to a file:// URI.
func PathToURI(path string) protocol.DocumentURI {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return protocol.DocumentURI(u.String())
}

// Store holds the open documents.
type Store struct {
	mu   sync.RWMutex
	docs map[protocol.DocumentURI]*Doc
	enc  string
}

// NewStore creates a store using the given position encoding.
func NewStore(enc string) *Store {
	return &Store{docs: map[protocol.DocumentURI]*Doc{}, enc: enc}
}

// Open adds or replaces a document.
func (s *Store) Open(item protocol.TextDocumentItem) *Doc {
	d := New(item.URI, item.LanguageID, item.Version, item.Text, s.enc)
	s.mu.Lock()
	s.docs[item.URI] = d
	s.mu.Unlock()
	return d
}

// Change applies edits to an open document.
func (s *Store) Change(p protocol.DidChangeTextDocumentParams) (*Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil, fmt.Errorf("document not open: %s", p.TextDocument.URI)
	}
	nd, err := d.Apply(p.TextDocument.Version, p.ContentChanges)
	if err != nil {
		return nil, err
	}
	s.docs[p.TextDocument.URI] = nd
	return nd, nil
}

// Close removes a document.
func (s *Store) Close(uri protocol.DocumentURI) {
	s.mu.Lock()
	delete(s.docs, uri)
	s.mu.Unlock()
}

// Get returns the current snapshot of a document, or nil if it is not open.
func (s *Store) Get(uri protocol.DocumentURI) *Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.docs[uri]
}

// All returns snapshots of every open document.
func (s *Store) All() []*Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Doc, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out
}
