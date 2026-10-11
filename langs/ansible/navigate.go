package ansible

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/tree"
	"github.com/xsak/bin-language-servers/internal/yamlast"
	"github.com/xsak/bin-language-servers/langs/yaml"
)

// short strips the ansible.builtin./ansible.legacy. prefix from a module key.
func short(k string) string {
	k = strings.TrimPrefix(k, "ansible.builtin.")
	return strings.TrimPrefix(k, "ansible.legacy.")
}

// Definition jumps from roles, included/imported files, templates/files and notify to their
// targets.
func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	off := doc.Offset(pos)
	root := yaml.DocAt(doc.Text, off)
	if root == nil {
		return nil, nil
	}
	loc, ok := root.At(off)
	if !ok || loc.Node == nil || loc.Node.Kind != tree.String {
		return nil, nil
	}
	val, _ := loc.Node.Value.(string)
	if val == "" || strings.Contains(val, "{{") {
		return nil, nil
	}
	path := loc.Path
	docDir := filepath.Dir(doc.Path())
	key := ""
	if n := len(path); n > 0 && !path[n-1].IsIndex {
		key = path[n-1].Key
	}
	parentKey := ""
	if n := len(path); n >= 2 && !path[n-2].IsIndex {
		parentKey = short(path[n-2].Key)
	}
	inList := len(path) >= 2 && path[len(path)-1].IsIndex

	var target string
	switch {
	// roles: [name] / roles: [{role: name}] / include_role|import_role: {name: x}
	case inList && path[len(path)-2].Key == "roles",
		key == "role",
		key == "name" && (parentKey == "include_role" || parentKey == "import_role"):
		target = s.findRole(docDir, val)
	// include_tasks: file.yml / import_tasks / import_playbook / include_vars
	case isOneOf(short(key), "include_tasks", "import_tasks", "import_playbook", "include_vars", "include"),
		key == "file" && isOneOf(parentKey, "include_tasks", "import_tasks", "include_vars"):
		target = findFile(val, docDir, filepath.Join(docDir, "..", "tasks"), filepath.Join(docDir, "..", "vars"),
			s.Env.RootPath)
	case inList && path[len(path)-2].Key == "vars_files":
		target = findFile(val, docDir, s.Env.RootPath)
	// template/copy src
	case key == "src" && parentKey == "template":
		target = findFile(val, filepath.Join(docDir, "..", "templates"), filepath.Join(docDir, "templates"), docDir)
	case key == "src" && (parentKey == "copy" || parentKey == "script" || parentKey == "unarchive"):
		target = findFile(val, filepath.Join(docDir, "..", "files"), filepath.Join(docDir, "files"), docDir)
	// notify: handler name (or list item)
	case key == "notify" || (inList && path[len(path)-2].Key == "notify"):
		return s.findHandler(doc, val), nil
	}
	if target == "" {
		return nil, nil
	}
	return []protocol.Location{{URI: document.PathToURI(target)}}, nil
}

func isOneOf(s string, list ...string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

// findRole returns roles/<name>/tasks/main.yml (or the role directory's first file).
func (s *Server) findRole(docDir, name string) string {
	parts := strings.Split(name, ".")
	if len(parts) == 3 {
		// namespace.collection.role
		for _, cp := range collectionPaths(nil) {
			if p := roleEntry(filepath.Join(cp, "ansible_collections", parts[0], parts[1], "roles", parts[2])); p != "" {
				return p
			}
		}
	}
	for _, dir := range s.roleDirs(docDir) {
		if p := roleEntry(filepath.Join(dir, name)); p != "" {
			return p
		}
	}
	return ""
}

func roleEntry(dir string) string {
	for _, f := range []string{"tasks/main.yml", "tasks/main.yaml", "meta/main.yml", "README.md"} {
		if p := filepath.Join(dir, f); isFile(p) {
			return p
		}
	}
	return ""
}

func findFile(name string, dirs ...string) string {
	if filepath.IsAbs(name) {
		if isFile(name) {
			return name
		}
		return ""
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if p := filepath.Clean(filepath.Join(d, name)); isFile(p) {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// findHandler looks for a handler with that name or listen topic: in the plays' handlers
// sections of the current file, in the file itself if it is a handlers file, and in the role's
// handlers/main.yml.
func (s *Server) findHandler(doc *document.Doc, name string) []protocol.Location {
	var out []protocol.Location
	match := func(d *document.Doc, handlers *tree.Node) {
		if handlers == nil || handlers.Kind != tree.Array {
			return
		}
		for _, h := range handlers.Items {
			for _, k := range []string{"name", "listen"} {
				if p := h.Prop(k); p != nil && p.Value != nil && p.Value.Value == name {
					out = append(out, protocol.Location{URI: d.URI, Range: d.Range(p.Value.Start, p.Value.End)})
				}
			}
		}
	}
	search := func(d *document.Doc, isHandlersFile bool) {
		for _, yd := range yamlast.Parse(d.Text).Docs {
			if yd.Root == nil || yd.Root.Kind != tree.Array {
				continue
			}
			if isHandlersFile {
				match(d, yd.Root)
				continue
			}
			for _, play := range yd.Root.Items {
				if p := play.Prop("handlers"); p != nil {
					match(d, p.Value)
				}
			}
		}
	}
	search(doc, strings.Contains(filepath.ToSlash(doc.Path()), "/handlers/"))
	dir := filepath.Dir(doc.Path())
	for _, f := range []string{"../handlers/main.yml", "../handlers/main.yaml"} {
		p := filepath.Clean(filepath.Join(dir, f))
		if data, err := os.ReadFile(p); err == nil && p != doc.Path() {
			search(document.New(document.PathToURI(p), "ansible", 0, string(data), doc.Encoding()), true)
		}
	}
	return out
}
