// Package ansible is the Ansible language module: the YAML module with ansible-lint's schemas
// picked per file kind, module and option completion and hover read from the installed module
// sources, navigation to roles, included files, templates and handlers, an unknown-module check,
// and optional ansible-lint diagnostics on open and save.
package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/extproc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
	"github.com/xsak/bin-language-servers/internal/yamlast"
	"github.com/xsak/bin-language-servers/langs/yaml"
)

func init() {
	lang.Register(lang.Info{
		Name:        "ansible",
		Description: "Ansible: ansible-lint schemas, module/option completion and hover, roles and includes, ansible-lint",
		New:         func() lang.Language { return &Server{} },
	})
}

var lookPath = extproc.Find

// SchemaBase is where ansible-lint publishes its JSON schemas (the ones SchemaStore uses).
const SchemaBase = "https://raw.githubusercontent.com/ansible/ansible-lint/main/src/ansiblelint/schemas/"

// Settings use ansible-language-server's names ("ansible" section).
type Settings struct {
	AnsiblePath string // path to the ansible executable; used to find ansible-core's modules
	FQCN        bool   // complete fully qualified names (ansible.builtin.copy)
	Validation  bool   // ansible-lint
	LintPath    string
	LintArgs    []string
}

// Server implements the module on top of the YAML module.
type Server struct {
	yaml.Server

	mu    sync.Mutex
	set   Settings
	index atomic.Pointer[Index]
	gen   atomic.Int64                                   // reindex generation; older scans don't overwrite newer ones
	lint  map[protocol.DocumentURI][]protocol.Diagnostic // last ansible-lint results
}

func (s *Server) Name() string { return "ansible" }

func (s *Server) Init(ctx context.Context, env lang.Env) error {
	s.set = Settings{AnsiblePath: "ansible", FQCN: true, Validation: true, LintPath: "ansible-lint"}
	s.lint = map[protocol.DocumentURI][]protocol.Diagnostic{}
	s.Hooks = yaml.Hooks{Schema: schemaFor, Names: s.names}
	if err := s.Server.Init(ctx, env); err != nil {
		return err
	}
	if len(env.InitOptions) > 0 {
		_ = s.Configure(env.InitOptions)
	}
	s.reindex()
	return nil
}

// reindex scans module directories in the background.
func (s *Server) reindex() {
	path := s.settings().AnsiblePath
	gen := s.gen.Add(1)
	go func() {
		core := coreModuleDirs(path)
		ix := NewIndex(core, collectionPaths(core))
		s.mu.Lock()
		stale := s.gen.Load() != gen
		if !stale {
			s.index.Store(ix)
		}
		s.mu.Unlock()
		if stale {
			return
		}
		s.Env.Logger.Info("ansible modules indexed", "modules", ix.Len(), "dirs", len(ix.roots))
		if s.Env.Client != nil {
			s.Env.Client.RefreshDiagnostics()
		}
	}()
}

func (s *Server) idx() *Index {
	if ix := s.index.Load(); ix != nil {
		return ix
	}
	return &Index{files: map[string]string{}, short: map[string]string{}, docs: map[string]*ModuleDoc{}}
}

func (s *Server) settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

func (s *Server) ConfigSections() []string { return []string{"ansible", "yaml"} }

// Configure reads the "ansible" section (ansible-language-server layout) and passes the rest to
// the YAML module.
func (s *Server) Configure(raw json.RawMessage) error {
	_ = s.Server.Configure(raw)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if inner, ok := top["ansible"]; ok {
		top = nil
		if err := json.Unmarshal(inner, &top); err != nil {
			return err
		}
	}
	var cfg struct {
		Ansible *struct {
			Path    *string `json:"path"`
			UseFQCN *bool   `json:"useFullyQualifiedCollectionNames"`
		} `json:"ansible"`
		Validation *struct {
			Enabled *bool `json:"enabled"`
			Lint    *struct {
				Enabled   *bool           `json:"enabled"`
				Path      *string         `json:"path"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"lint"`
		} `json:"validation"`
	}
	b, _ := json.Marshal(top)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	s.mu.Lock()
	old := s.set.AnsiblePath
	if a := cfg.Ansible; a != nil {
		if a.Path != nil {
			s.set.AnsiblePath = *a.Path
		}
		if a.UseFQCN != nil {
			s.set.FQCN = *a.UseFQCN
		}
	}
	if v := cfg.Validation; v != nil {
		if v.Enabled != nil {
			s.set.Validation = *v.Enabled
		}
		if l := v.Lint; l != nil {
			if l.Enabled != nil && !*l.Enabled {
				s.set.Validation = false
			}
			if l.Path != nil {
				s.set.LintPath = *l.Path
			}
			var list []string
			var str string
			if json.Unmarshal(l.Arguments, &list) == nil {
				s.set.LintArgs = list
			} else if json.Unmarshal(l.Arguments, &str) == nil {
				s.set.LintArgs = strings.Fields(str)
			}
		}
	}
	changed := s.set.AnsiblePath != old
	s.mu.Unlock()
	if changed && s.gen.Load() > 0 {
		s.reindex()
	}
	return nil
}

// --- schema selection ---

var kindRules = []struct {
	re     *regexp.Regexp
	schema string
}{
	{regexp.MustCompile(`(^|/)meta/main\.ya?ml$`), "meta.json"},
	{regexp.MustCompile(`(^|/)meta/runtime\.ya?ml$`), "meta-runtime.json"},
	{regexp.MustCompile(`(^|/)meta/argument_specs\.ya?ml$`), "role-arg-spec.json"},
	{regexp.MustCompile(`(^|/)requirements\.ya?ml$`), "requirements.json"},
	{regexp.MustCompile(`(^|/)galaxy\.ya?ml$`), "galaxy.json"},
	{regexp.MustCompile(`(^|/)execution-environment\.ya?ml$`), "execution-environment.json"},
	{regexp.MustCompile(`(^|/)(vars|defaults|host_vars|group_vars)/`), "vars.json"},
	{regexp.MustCompile(`(^|/)(tasks|handlers)/[^/]+\.ya?ml$`), "ansible.json#/$defs/tasks"},
	{regexp.MustCompile(`(^|/)inventory[^/]*\.ya?ml$|(^|/)(hosts|inventory)/[^/]+\.ya?ml$`), "inventory.json"},
}

// fileKind returns the ansible-lint schema file for a document: by path, then by content
// (a list of plays is a playbook, another list is a task list).
func fileKind(path string, root *tree.Node) string {
	p := filepath.ToSlash(path)
	for _, r := range kindRules {
		if r.re.MatchString(p) {
			return r.schema
		}
	}
	if root != nil && root.Kind == tree.Array {
		for _, it := range root.Items {
			if it.Prop("hosts") != nil || it.Prop("import_playbook") != nil || it.Prop("ansible.builtin.import_playbook") != nil {
				return "ansible.json#/$defs/playbook"
			}
		}
		if len(root.Items) > 0 {
			return "ansible.json#/$defs/tasks"
		}
	}
	return ""
}

func schemaFor(doc *document.Doc, root *tree.Node) string {
	if k := fileKind(doc.Path(), root); k != "" {
		return SchemaBase + k
	}
	return ""
}

// --- task structure ---

// blockKeys hold task lists.
var blockKeys = map[string]bool{"tasks": true, "pre_tasks": true, "post_tasks": true, "handlers": true,
	"block": true, "rescue": true, "always": true}

// taskKeywords are task keys that are not modules.
var taskKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`name action args async become become_exe become_flags become_method
become_user changed_when check_mode collections connection debugger delay delegate_facts delegate_to diff
environment failed_when ignore_errors ignore_unreachable local_action loop loop_control module_defaults
no_log notify poll port register remote_user retries run_once tags throttle timeout until vars when
block rescue always listen any_errors_fatal`) {
		taskKeywords[k] = true
	}
}

func isTaskKeyword(k string) bool { return taskKeywords[k] || strings.HasPrefix(k, "with_") }

// isTaskPath reports whether path points at a task (an item of a task list). tasksFile says
// whether the document itself is a task list.
func isTaskPath(path tree.Path, tasksFile bool) bool {
	if len(path) == 0 || !path[len(path)-1].IsIndex {
		return false
	}
	if len(path) == 1 {
		return tasksFile
	}
	prev := path[len(path)-2]
	return !prev.IsIndex && blockKeys[prev.Key]
}

// --- completion hook ---

func (s *Server) names(path tree.Path, isKey bool, lines []string, ln int) []yaml.Name {
	ix := s.idx()
	tasksFile := looksLikeTasks(lines)
	switch {
	case isKey && isTaskPath(path, tasksFile):
		return s.moduleNames(ix)
	case isKey && len(path) >= 2 && isTaskPath(path[:len(path)-1], tasksFile):
		// Options of the module at path's last key.
		if d := ix.Doc(ix.Resolve(path[len(path)-1].Key)); d != nil {
			var out []yaml.Name
			for name, o := range d.Options {
				out = append(out, yaml.Name{Label: name, Detail: typeOf(o), Doc: o.Markdown(name)})
			}
			return out
		}
	case !isKey && len(path) >= 3 && isTaskPath(path[:len(path)-2], tasksFile):
		if d := ix.Doc(ix.Resolve(path[len(path)-2].Key)); d != nil {
			o, ok := d.Options[path[len(path)-1].Key]
			if !ok {
				return nil
			}
			var out []yaml.Name
			for _, c := range o.Choices {
				out = append(out, yaml.Name{Label: fmt.Sprint(c)})
			}
			if o.Type == "bool" && len(o.Choices) == 0 {
				out = append(out, yaml.Name{Label: "true"}, yaml.Name{Label: "false"})
			}
			return out
		}
	case !isKey && len(path) >= 1 && (path[len(path)-1].Key == "role" || (len(path) >= 2 && path[len(path)-2].Key == "roles")):
		return s.roleNames(lines)
	}
	return nil
}

func (s *Server) moduleNames(ix *Index) []yaml.Name {
	fqcn := s.settings().FQCN
	var out []yaml.Name
	for _, n := range ix.Names() {
		label := n
		if !fqcn && strings.HasPrefix(n, "ansible.builtin.") {
			label = strings.TrimPrefix(n, "ansible.builtin.")
		}
		item := yaml.Name{Label: label, Detail: "module"}
		// Parsing every module would be slow; builtin docs are cheap enough and most used.
		if strings.HasPrefix(n, "ansible.builtin.") {
			if d := ix.Doc(n); d != nil {
				item.Detail = d.ShortDescription
			}
		}
		out = append(out, item)
	}
	return out
}

// looksLikeTasks guesses from the text whether a document is a task list (not a playbook).
func looksLikeTasks(lines []string) bool {
	top := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || t == "---" {
			continue
		}
		if strings.HasPrefix(l, "- ") || l == "-" {
			top = true
			if strings.HasPrefix(t, "- hosts:") || strings.HasPrefix(t, "- import_playbook:") || strings.HasPrefix(t, "- ansible.builtin.import_playbook:") {
				return false
			}
			continue
		}
		if !strings.HasPrefix(l, " ") && strings.HasPrefix(strings.TrimSpace(l), "hosts:") {
			return false
		}
		if len(l) > 2 && l[0] == ' ' && l[1] == ' ' && strings.HasPrefix(strings.TrimSpace(l), "hosts:") {
			return false
		}
	}
	return top
}

// roleNames lists role directories next to the playbook and in the default role paths.
func (s *Server) roleNames(_ []string) []yaml.Name {
	var out []yaml.Name
	seen := map[string]bool{}
	for _, dir := range s.roleDirs("") {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() && !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, yaml.Name{Label: e.Name(), Detail: "role in " + dir})
			}
		}
	}
	return out
}

// roleDirs returns the directories searched for roles.
func (s *Server) roleDirs(docDir string) []string {
	var dirs []string
	if docDir != "" {
		dirs = append(dirs, filepath.Join(docDir, "roles"), filepath.Join(filepath.Dir(docDir), "roles"),
			filepath.Dir(docDir))
	}
	if s.Env.RootPath != "" {
		dirs = append(dirs, filepath.Join(s.Env.RootPath, "roles"))
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("ANSIBLE_ROLES_PATH"))...)
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".ansible", "roles"))
	}
	dirs = append(dirs, "/usr/share/ansible/roles", "/etc/ansible/roles")
	return uniqueDirs(dirs)
}

// --- diagnostics ---

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	out := s.Server.Diagnose(ctx, doc)
	ix := s.idx()
	if ix.Len() > 0 {
		res := yamlast.Parse(doc.Text)
		for _, d := range res.Docs {
			out = append(out, unknownModules(doc, d.Root, ix)...)
		}
	}
	s.lintOnce(doc)
	s.mu.Lock()
	lint := s.lint[doc.URI]
	s.mu.Unlock()
	// ansible-lint's syntax check reports unresolved modules too; keep only its message.
	lintLines := map[uint32]bool{}
	for _, d := range lint {
		if strings.Contains(d.Message, "resolve module") {
			lintLines[d.Range.Start.Line] = true
		}
	}
	kept := out[:0]
	for _, d := range out {
		if !(lintLines[d.Range.Start.Line] && strings.HasPrefix(d.Message, "Couldn't resolve module")) {
			kept = append(kept, d)
		}
	}
	return append(kept, lint...)
}

// unknownModules reports task keys that are neither keywords nor installed modules. Keys of
// collections that aren't installed are left alone (they may exist on the control node).
func unknownModules(doc *document.Doc, root *tree.Node, ix *Index) []protocol.Diagnostic {
	if root == nil || root.Kind != tree.Array {
		return nil
	}
	var out []protocol.Diagnostic
	var walkTasks func(tasks *tree.Node)
	checkTask := func(task *tree.Node) {
		for _, p := range task.Props {
			k := p.Key
			if isTaskKeyword(k) {
				if blockKeys[k] && p.Value != nil {
					walkTasks(p.Value)
				}
				continue
			}
			if fqcn := ix.Resolve(k); fqcn != "" {
				out = append(out, checkOptions(doc, ix.Doc(fqcn), p, task)...)
				continue
			}
			parts := strings.Split(k, ".")
			if len(parts) >= 3 && !ix.HasCollection(parts[0]+"."+parts[1]) {
				continue
			}
			if len(parts) == 2 {
				continue // not a module name shape
			}
			out = append(out, schemalang.Diagnostic(doc, p.KeyStart, p.KeyEnd, protocol.SeverityWarning,
				fmt.Sprintf("Couldn't resolve module/action %q.", k)))
		}
	}
	walkTasks = func(tasks *tree.Node) {
		if tasks == nil || tasks.Kind != tree.Array {
			return
		}
		for _, t := range tasks.Items {
			if t.Kind == tree.Object {
				checkTask(t)
			}
		}
	}
	isPlaybook := false
	for _, it := range root.Items {
		if it.Prop("hosts") != nil {
			isPlaybook = true
			for _, k := range []string{"tasks", "pre_tasks", "post_tasks", "handlers"} {
				if p := it.Prop(k); p != nil {
					walkTasks(p.Value)
				}
			}
		}
	}
	if !isPlaybook && fileKind(doc.Path(), root) == "ansible.json#/$defs/tasks" {
		walkTasks(root)
	}
	return out
}

// checkOptions validates a module call against its documentation: choices, unknown options (only
// when the docs are complete, i.e. no documentation fragments add options) and required options.
func checkOptions(doc *document.Doc, d *ModuleDoc, call *tree.Prop, task *tree.Node) []protocol.Diagnostic {
	if d == nil || call.Value == nil || call.Value.Kind != tree.Object {
		return nil // free-form call ("command: ls") or unparsable docs
	}
	args := append([]*tree.Prop{}, call.Value.Props...)
	if a := task.Prop("args"); a != nil && a.Value != nil && a.Value.Kind == tree.Object {
		args = append(args, a.Value.Props...)
	}
	byName := map[string]string{} // name or alias -> option name
	for name, o := range d.Options {
		byName[name] = name
		for _, al := range o.Aliases {
			byName[al] = name
		}
	}
	var out []protocol.Diagnostic
	present := map[string]bool{}
	for _, a := range args {
		name, known := byName[a.Key]
		if !known {
			if d.Fragments == nil && !strings.HasPrefix(a.Key, "_") {
				out = append(out, schemalang.Diagnostic(doc, a.KeyStart, a.KeyEnd, protocol.SeverityWarning,
					fmt.Sprintf("Unsupported parameter %q for module %s.", a.Key, d.FQCN)))
			}
			continue
		}
		present[name] = true
		o := d.Options[name]
		if len(o.Choices) == 0 || a.Value == nil {
			continue
		}
		if s, ok := scalarText(a.Value); ok && !strings.Contains(s, "{{") && !inChoices(s, o.Choices) {
			vals := make([]string, len(o.Choices))
			for i, c := range o.Choices {
				vals[i] = fmt.Sprint(c)
			}
			out = append(out, schemalang.Diagnostic(doc, a.Value.Start, a.Value.End, protocol.SeverityWarning,
				fmt.Sprintf("Value %q is not one of: %s.", s, strings.Join(vals, ", "))))
		}
	}
	var missing []string
	for name, o := range d.Options {
		if o.Required() && !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		out = append(out, schemalang.Diagnostic(doc, call.KeyStart, call.KeyEnd, protocol.SeverityWarning,
			fmt.Sprintf("Missing required parameter(s) of %s: %s.", d.FQCN, strings.Join(missing, ", "))))
	}
	return out
}

func scalarText(n *tree.Node) (string, bool) {
	switch n.Kind {
	case tree.String, tree.Number, tree.Bool:
		return fmt.Sprint(n.Value), true
	}
	return "", false
}

// inChoices compares like Ansible does for documentation purposes: as strings, with YAML 1.1
// booleans normalized.
func inChoices(v string, choices []any) bool {
	norm := func(s string) string {
		switch strings.ToLower(s) {
		case "yes", "true", "on":
			return "true"
		case "no", "false", "off":
			return "false"
		}
		return s
	}
	for _, c := range choices {
		if norm(fmt.Sprint(c)) == norm(v) {
			return true
		}
	}
	return false
}

// --- hover ---

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	off := doc.Offset(pos)
	root := yaml.DocAt(doc.Text, off)
	if root != nil {
		if loc, ok := root.At(off); ok && loc.OnKey {
			ix := s.idx()
			tasksFile := root.Kind == tree.Array && fileKind(doc.Path(), root) == "ansible.json#/$defs/tasks"
			path := loc.Path
			md := ""
			switch {
			case len(path) >= 2 && isTaskPath(path[:len(path)-1], tasksFile) && !isTaskKeyword(loc.Prop.Key):
				if d := ix.Doc(ix.Resolve(loc.Prop.Key)); d != nil {
					md = d.Markdown()
				}
			case len(path) >= 3 && isTaskPath(path[:len(path)-2], tasksFile):
				if d := ix.Doc(ix.Resolve(path[len(path)-2].Key)); d != nil {
					if o, ok := d.Options[loc.Prop.Key]; ok {
						md = o.Markdown(loc.Prop.Key)
					}
				}
			}
			if md != "" {
				r := doc.Range(loc.Prop.KeyStart, loc.Prop.KeyEnd)
				return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
			}
		}
	}
	return s.Server.Hover(ctx, doc, pos)
}
