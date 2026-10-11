// Package compose is the Docker Compose language module: the YAML module with the Compose
// specification schema, plus checks and navigation for references between services, networks,
// volumes, secrets and configs.
package compose

import (
	"context"
	"fmt"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
	"github.com/xsak/bin-language-servers/internal/yamlast"
	"github.com/xsak/bin-language-servers/langs/yaml"
)

// SchemaURL is the Compose specification schema (the one SchemaStore uses).
const SchemaURL = "https://raw.githubusercontent.com/compose-spec/compose-go/master/schema/compose-spec.json"

func init() {
	lang.Register(lang.Info{
		Name:        "compose",
		Description: "Docker Compose: Compose spec schema, reference checks, completion and navigation",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module on top of the YAML module.
type Server struct {
	yaml.Server
}

func (s *Server) Name() string { return "compose" }

func (s *Server) Init(ctx context.Context, env lang.Env) error {
	s.Hooks = yaml.Hooks{SchemaURL: SchemaURL, Names: s.names}
	return s.Server.Init(ctx, env)
}

// kinds of top-level definitions a service can reference.
const (
	kService = "services"
	kNetwork = "networks"
	kVolume  = "volumes"
	kSecret  = "secrets"
	kConfig  = "configs"
)

var kindNoun = map[string]string{kService: "Service", kNetwork: "Network", kVolume: "Volume",
	kSecret: "Secret", kConfig: "Config"}

// ref is a use of a top-level name.
type ref struct {
	kind, name string
	start, end int
	service    string // the service that contains the reference
}

// collectRefs finds every reference in one Compose document.
func collectRefs(text string, root *tree.Node) []ref {
	var out []ref
	services := root.Prop(kService)
	if services == nil || services.Value == nil || services.Value.Kind != tree.Object {
		return nil
	}
	addScalar := func(kind, svc string, n *tree.Node, name string, offset int) {
		start := n.Start
		if start < len(text) && (text[start] == '"' || text[start] == '\'') {
			start++
		}
		start += offset
		out = append(out, ref{kind: kind, name: name, start: start, end: start + len(name), service: svc})
	}
	addKey := func(kind, svc string, p *tree.Prop) {
		out = append(out, ref{kind: kind, name: p.Key, start: p.KeyStart, end: p.KeyEnd, service: svc})
	}
	// listOrMap handles fields that are a list of names or a map keyed by name.
	listOrMap := func(kind, svc string, n *tree.Node, field string) {
		switch n.Kind {
		case tree.Array:
			for _, it := range n.Items {
				switch it.Kind {
				case tree.String:
					name, _ := it.Value.(string)
					if field == "links" || field == "volumes_from" {
						if strings.HasPrefix(name, "container:") {
							continue
						}
						name, _, _ = strings.Cut(name, ":")
					}
					addScalar(kind, svc, it, name, 0)
				case tree.Object:
					if src := it.Prop("source"); src != nil && src.Value != nil && src.Value.Kind == tree.String {
						name, _ := src.Value.Value.(string)
						addScalar(kind, svc, src.Value, name, 0)
					}
				}
			}
		case tree.Object:
			for _, p := range n.Props {
				addKey(kind, svc, p)
			}
		}
	}

	for _, sp := range services.Value.Props {
		svc := sp.Key
		if sp.Value == nil || sp.Value.Kind != tree.Object {
			continue
		}
		for _, f := range sp.Value.Props {
			if f.Value == nil {
				continue
			}
			switch f.Key {
			case "depends_on", "links", "volumes_from":
				listOrMap(kService, svc, f.Value, f.Key)
			case "networks":
				listOrMap(kNetwork, svc, f.Value, f.Key)
			case "secrets":
				listOrMap(kSecret, svc, f.Value, f.Key)
			case "configs":
				listOrMap(kConfig, svc, f.Value, f.Key)
			case "volumes":
				if f.Value.Kind != tree.Array {
					continue
				}
				for _, it := range f.Value.Items {
					switch it.Kind {
					case tree.String:
						v, _ := it.Value.(string)
						src, _, hasDst := strings.Cut(v, ":")
						if hasDst && isNamedVolume(src) {
							addScalar(kVolume, svc, it, src, 0)
						}
					case tree.Object:
						if it.StringProp("type") == "volume" {
							if src := it.Prop("source"); src != nil && src.Value != nil && src.Value.Kind == tree.String {
								name, _ := src.Value.Value.(string)
								addScalar(kVolume, svc, src.Value, name, 0)
							}
						}
					}
				}
			case "extends":
				switch f.Value.Kind {
				case tree.String:
					name, _ := f.Value.Value.(string)
					addScalar(kService, svc, f.Value, name, 0)
				case tree.Object:
					if f.Value.Prop("file") != nil {
						continue // another file
					}
					if p := f.Value.Prop("service"); p != nil && p.Value != nil && p.Value.Kind == tree.String {
						name, _ := p.Value.Value.(string)
						addScalar(kService, svc, p.Value, name, 0)
					}
				}
			case "network_mode":
				if v, _ := f.Value.Value.(string); strings.HasPrefix(v, "service:") {
					addScalar(kService, svc, f.Value, strings.TrimPrefix(v, "service:"), len("service:"))
				}
			}
		}
	}
	return out
}

// isNamedVolume tells a named volume from a bind-mount path in "src:dst".
func isNamedVolume(src string) bool {
	return src != "" && !strings.ContainsAny(src, "/\\$~") && src != "." && src != ".."
}

// definitions returns the declared names of a kind.
func definitions(root *tree.Node, kind string) map[string]*tree.Prop {
	out := map[string]*tree.Prop{}
	if p := root.Prop(kind); p != nil && p.Value != nil && p.Value.Kind == tree.Object {
		for _, d := range p.Value.Props {
			out[d.Key] = d
		}
	}
	return out
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	out := s.Server.Diagnose(ctx, doc)
	res := yamlast.Parse(doc.Text)
	for _, d := range res.Docs {
		if d.Root == nil {
			continue
		}
		defs := map[string]map[string]*tree.Prop{}
		for _, r := range collectRefs(doc.Text, d.Root) {
			if defs[r.kind] == nil {
				defs[r.kind] = definitions(d.Root, r.kind)
			}
			if defs[r.kind][r.name] != nil || (r.kind == kNetwork && r.name == "default") {
				continue
			}
			msg := fmt.Sprintf("%s %q is not defined under top-level %q.", kindNoun[r.kind], r.name, r.kind)
			if r.kind == kService {
				msg = fmt.Sprintf("Service %q is not defined.", r.name)
			}
			out = append(out, schemalang.Diagnostic(doc, r.start, r.end, protocol.SeverityError, msg))
		}
	}
	return out
}

// target finds the reference or definition at off.
func target(doc *document.Doc, off int) (root *tree.Node, kind, name string, start, end int, ok bool) {
	res := yamlast.Parse(doc.Text)
	for _, d := range res.Docs {
		if d.Root == nil || off < d.Start || off > d.End {
			continue
		}
		for _, r := range collectRefs(doc.Text, d.Root) {
			if off >= r.start && off <= r.end {
				return d.Root, r.kind, r.name, r.start, r.end, true
			}
		}
		for _, k := range []string{kService, kNetwork, kVolume, kSecret, kConfig} {
			for n, p := range definitions(d.Root, k) {
				if off >= p.KeyStart && off <= p.KeyEnd {
					return d.Root, k, n, p.KeyStart, p.KeyEnd, true
				}
			}
		}
	}
	return nil, "", "", 0, 0, false
}

func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	root, kind, name, _, _, ok := target(doc, doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	if p := definitions(root, kind)[name]; p != nil {
		return []protocol.Location{{URI: doc.URI, Range: doc.Range(p.KeyStart, p.KeyEnd)}}, nil
	}
	return nil, nil
}

func (s *Server) References(_ context.Context, doc *document.Doc, pos protocol.Position, includeDecl bool) ([]protocol.Location, error) {
	root, kind, name, _, _, ok := target(doc, doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	var out []protocol.Location
	if p := definitions(root, kind)[name]; p != nil && includeDecl {
		out = append(out, protocol.Location{URI: doc.URI, Range: doc.Range(p.KeyStart, p.KeyEnd)})
	}
	for _, r := range collectRefs(doc.Text, root) {
		if r.kind == kind && r.name == name {
			out = append(out, protocol.Location{URI: doc.URI, Range: doc.Range(r.start, r.end)})
		}
	}
	return out, nil
}

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	root, kind, name, start, end, ok := target(doc, doc.Offset(pos))
	if ok && kind == kService {
		if p := definitions(root, kind)[name]; p != nil && p.Value != nil && p.Value.Kind == tree.Object {
			// Only references get this summary; on the definition key the schema hover is more useful.
			if p.KeyStart != start {
				var b strings.Builder
				fmt.Fprintf(&b, "Service `%s`", name)
				for _, f := range []string{"image", "build", "container_name"} {
					if v := p.Value.StringProp(f); v != "" {
						fmt.Fprintf(&b, "\n\n%s: `%s`", f, v)
					}
				}
				r := doc.Range(start, end)
				return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: b.String()}, Range: &r}, nil
			}
		}
	}
	return s.Server.Hover(ctx, doc, pos)
}

// names offers top-level names where a reference is expected.
func (s *Server) names(path tree.Path, isKey bool, lines []string, _ int) []yaml.Name {
	if len(path) < 3 || path[0].Key != kService || path[1].IsIndex {
		return nil
	}
	current := path[1].Key
	field := path[2].Key
	listItem := len(path) == 4 && path[3].IsIndex && !isKey
	mapKey := len(path) == 3 && isKey

	collect := func(kind string, exclude string, detail string) []yaml.Name {
		var out []yaml.Name
		for _, n := range yaml.SectionKeys(lines, kind) {
			if n != exclude {
				out = append(out, yaml.Name{Label: n, Detail: detail})
			}
		}
		return out
	}
	switch field {
	case "depends_on":
		if listItem || mapKey {
			return collect(kService, current, "service")
		}
	case "links", "volumes_from":
		if listItem {
			return collect(kService, current, "service")
		}
	case "networks":
		if listItem || mapKey {
			return collect(kNetwork, "", "network")
		}
	case "secrets":
		if listItem {
			return collect(kSecret, "", "secret")
		}
	case "configs":
		if listItem {
			return collect(kConfig, "", "config")
		}
	case "volumes":
		if listItem {
			out := collect(kVolume, "", "named volume")
			for i := range out {
				out[i].Label += ":"
			}
			return out
		}
	case "extends":
		if len(path) == 4 && path[3].Key == "service" && !isKey {
			return collect(kService, current, "service")
		}
	case "network_mode":
		if len(path) == 3 && !isKey {
			out := []yaml.Name{{Label: "bridge"}, {Label: "host"}, {Label: "none"}}
			for _, n := range yaml.SectionKeys(lines, kService) {
				if n != current {
					out = append(out, yaml.Name{Label: "service:" + n, Detail: "share network with service"})
				}
			}
			return out
		}
	}
	return nil
}
