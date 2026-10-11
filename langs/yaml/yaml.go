// Package yaml is the YAML language module: syntax errors, JSON Schema validation per
// document (modeline, settings, Kubernetes, SchemaStore), completion, hover and outline.
package yaml

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
	"github.com/xsak/bin-language-servers/internal/yamlast"
)

func init() {
	lang.Register(lang.Info{
		Name:        "yaml",
		Description: "YAML with JSON Schema (SchemaStore, modeline, settings, Kubernetes)",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module. Modules built on YAML (compose, ansible) embed it and set Hooks.
type Server struct {
	schemalang.Base
	Hooks Hooks
}

// Hooks customize the YAML module for a YAML-based format.
type Hooks struct {
	// SchemaURL, if set, is used for every document unless a modeline says otherwise.
	SchemaURL string
	// Names returns extra completion names at path (a key position when isKey), e.g. service
	// names inside depends_on. lines is the document text split into lines; ln is the cursor
	// line.
	Names func(path tree.Path, isKey bool, lines []string, ln int) []Name
}

// Name is an extra completion candidate from a hook.
type Name struct {
	Label, Detail string
}

func (s *Server) Name() string { return "yaml" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	return s.InitBase(env, "yaml", schema.ForYAML)
}

func (s *Server) TriggerCharacters() []string { return []string{":", " ", "-"} }

// modelineRe matches "# yaml-language-server: $schema=<url>".
var modelineRe = regexp.MustCompile(`(?m)^\s*#\s*yaml-language-server:\s*\$schema=(\S+)`)

// SchemaFor is schemaFor for embedding modules.
func (s *Server) SchemaFor(ctx context.Context, doc *document.Doc, root *tree.Node, wait time.Duration) *schema.Schema {
	return s.schemaFor(ctx, doc, root, wait)
}

// schemaFor returns the schema for one document of the stream. Order: modeline, settings
// associations, Kubernetes (apiVersion + kind), SchemaStore catalog.
func (s *Server) schemaFor(ctx context.Context, doc *document.Doc, root *tree.Node, wait time.Duration) *schema.Schema {
	if m := modelineRe.FindStringSubmatch(doc.Text); m != nil {
		return s.Load(ctx, schemalang.ResolveRef(doc, m[1]), wait)
	}
	if s.Hooks.SchemaURL != "" {
		return s.Load(ctx, s.Hooks.SchemaURL, wait)
	}
	set := s.Settings()
	path := doc.Path()
	for _, a := range set.Associations {
		if a.Globs.Match(path) {
			return s.Load(ctx, a.URL, wait)
		}
	}
	if u := kubernetesURL(set.Kubernetes, path, root); u != "" {
		sch, err := s.LoadErr(ctx, u, wait)
		if err == nil {
			return sch
		}
		if !errors.Is(err, schema.ErrNotFound) {
			return nil // pending or failing; diagnostics are refreshed when it arrives
		}
		// Not a built-in kind (CRD, kustomize, ...): fall through to the catalog.
	}
	return s.Load(ctx, s.ResolveFile(ctx, doc, wait), wait)
}

// kubernetesURL returns the schema URL for a Kubernetes manifest, or "".
func kubernetesURL(k schema.Kubernetes, path string, root *tree.Node) string {
	apiVersion, kind := root.StringProp("apiVersion"), root.StringProp("kind")
	if apiVersion == "" || kind == "" || k.SchemaBase == "" {
		return ""
	}
	if k.Globs != nil {
		if !k.Globs.Match(path) {
			return ""
		}
	} else if !k.AutoDetect {
		return ""
	}
	group, version, found := strings.Cut(apiVersion, "/")
	if !found {
		group, version = "", apiVersion
	}
	name := strings.ToLower(kind)
	if group != "" {
		name += "-" + strings.ToLower(strings.Split(group, ".")[0])
	}
	return fmt.Sprintf("%s/%s-%s.json", k.SchemaBase, name, strings.ToLower(version))
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	res := yamlast.Parse(doc.Text)
	var out []protocol.Diagnostic
	for _, p := range res.Problems {
		sev := protocol.SeverityError
		if p.Warning {
			sev = protocol.SeverityWarning
		}
		out = append(out, schemalang.Diagnostic(doc, p.Start, p.End, sev, p.Message))
	}
	if !s.Settings().Validate {
		return out
	}
	for _, d := range res.Docs {
		if d.Root == nil {
			continue
		}
		if sch := s.schemaFor(ctx, doc, d.Root, schemalang.DiagnoseWait); sch != nil {
			out = append(out, schemalang.SchemaDiagnostics(doc, sch.Validate(d.Root))...)
		}
	}
	return out
}

// docAt returns the document of the stream containing off.
func docAt(res *yamlast.Result, off int) *yamlast.Document {
	for i := range res.Docs {
		d := &res.Docs[i]
		if off >= d.Start && (off < d.End || i == len(res.Docs)-1) {
			return d
		}
	}
	return nil
}

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	res := yamlast.Parse(doc.Text)
	off := doc.Offset(pos)
	d := docAt(res, off)
	if d == nil || d.Root == nil {
		return nil, nil
	}
	loc, ok := d.Root.At(off)
	if !ok {
		return nil, nil
	}
	start, end := 0, 0
	switch {
	case loc.OnKey:
		start, end = loc.Prop.KeyStart, loc.Prop.KeyEnd
	case loc.Node != nil && loc.Node.Kind != tree.Object && loc.Node.Kind != tree.Array && loc.Node.End > loc.Node.Start:
		start, end = loc.Node.Start, loc.Node.End
	default:
		return nil, nil
	}
	sch := s.schemaFor(ctx, doc, d.Root, schemalang.FeatureWait)
	return schemalang.Hover(doc, sch, loc.Path, d.Root.ToValue(), start, end), nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	res := yamlast.Parse(doc.Text)
	if len(res.Docs) == 1 {
		return schemalang.Symbols(doc, res.Docs[0].Root), nil
	}
	var out []protocol.DocumentSymbol
	for i, d := range res.Docs {
		if d.Root == nil {
			continue
		}
		name := fmt.Sprintf("Document %d", i+1)
		if kind := d.Root.StringProp("kind"); kind != "" {
			name = kind
			if meta := d.Root.Prop("metadata"); meta != nil && meta.Value != nil {
				if n := meta.Value.StringProp("name"); n != "" {
					name += " " + n
				}
			}
		}
		r := doc.Range(d.Root.Start, d.Root.End)
		out = append(out, protocol.DocumentSymbol{Name: name, Kind: protocol.SymbolNamespace, Range: r,
			SelectionRange: doc.Range(d.Root.Start, min(d.Root.End, d.Root.Start+1)),
			Children:       schemalang.Symbols(doc, d.Root)})
	}
	return out, nil
}
