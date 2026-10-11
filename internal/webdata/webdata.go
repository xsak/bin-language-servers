// Package webdata embeds the HTML and CSS reference data of microsoft/vscode-custom-data (MIT,
// see LICENSE.vscode-custom-data): elements, attributes, CSS properties, values, at-rules and
// pseudo-classes, with descriptions and MDN links. Refresh it with:
//
//	scripts/go.sh generate ./internal/webdata/
package webdata

//go:generate go run ./gen

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed html-data.json.gz
var htmlGz []byte

//go:embed css-data.json.gz
var cssGz []byte

// Desc is a description given either as a string or as {"kind": "markdown", "value": "..."}.
type Desc string

func (d *Desc) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*d = Desc(s)
		return nil
	}
	var m struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*d = Desc(m.Value)
	return nil
}

// Status is a string in the CSS data; in the HTML data it can be an object, which is ignored.
type Status string

func (s *Status) UnmarshalJSON(b []byte) error {
	var v string
	if json.Unmarshal(b, &v) == nil {
		*s = Status(v)
	}
	return nil
}

// Reference is a documentation link.
type Reference struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Entry is the common shape of tags, attributes, properties, values, at-rules and pseudo-classes.
type Entry struct {
	Name         string      `json:"name"`
	Description  Desc        `json:"description"`
	References   []Reference `json:"references"`
	Browsers     []string    `json:"browsers"`
	Status       Status      `json:"status"` // standard, experimental, nonstandard, obsolete
	Syntax       string      `json:"syntax"`
	Relevance    int         `json:"relevance"`
	Restrictions []string    `json:"restrictions"` // CSS: kinds of values a property takes
	Values       []Entry     `json:"values"`
	ValueSet     string      `json:"valueSet"`   // HTML attributes
	Attributes   []Entry     `json:"attributes"` // HTML tags
	VoidElement  bool        `json:"void"`
}

// Markdown renders the entry for hover and completion documentation.
func (e Entry) Markdown() string {
	var b strings.Builder
	if st := string(e.Status); st == "obsolete" || st == "nonstandard" || st == "experimental" {
		fmt.Fprintf(&b, "*%s*\n\n", strings.ToUpper(st[:1])+st[1:])
	}
	b.WriteString(string(e.Description))
	if e.Syntax != "" {
		fmt.Fprintf(&b, "\n\nSyntax: `%s`", e.Syntax)
	}
	if bs := browsers(e.Browsers); bs != "" {
		b.WriteString("\n\n" + bs)
	}
	for _, r := range e.References {
		fmt.Fprintf(&b, "\n\n[%s](%s)", r.Name, r.URL)
	}
	return strings.TrimSpace(b.String())
}

var browserNames = map[string]string{"C": "Chrome", "E": "Edge", "FF": "Firefox", "S": "Safari", "IE": "IE", "O": "Opera"}

// browsers summarizes desktop browser support ("Chrome 1, Edge 12, Firefox 1, Safari 4").
func browsers(codes []string) string {
	var parts []string
	for _, c := range codes {
		i := 0
		for i < len(c) && c[i] >= 'A' && c[i] <= 'Z' {
			i++
		}
		if name, ok := browserNames[c[:i]]; ok && name != "IE" {
			parts = append(parts, name+" "+c[i:])
		}
	}
	return strings.Join(parts, ", ")
}

// HTMLData is browsers.html-data.json.
type HTMLData struct {
	Tags             []Entry `json:"tags"`
	GlobalAttributes []Entry `json:"globalAttributes"`
	ValueSets        []Entry `json:"valueSets"`
}

// CSSData is browsers.css-data.json.
type CSSData struct {
	Properties     []Entry `json:"properties"`
	AtDirectives   []Entry `json:"atDirectives"`
	PseudoClasses  []Entry `json:"pseudoClasses"`
	PseudoElements []Entry `json:"pseudoElements"`
}

var (
	htmlOnce sync.Once
	htmlData *HTMLData
	cssOnce  sync.Once
	cssData  *CSSData
)

// HTML returns the decoded HTML data (decoded once, on first use).
func HTML() *HTMLData {
	htmlOnce.Do(func() {
		htmlData = &HTMLData{}
		mustDecode(htmlGz, htmlData)
	})
	return htmlData
}

// CSS returns the decoded CSS data (decoded once, on first use).
func CSS() *CSSData {
	cssOnce.Do(func() {
		cssData = &CSSData{}
		mustDecode(cssGz, cssData)
	})
	return cssData
}

func mustDecode(gz []byte, v any) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		panic(err)
	}
	if err := json.NewDecoder(zr).Decode(v); err != nil {
		panic(err)
	}
}

// Find returns the entry with the given name (case-insensitive) or nil.
func Find(list []Entry, name string) *Entry {
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i]
		}
	}
	return nil
}
