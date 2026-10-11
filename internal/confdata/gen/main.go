// Command gen builds the Apache httpd and nginx directive tables from the official documentation
// (httpd.apache.org quick reference, nginx.org directive pages): name, syntax, default, contexts,
// module, a short description and the link to the full documentation.
// Run with: scripts/go.sh generate ./internal/confdata/
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Directive is one documented directive.
type Directive struct {
	Name      string   `json:"name"`
	Syntax    []string `json:"syntax"`
	Default   string   `json:"default,omitempty"`
	Contexts  []string `json:"contexts"`
	Module    string   `json:"module"`
	Status    string   `json:"status,omitempty"`
	Container bool     `json:"container,omitempty"` // Apache <Section> directives
	Desc      string   `json:"desc"`
	URL       string   `json:"url"`
}

var client = &http.Client{Timeout: 60 * time.Second}

func get(url string) string {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return string(b)
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Second)
	}
	fmt.Fprintln(os.Stderr, "failed:", url)
	os.Exit(1)
	return ""
}

var (
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

func text(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(s, "")), " "))
}

// shorten keeps the first two sentences, at most ~400 characters.
func shorten(s string) string {
	out := ""
	for i, sentence := range strings.SplitAfter(s, ". ") {
		if i == 2 || len(out)+len(sentence) > 400 && out != "" {
			break
		}
		out += sentence
	}
	return strings.TrimSpace(out)
}

func apache() []Directive {
	const base = "https://httpd.apache.org/docs/2.4/mod/"
	page := get(base + "quickreference.html")
	start := strings.Index(page, `<table class="qref"`)
	end := strings.Index(page[start:], "</table>")
	table := page[start : start+end]
	rowRe := regexp.MustCompile(`(?s)<tr[^>]*><td><a href="([^"#]+)#([^"]+)"[^>]*>(.*?)</a></td><td>(.*?)</td><td>(.*?)</td><td>(.*?)</td></tr><tr[^>]*><td class="descr"[^>]*>(.*?)</td></tr>`)
	ctxNames := map[rune]string{'s': "server config", 'v': "virtual host", 'd': "directory", 'h': ".htaccess"}
	statusNames := map[string]string{"C": "Core", "M": "MPM", "B": "Base", "E": "Extension", "X": "Experimental", "T": "External"}
	var out []Directive
	for _, m := range rowRe.FindAllStringSubmatch(table, -1) {
		syntax := text(m[3])
		d := Directive{Syntax: []string{syntax}, Default: text(m[4]), Module: strings.TrimSuffix(m[1], ".html"),
			Status: statusNames[text(m[6])], Desc: text(m[7]), URL: base + m[1] + "#" + m[2]}
		name := strings.Fields(syntax)[0]
		if strings.HasPrefix(name, "<") {
			d.Container = true
			name = strings.TrimSuffix(strings.TrimPrefix(name, "<"), ">")
		}
		d.Name = name
		for _, c := range text(m[5]) {
			if n, ok := ctxNames[c]; ok {
				d.Contexts = append(d.Contexts, n)
			}
		}
		out = append(out, d)
	}
	return out
}

func nginx() []Directive {
	const base = "https://nginx.org/en/docs/"
	index := get(base + "dirindex.html")
	pageRe := regexp.MustCompile(`href="([a-z0-9_/]+\.html)#`)
	pages := map[string]bool{}
	for _, m := range pageRe.FindAllStringSubmatch(index, -1) {
		pages[m[1]] = true
	}
	var names []string
	for p := range pages {
		names = append(names, p)
	}
	sort.Strings(names)
	thRe := regexp.MustCompile(`(?s)<th>\s*(\w+):\s*</th>\s*<td>(.*?)</td>`)
	var out []Directive
	for _, p := range names {
		page := get(base + p)
		module := strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".html")
		for _, chunk := range strings.Split(page, `<a name="`)[1:] {
			if !strings.Contains(chunk[:min(len(chunk), 200)], `<div class="directive">`) {
				continue
			}
			anchor := chunk[:strings.IndexByte(chunk, '"')]
			// The table may be followed by a version note before </div>.
			tEnd := strings.Index(chunk, "</table>")
			divEnd := strings.Index(chunk, "</div>")
			if tEnd < 0 || divEnd < tEnd {
				continue
			}
			d := Directive{Module: module, URL: base + p + "#" + anchor}
			for _, m := range thRe.FindAllStringSubmatch(chunk[:tEnd], -1) {
				switch m[1] {
				case "Syntax":
					for _, s := range strings.Split(m[2], "<br>") {
						if t := text(s); t != "" {
							d.Syntax = append(d.Syntax, t)
						}
					}
				case "Default":
					d.Default = text(m[2])
				case "Context":
					for _, c := range strings.Split(text(m[2]), ",") {
						if c = strings.TrimSpace(c); c != "" {
							d.Contexts = append(d.Contexts, c)
						}
					}
				}
			}
			if len(d.Syntax) == 0 {
				continue
			}
			d.Name = strings.Fields(d.Syntax[0])[0]
			rest := chunk[divEnd+len("</div>"):]
			if i := strings.Index(rest, `<a name="`); i >= 0 {
				rest = rest[:i]
			}
			if i := strings.Index(rest, "<center>"); i >= 0 {
				rest = rest[:i]
			}
			d.Desc = shorten(text(rest))
			out = append(out, d)
		}
	}
	return out
}

func write(name string, v any) {
	data, _ := json.Marshal(v)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(data)
	zw.Close()
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s: %d bytes\n", name, buf.Len())
}

func main() {
	a := apache()
	n := nginx()
	if len(a) < 400 || len(n) < 500 {
		fmt.Fprintf(os.Stderr, "too few directives (apache %d, nginx %d); the page layout may have changed\n", len(a), len(n))
		os.Exit(1)
	}
	fmt.Printf("apache: %d directives, nginx: %d directives\n", len(a), len(n))
	write("apache.json.gz", a)
	write("nginx.json.gz", n)
}
