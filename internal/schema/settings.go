package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Association binds file patterns to a schema URL.
type Association struct {
	Globs *GlobSet
	URL   string
}

// Settings are the schema-related settings shared by the JSON and YAML modules. They accept
// the same shapes as vscode-json-languageserver ("json") and yaml-language-server ("yaml"),
// so existing editor configurations keep working.
type Settings struct {
	Associations []Association
	SchemaStore  bool // use the SchemaStore catalog
	Validate     bool
	Format       bool

	// YAML only.
	Kubernetes Kubernetes
}

// Kubernetes controls built-in Kubernetes manifest support.
type Kubernetes struct {
	// AutoDetect applies Kubernetes schemas to documents with apiVersion and kind.
	AutoDetect bool
	// Globs limits Kubernetes schemas to these files (from yaml.schemas {"kubernetes": glob}).
	Globs *GlobSet
	// SchemaBase is the URL prefix of the per-kind standalone schemas.
	SchemaBase string
}

// kubernetesSchemaRepo is yannh/kubernetes-json-schema; each Kubernetes version has a
// "<version>-standalone-strict" directory (standalone: no $refs, strict: no unknown fields).
const kubernetesSchemaRepo = "https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/"

// DefaultKubernetesSchemaBase follows the latest Kubernetes release.
const DefaultKubernetesSchemaBase = kubernetesSchemaRepo + "master-standalone-strict"

// DefaultSettings returns the defaults: everything on.
func DefaultSettings() Settings {
	return Settings{
		SchemaStore: true, Validate: true, Format: true,
		Kubernetes: Kubernetes{AutoDetect: true, SchemaBase: DefaultKubernetesSchemaBase},
	}
}

// ParseSettings reads the section ("json" or "yaml") from raw, which may be the whole settings
// object ({"json": {...}}) or the section itself. Inline schemas are registered in store.
// root is the workspace root used to resolve relative paths.
func ParseSettings(raw json.RawMessage, section, root string, store *Store) (Settings, error) {
	s := DefaultSettings()
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return s, err
	}
	if inner, ok := top[section]; ok {
		if err := json.Unmarshal(inner, &top); err != nil {
			return s, err
		}
	}

	boolOpt := func(key string, dst *bool) {
		v, ok := top[key]
		if !ok {
			return
		}
		var b bool
		if json.Unmarshal(v, &b) == nil {
			*dst = b
			return
		}
		var obj struct {
			Enable *bool `json:"enable"`
		}
		if json.Unmarshal(v, &obj) == nil && obj.Enable != nil {
			*dst = *obj.Enable
		}
	}
	boolOpt("validate", &s.Validate)
	boolOpt("format", &s.Format)
	boolOpt("schemaStore", &s.SchemaStore)

	if v, ok := top["schemas"]; ok {
		var err error
		switch section {
		case "yaml", "toml":
			err = parseYAMLSchemas(v, root, &s)
		default:
			err = parseJSONSchemas(v, root, store, &s)
		}
		if err != nil {
			return s, err
		}
	}
	if v, ok := top["kubernetes"]; ok {
		var k struct {
			AutoDetect *bool  `json:"autoDetect"`
			SchemaBase string `json:"schemaBase"`
			Version    string `json:"version"`
		}
		if json.Unmarshal(v, &k) == nil {
			if k.AutoDetect != nil {
				s.Kubernetes.AutoDetect = *k.AutoDetect
			}
			switch {
			case k.SchemaBase != "":
				s.Kubernetes.SchemaBase = strings.TrimRight(k.SchemaBase, "/")
			case k.Version != "":
				v := k.Version
				if v != "master" && !strings.HasPrefix(v, "v") {
					v = "v" + v
				}
				s.Kubernetes.SchemaBase = kubernetesSchemaRepo + v + "-standalone-strict"
			}
		}
	}
	return s, nil
}

// parseJSONSchemas handles [{"fileMatch": [...], "url": "...", "schema": {...}}].
func parseJSONSchemas(raw json.RawMessage, root string, store *Store, s *Settings) error {
	var list []struct {
		FileMatch []string        `json:"fileMatch"`
		URL       string          `json:"url"`
		Schema    json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("json.schemas: %w", err)
	}
	for i, it := range list {
		if len(it.FileMatch) == 0 {
			continue
		}
		u := ""
		if len(it.Schema) > 0 && string(it.Schema) != "null" {
			doc, err := decode(it.Schema)
			if err != nil {
				return fmt.Errorf("json.schemas[%d].schema: %w", i, err)
			}
			u = fmt.Sprintf("inline://settings/json/%d", i)
			if it.URL != "" {
				u = it.URL
			}
			store.AddInline(u, doc)
		} else if it.URL != "" {
			u = ResolveURL(it.URL, root)
		} else {
			continue
		}
		s.Associations = append(s.Associations, Association{Globs: workspaceGlobs(it.FileMatch, root), URL: u})
	}
	return nil
}

// parseYAMLSchemas handles {"url": "glob" | ["globs"]}; the key "kubernetes" selects the
// built-in Kubernetes support.
func parseYAMLSchemas(raw json.RawMessage, root string, s *Settings) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("yaml.schemas: %w", err)
	}
	for key, v := range m {
		var globs []string
		var one string
		if json.Unmarshal(v, &one) == nil {
			globs = []string{one}
		} else if err := json.Unmarshal(v, &globs); err != nil {
			return fmt.Errorf("yaml.schemas[%q]: %w", key, err)
		}
		if key == "kubernetes" {
			s.Kubernetes.Globs = workspaceGlobs(globs, root)
			continue
		}
		s.Associations = append(s.Associations, Association{Globs: workspaceGlobs(globs, root),
			URL: ResolveURL(key, root)})
	}
	return nil
}

// workspaceGlobs anchors patterns starting with "/" at the workspace root (VS Code semantics);
// other patterns match anywhere.
func workspaceGlobs(globs []string, root string) *GlobSet {
	return workspaceGlobsSlash(globs, filepath.ToSlash(root))
}

// workspaceGlobsSlash is the same in slash form; everything stays in slash form so the result
// is the same on every platform.
func workspaceGlobsSlash(globs []string, rootSlash string) *GlobSet {
	out := make([]string, 0, len(globs))
	for _, g := range globs {
		neg := strings.HasPrefix(g, "!")
		g = strings.TrimPrefix(g, "!")
		g = filepath.ToSlash(expandHome(g))
		if strings.HasPrefix(g, "/") && rootSlash != "" && !strings.HasPrefix(g, rootSlash+"/") && !fileExists(g) {
			g = path.Join(rootSlash, g)
		}
		if neg {
			g = "!" + g
		}
		out = append(out, g)
	}
	return NewGlobSet(out)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ResolveURL turns a settings value into an absolute URL: http(s)/file URLs are kept, paths
// are made absolute against root.
func ResolveURL(ref, root string) string {
	if u, err := url.Parse(ref); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		return ref
	}
	p := expandHome(ref)
	if !filepath.IsAbs(p) && root != "" {
		p = filepath.Join(root, p)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Resolver picks the schema for a file from settings and the catalog.
type Resolver struct {
	Catalog *Catalog
	Filter  func([]string) bool // catalog entry filter (ForJSON, ForYAML)

	mu       sync.RWMutex
	settings Settings
}

// SetSettings replaces the settings.
func (r *Resolver) SetSettings(s Settings) {
	r.mu.Lock()
	r.settings = s
	r.mu.Unlock()
}

// Settings returns the current settings.
func (r *Resolver) Settings() Settings {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.settings
}

// Resolve returns the schema URL for path from settings associations, then the catalog.
// It returns "" if nothing matches.
func (r *Resolver) Resolve(ctx context.Context, path string) (string, error) {
	s := r.Settings()
	for _, a := range s.Associations {
		if a.Globs.Match(path) {
			return a.URL, nil
		}
	}
	if !s.SchemaStore || r.Catalog == nil || path == "" {
		return "", nil
	}
	e, err := r.Catalog.Match(ctx, path, r.Filter)
	return e.URL, err
}
