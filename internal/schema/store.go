// Package schema loads, caches and compiles JSON Schemas and uses them to validate, complete
// and describe tree.Node documents. JSON, YAML, Compose, Ansible and Kubernetes support are all
// built on it.
package schema

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	goyaml "github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrNotFound means the schema URL does not exist (HTTP 404 or missing file).
var ErrNotFound = errors.New("schema not found")

// ErrPending means the schema is still being loaded; callers retry after the store's OnLoaded
// callback fires.
var ErrPending = errors.New("schema is loading")

// Options configures a Store.
type Options struct {
	CacheDir string        // on-disk cache for downloaded documents; "" disables it
	TTL      time.Duration // re-download cached documents older than this (default 24h)
	Offline  bool          // never use the network
	Logger   *slog.Logger
	// OnLoaded is called (in a background goroutine) whenever a schema finished loading, so
	// the language module can re-run diagnostics.
	OnLoaded func()
	// HTTPClient overrides the client used for downloads (tests).
	HTTPClient *http.Client
}

// Schema is a compiled schema plus access to the raw documents it came from, for annotations
// the validator doesn't keep (markdownDescription, enumDescriptions, defaultSnippets).
type Schema struct {
	URL   string
	Root  *jsonschema.Schema
	store *Store
}

type entry struct {
	done   chan struct{}
	schema *Schema
	err    error
	at     time.Time // when loading finished
}

// retryAfter is how long a failed (non-404) load is remembered before trying again.
const retryAfter = time.Minute

// Store loads and compiles schemas. It is safe for concurrent use.
type Store struct {
	opts Options
	http *http.Client
	log  *slog.Logger

	mu       sync.Mutex
	compiled map[string]*entry
	raw      map[string]any // url (without fragment) -> parsed document
	inline   map[string]any // registered in-memory schemas
}

// NewStore creates a store.
func NewStore(opts Options) *Store {
	if opts.TTL == 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.OnLoaded == nil {
		opts.OnLoaded = func() {}
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Store{
		opts:     opts,
		http:     client,
		log:      opts.Logger,
		compiled: map[string]*entry{},
		raw:      map[string]any{},
		inline:   map[string]any{},
	}
}

// AddInline registers an in-memory schema document under url (any unique URL, e.g.
// "inline://settings/0"). Re-registering replaces it and drops compiled schemas.
func (s *Store) AddInline(url string, doc any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inline[url] = doc
	delete(s.raw, url)
	s.compiled = map[string]*entry{}
}

// Get returns the compiled schema for url, loading it in the background if needed. It waits
// until the schema is ready or ctx is done; in the latter case it returns ErrPending.
func (s *Store) Get(ctx context.Context, url string) (*Schema, error) {
	s.mu.Lock()
	e, ok := s.compiled[url]
	if ok && e.failedLongAgo() {
		ok = false
	}
	if !ok {
		e = &entry{done: make(chan struct{})}
		s.compiled[url] = e
		go s.load(url, e)
	}
	s.mu.Unlock()

	select {
	case <-e.done:
		return e.schema, e.err
	case <-ctx.Done():
		return nil, ErrPending
	}
}

func (e *entry) failedLongAgo() bool {
	select {
	case <-e.done:
		return e.err != nil && !errors.Is(e.err, ErrNotFound) && time.Since(e.at) > retryAfter
	default:
		return false
	}
}

func (s *Store) load(url string, e *entry) {
	c := jsonschema.NewCompiler()
	c.UseLoader(loaderFunc(s.document))
	c.UseRegexpEngine(lenientRegexp)
	root, err := c.Compile(url)
	if err != nil {
		if errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), ErrNotFound.Error()) {
			e.err = ErrNotFound
		} else {
			e.err = err
		}
		s.log.Warn("schema load failed", "url", url, "err", err)
	} else {
		e.schema = &Schema{URL: url, Root: root, store: s}
		s.log.Debug("schema loaded", "url", url)
	}
	e.at = time.Now()
	close(e.done)
	// Only now can waiters see the result, so notify after closing.
	s.opts.OnLoaded()
}

type loaderFunc func(string) (any, error)

func (f loaderFunc) Load(url string) (any, error) { return f(url) }

// document returns the parsed document at url (without fragment), from memory, the inline
// registry, the disk cache or the network.
func (s *Store) document(rawURL string) (any, error) {
	key, _, _ := strings.Cut(rawURL, "#")
	s.mu.Lock()
	if doc, ok := s.raw[key]; ok {
		s.mu.Unlock()
		return doc, nil
	}
	if doc, ok := s.inline[key]; ok {
		s.raw[key] = doc
		s.mu.Unlock()
		return doc, nil
	}
	s.mu.Unlock()

	data, err := s.fetch(key)
	if err != nil {
		return nil, err
	}
	doc, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	s.mu.Lock()
	s.raw[key] = doc
	s.mu.Unlock()
	return doc, nil
}

// Fetch returns the raw bytes at url using the same cache as schemas (used for the catalog).
func (s *Store) Fetch(url string) ([]byte, error) { return s.fetch(url) }

func (s *Store) fetch(rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		data, err := os.ReadFile(filepath.FromSlash(u.Path))
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", rawURL, ErrNotFound)
		}
		return data, err
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported schema URL scheme %q", u.Scheme)
	}

	cachePath := ""
	if s.opts.CacheDir != "" {
		sum := sha256.Sum256([]byte(rawURL))
		cachePath = filepath.Join(s.opts.CacheDir, hex.EncodeToString(sum[:16])+".json")
	}
	var stale []byte
	if cachePath != "" {
		if fi, err := os.Stat(cachePath); err == nil {
			data, rerr := os.ReadFile(cachePath)
			if rerr == nil {
				if time.Since(fi.ModTime()) < s.opts.TTL || s.opts.Offline {
					return data, nil
				}
				stale = data
			}
		}
	}
	if s.opts.Offline {
		return nil, fmt.Errorf("%s: offline and not cached: %w", rawURL, ErrNotFound)
	}

	data, err := s.download(rawURL)
	if err != nil {
		if stale != nil && !errors.Is(err, ErrNotFound) {
			s.log.Info("using stale cached schema", "url", rawURL, "err", err)
			return stale, nil
		}
		return nil, err
	}
	if cachePath != "" {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, data, 0o644) == nil {
				_ = os.Rename(tmp, cachePath)
			}
		}
	}
	return data, nil
}

func (s *Store) download(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bls-language-server")
	req.Header.Set("Accept", "application/json, application/schema+json, */*")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", rawURL, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", rawURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// decode parses JSON (numbers as json.Number), falling back to YAML.
func decode(data []byte) (any, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err == nil {
		return doc, nil
	}
	var v any
	if yerr := goyaml.UnmarshalWithOptions(data, &v, goyaml.UseOrderedMap()); yerr != nil {
		return nil, err
	}
	// Round-trip through JSON to get map[string]any and json.Number.
	j, jerr := json.Marshal(normalizeYAML(v))
	if jerr != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(j))
}

func normalizeYAML(v any) any {
	switch t := v.(type) {
	case goyaml.MapSlice:
		m := make(map[string]any, len(t))
		for _, it := range t {
			m[fmt.Sprint(it.Key)] = normalizeYAML(it.Value)
		}
		return m
	case map[string]any:
		for k, x := range t {
			t[k] = normalizeYAML(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = normalizeYAML(x)
		}
		return m
	case []any:
		for i, x := range t {
			t[i] = normalizeYAML(x)
		}
		return t
	}
	return v
}

// lenientRegexp compiles patterns with Go's regexp. Patterns Go can't handle (lookarounds,
// backreferences) match everything instead of failing the whole schema.
func lenientRegexp(s string) (jsonschema.Regexp, error) {
	re, err := regexp.Compile(s)
	if err != nil {
		return anyRegexp(s), nil
	}
	return re, nil
}

type anyRegexp string

func (r anyRegexp) MatchString(string) bool { return true }
func (r anyRegexp) String() string          { return string(r) }

// RawAt returns the raw JSON object at a schema location ("url#/json/pointer").
func (sch *Schema) RawAt(location string) map[string]any {
	u, frag, _ := strings.Cut(location, "#")
	sch.store.mu.Lock()
	doc := sch.store.raw[u]
	sch.store.mu.Unlock()
	if doc == nil {
		return nil
	}
	cur := doc
	if frag != "" {
		for _, part := range strings.Split(strings.TrimPrefix(frag, "/"), "/") {
			part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
			if p, err := url.PathUnescape(part); err == nil {
				part = p
			}
			switch c := cur.(type) {
			case map[string]any:
				cur = c[part]
			case []any:
				var i int
				if _, err := fmt.Sscanf(part, "%d", &i); err != nil || i < 0 || i >= len(c) {
					return nil
				}
				cur = c[i]
			default:
				return nil
			}
		}
	}
	m, _ := cur.(map[string]any)
	return m
}
