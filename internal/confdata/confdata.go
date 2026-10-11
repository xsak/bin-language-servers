// Package confdata embeds directive tables for Apache httpd and nginx, generated from their
// official documentation (httpd.apache.org, Apache License 2.0; nginx.org). Refresh with:
//
//	scripts/go.sh generate ./internal/confdata/
package confdata

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

//go:embed apache.json.gz
var apacheGz []byte

//go:embed nginx.json.gz
var nginxGz []byte

// Directive is one documented directive.
type Directive struct {
	Name      string   `json:"name"`
	Syntax    []string `json:"syntax"`
	Default   string   `json:"default"`
	Contexts  []string `json:"contexts"`
	Module    string   `json:"module"`
	Status    string   `json:"status"`
	Container bool     `json:"container"`
	Desc      string   `json:"desc"`
	URL       string   `json:"url"`
}

// Markdown renders the directive for hover and completion.
func (d Directive) Markdown() string {
	var b strings.Builder
	b.WriteString("```\n" + strings.Join(d.Syntax, "\n") + "\n```\n\n")
	if d.Desc != "" {
		b.WriteString(d.Desc + "\n\n")
	}
	if d.Default != "" {
		fmt.Fprintf(&b, "Default: `%s`  \n", d.Default)
	}
	if len(d.Contexts) > 0 {
		fmt.Fprintf(&b, "Context: %s  \n", strings.Join(d.Contexts, ", "))
	}
	fmt.Fprintf(&b, "Module: `%s`", d.Module)
	if d.Status != "" {
		b.WriteString(" (" + d.Status + ")")
	}
	if d.URL != "" {
		fmt.Fprintf(&b, "\n\n[Documentation](%s)", d.URL)
	}
	return b.String()
}

// Table indexes directives by name (case-insensitive for Apache).
type Table struct {
	All    []Directive
	byName map[string][]int
	fold   bool
}

// Lookup returns every entry for a name (nginx names can exist in several modules).
func (t *Table) Lookup(name string) []Directive {
	if t.fold {
		name = strings.ToLower(name)
	}
	var out []Directive
	for _, i := range t.byName[name] {
		out = append(out, t.All[i])
	}
	return out
}

var (
	apacheOnce, nginxOnce sync.Once
	apacheT, nginxT       *Table
)

// Apache returns the Apache httpd directives.
func Apache() *Table {
	apacheOnce.Do(func() { apacheT = load(apacheGz, true) })
	return apacheT
}

// Nginx returns the nginx directives.
func Nginx() *Table {
	nginxOnce.Do(func() { nginxT = load(nginxGz, false) })
	return nginxT
}

func load(gz []byte, fold bool) *Table {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		panic(err)
	}
	t := &Table{byName: map[string][]int{}, fold: fold}
	if err := json.NewDecoder(zr).Decode(&t.All); err != nil {
		panic(err)
	}
	for i, d := range t.All {
		k := d.Name
		if fold {
			k = strings.ToLower(k)
		}
		t.byName[k] = append(t.byName[k], i)
	}
	return t
}
