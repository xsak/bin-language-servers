package schema

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// SchemaStoreCatalogURL is the public SchemaStore catalog.
const SchemaStoreCatalogURL = "https://www.schemastore.org/api/json/catalog.json"

// CatalogEntry is one schema of the catalog.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	FileMatch   []string `json:"fileMatch"`
	URL         string   `json:"url"`
}

// Catalog maps file names to schema URLs using SchemaStore's catalog.
type Catalog struct {
	store *Store
	url   string

	once    sync.Once
	done    chan struct{}
	entries []catalogMatcher
}

type catalogMatcher struct {
	entry CatalogEntry
	globs *GlobSet
}

// NewCatalog creates a catalog loaded lazily from url (SchemaStoreCatalogURL if empty).
func NewCatalog(store *Store, url string) *Catalog {
	if url == "" {
		url = SchemaStoreCatalogURL
	}
	return &Catalog{store: store, url: url, done: make(chan struct{})}
}

// Match returns the catalog entry for path (zero value if none matches). filter, if not nil,
// limits the entries considered by their fileMatch patterns (see ForYAML, ForJSON). While the
// catalog is still loading it returns ErrPending, and the store's OnLoaded callback fires when
// it is ready.
func (c *Catalog) Match(ctx context.Context, path string, filter func([]string) bool) (CatalogEntry, error) {
	c.once.Do(func() { go c.load() })
	select {
	case <-c.done:
	case <-ctx.Done():
		return CatalogEntry{}, ErrPending
	}
	for _, m := range c.entries {
		if filter != nil && !filter(m.entry.FileMatch) {
			continue
		}
		if m.globs.Match(path) {
			return m.entry, nil
		}
	}
	return CatalogEntry{}, nil
}

// ForYAML keeps catalog entries meant for YAML files.
func ForYAML(globs []string) bool { return anySuffix(globs, ".yaml", ".yml") }

// ForJSON keeps catalog entries that are not for YAML or TOML files.
func ForJSON(globs []string) bool {
	for _, g := range globs {
		if !anySuffix([]string{g}, ".yaml", ".yml", ".toml") {
			return true
		}
	}
	return false
}

func anySuffix(globs []string, suffixes ...string) bool {
	for _, g := range globs {
		for _, s := range suffixes {
			if strings.HasSuffix(g, s) {
				return true
			}
		}
	}
	return false
}

func (c *Catalog) load() {
	defer func() {
		close(c.done)
		c.store.opts.OnLoaded()
	}()
	data, err := c.store.Fetch(c.url)
	if err != nil {
		c.store.log.Warn("schema catalog unavailable", "url", c.url, "err", err)
		return
	}
	var cat struct {
		Schemas []CatalogEntry `json:"schemas"`
	}
	if err := json.Unmarshal(data, &cat); err != nil {
		c.store.log.Warn("bad schema catalog", "err", err)
		return
	}
	for _, e := range cat.Schemas {
		if len(e.FileMatch) == 0 || e.URL == "" {
			continue
		}
		c.entries = append(c.entries, catalogMatcher{entry: e, globs: NewGlobSet(e.FileMatch)})
	}
}

// GlobSet matches paths against VS Code style fileMatch patterns. Patterns without a slash
// match the base name; patterns with a slash match the end of the path; "**" spans
// directories; a leading "!" excludes.
type GlobSet struct {
	include, exclude []*regexp.Regexp
}

// NewGlobSet compiles patterns; invalid ones are ignored.
func NewGlobSet(patterns []string) *GlobSet {
	g := &GlobSet{}
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		re, err := regexp.Compile(globToRegexp(p))
		if err != nil {
			continue
		}
		if neg {
			g.exclude = append(g.exclude, re)
		} else {
			g.include = append(g.include, re)
		}
	}
	return g
}

// Match reports whether path (an absolute or relative file path) matches.
func (g *GlobSet) Match(path string) bool {
	path = filepath.ToSlash(path)
	matched := false
	for _, re := range g.include {
		if re.MatchString(path) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, re := range g.exclude {
		if re.MatchString(path) {
			return false
		}
	}
	return true
}

func globToRegexp(glob string) string {
	glob = strings.TrimPrefix(glob, "./")
	var b strings.Builder
	switch {
	case strings.HasPrefix(glob, "/"):
		b.WriteString("^") // absolute pattern
	case strings.HasPrefix(glob, "**/"):
		b.WriteString("(^|/)")
		glob = strings.TrimPrefix(glob, "**/")
	default:
		b.WriteString("(^|/)")
	}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			j := strings.IndexByte(glob[i:], '}')
			if j < 0 {
				b.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			alts := strings.Split(glob[i+1:i+j], ",")
			for k, a := range alts {
				alts[k] = regexp.QuoteMeta(a)
			}
			b.WriteString("(" + strings.Join(alts, "|") + ")")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}
