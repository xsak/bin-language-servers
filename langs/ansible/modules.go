package ansible

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	goyaml "github.com/goccy/go-yaml"
)

// Module documentation is read straight from the module source files: every Ansible module
// carries its documentation as a YAML string (DOCUMENTATION = r"""...""") in its .py file. No
// Python or ansible-doc is needed.

// ModuleDoc is the parsed DOCUMENTATION of a module.
type ModuleDoc struct {
	FQCN             string
	ShortDescription string               `yaml:"short_description"`
	Description      any                  `yaml:"description"` // string or list of strings
	Options          map[string]OptionDoc `yaml:"options"`
	Deprecated       any                  `yaml:"deprecated"`
	Fragments        any                  `yaml:"extends_documentation_fragment"`
}

// OptionDoc documents one module option.
type OptionDoc struct {
	Description any                  `yaml:"description"`
	Type        string               `yaml:"type"`
	RequiredRaw any                  `yaml:"required"` // bool, or "yes"/"no" (YAML 1.1 style)
	Default     any                  `yaml:"default"`
	Choices     []any                `yaml:"choices"`
	Aliases     []string             `yaml:"aliases"`
	Elements    string               `yaml:"elements"`
	Suboptions  map[string]OptionDoc `yaml:"suboptions"`
}

// Required reports whether the option is mandatory.
func (o OptionDoc) Required() bool { return truthy(o.RequiredRaw) }

// truthy accepts YAML 1.1 booleans, which module docs still use.
func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(b) {
		case "yes", "true", "on", "y":
			return true
		}
	}
	return false
}

// text joins a description that is a string or a list of strings and converts Ansible's
// documentation markup to Markdown.
func text(d any) string {
	switch v := d.(type) {
	case string:
		return markup(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, p := range v {
			parts = append(parts, markup(fmt.Sprint(p)))
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

var markupRe = regexp.MustCompile(`\b(RV|[BCEIMOPUVL])\(([^()]*)\)`)

// markup converts Ansible doc markup: C(code), O(option), V(value), I(italic), B(bold),
// M(module), U(url), L(text,url), E(env), P(plugin), RV(return value).
func markup(s string) string {
	s = strings.ReplaceAll(s, "HORIZONTALLINE", "---")
	return markupRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := markupRe.FindStringSubmatch(m)
		arg := sub[2]
		switch sub[1] {
		case "I":
			return "*" + arg + "*"
		case "B":
			return "**" + arg + "**"
		case "U":
			return arg
		case "L":
			if t, u, ok := strings.Cut(arg, ","); ok {
				return "[" + strings.TrimSpace(t) + "](" + strings.TrimSpace(u) + ")"
			}
			return arg
		case "P":
			name, _, _ := strings.Cut(arg, "#")
			return "`" + name + "`"
		}
		return "`" + arg + "`"
	})
}

// Index knows where modules are and parses their documentation on demand.
type Index struct {
	mu    sync.Mutex
	files map[string]string     // FQCN -> .py path
	short map[string]string     // short name -> FQCN (ansible.builtin wins)
	docs  map[string]*ModuleDoc // parsed, by FQCN
	roots []string              // where modules were found (for display)
}

// NewIndex scans the ansible-core modules directories and collection paths.
func NewIndex(coreDirs, collectionPaths []string) *Index {
	ix := &Index{files: map[string]string{}, short: map[string]string{}, docs: map[string]*ModuleDoc{}}
	for _, dir := range coreDirs {
		if ix.addDir(dir, "ansible.builtin") {
			ix.roots = append(ix.roots, dir)
		}
	}
	for _, cp := range collectionPaths {
		base := filepath.Join(cp, "ansible_collections")
		nss, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, ns := range nss {
			if !ns.IsDir() || strings.Contains(ns.Name(), ".") {
				continue
			}
			colls, _ := os.ReadDir(filepath.Join(base, ns.Name()))
			for _, c := range colls {
				dir := filepath.Join(base, ns.Name(), c.Name(), "plugins", "modules")
				if ix.addDir(dir, ns.Name()+"."+c.Name()) {
					ix.roots = append(ix.roots, dir)
				}
			}
		}
	}
	return ix
}

// addDir registers every module file under dir (modules may be in sub-directories).
func (ix *Index) addDir(dir, collection string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".py") || strings.HasPrefix(d.Name(), "_") {
			return nil
		}
		name := strings.TrimSuffix(d.Name(), ".py")
		fqcn := collection + "." + name
		if _, dup := ix.files[fqcn]; dup {
			return nil
		}
		ix.files[fqcn] = path
		if collection == "ansible.builtin" || ix.short[name] == "" {
			ix.short[name] = fqcn
		}
		found = true
		return nil
	})
	return found
}

// Len is the number of known modules.
func (ix *Index) Len() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.files)
}

// Names returns all FQCNs, sorted.
func (ix *Index) Names() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]string, 0, len(ix.files))
	for n := range ix.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Resolve maps a task key (FQCN or short name, "ansible.legacy.x" too) to an FQCN, or "".
func (ix *Index) Resolve(key string) string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	key = strings.Replace(key, "ansible.legacy.", "ansible.builtin.", 1)
	if _, ok := ix.files[key]; ok {
		return key
	}
	return ix.short[key]
}

// HasCollection reports whether any module of the collection (e.g. "community.general") is
// installed, so unknown modules are only reported for collections we can see.
func (ix *Index) HasCollection(coll string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for n := range ix.files {
		if strings.HasPrefix(n, coll+".") {
			return true
		}
	}
	return false
}

// Doc returns the parsed documentation of a module (nil if it can't be read).
func (ix *Index) Doc(fqcn string) *ModuleDoc {
	ix.mu.Lock()
	if d, ok := ix.docs[fqcn]; ok {
		ix.mu.Unlock()
		return d
	}
	path := ix.files[fqcn]
	ix.mu.Unlock()
	if path == "" {
		return nil
	}
	d := parseDoc(path)
	if d != nil {
		d.FQCN = fqcn
	}
	ix.mu.Lock()
	ix.docs[fqcn] = d
	ix.mu.Unlock()
	return d
}

// parseDoc extracts and parses the DOCUMENTATION string of a module file.
func parseDoc(path string) *ModuleDoc {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	src := string(data)
	i := strings.Index(src, "\nDOCUMENTATION")
	if i < 0 {
		if !strings.HasPrefix(src, "DOCUMENTATION") {
			return nil
		}
		i = 0
	}
	rest := src[i:]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return nil
	}
	rest = strings.TrimLeft(rest[eq+1:], " \t")
	rest = strings.TrimLeft(rest, "rRuU")
	var quote string
	for _, q := range []string{`"""`, `'''`} {
		if strings.HasPrefix(rest, q) {
			quote = q
		}
	}
	if quote == "" {
		return nil
	}
	rest = rest[len(quote):]
	end := strings.Index(rest, quote)
	if end < 0 {
		return nil
	}
	var d ModuleDoc
	if err := goyaml.Unmarshal([]byte(rest[:end]), &d); err != nil {
		return nil
	}
	return &d
}

// Markdown renders module documentation for hover.
func (d *ModuleDoc) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**", d.FQCN)
	if d.ShortDescription != "" {
		b.WriteString(" — " + d.ShortDescription)
	}
	if d.Deprecated != nil {
		b.WriteString("\n\n*Deprecated.*")
	}
	if desc := text(d.Description); desc != "" {
		b.WriteString("\n\n" + firstParagraphs(desc, 2))
	}
	if len(d.Options) > 0 {
		names := make([]string, 0, len(d.Options))
		for n := range d.Options {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("\n\nOptions:")
		for _, n := range names {
			o := d.Options[n]
			req := ""
			if o.Required() {
				req = ", required"
			}
			fmt.Fprintf(&b, "\n- `%s` (%s%s)", n, typeOf(o), req)
		}
	}
	return b.String()
}

// Markdown renders option documentation for hover and completion.
func (o OptionDoc) Markdown(name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%s", name, typeOf(o))
	if o.Required() {
		b.WriteString(", required")
	}
	b.WriteString(")")
	if desc := text(o.Description); desc != "" {
		b.WriteString("\n\n" + desc)
	}
	if len(o.Choices) > 0 {
		vals := make([]string, len(o.Choices))
		for i, c := range o.Choices {
			vals[i] = fmt.Sprintf("`%v`", c)
		}
		b.WriteString("\n\nChoices: " + strings.Join(vals, ", "))
	}
	if o.Default != nil {
		fmt.Fprintf(&b, "\n\nDefault: `%v`", o.Default)
	}
	if len(o.Aliases) > 0 {
		b.WriteString("\n\nAliases: `" + strings.Join(o.Aliases, "`, `") + "`")
	}
	return b.String()
}

func typeOf(o OptionDoc) string {
	t := o.Type
	if t == "" {
		t = "str"
	}
	if o.Elements != "" {
		t += " of " + o.Elements
	}
	return t
}

func firstParagraphs(s string, n int) string {
	parts := strings.SplitN(s, "\n\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n\n")
}

// --- discovery ---

// coreModuleDirs finds ansible-core's modules directory: next to a configured or PATH
// `ansible` executable (venvs, pipx), then in common install locations.
func coreModuleDirs(ansiblePath string) []string {
	var cands []string
	addFromExe := func(exe string) {
		if exe == "" {
			return
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		prefix := filepath.Dir(filepath.Dir(exe))
		m, _ := filepath.Glob(filepath.Join(prefix, "lib", "python3*", "site-packages", "ansible", "modules"))
		cands = append(cands, m...)
	}
	addFromExe(lookPath(ansiblePath))
	addFromExe(lookPath("ansible"))
	home, _ := os.UserHomeDir()
	for _, g := range []string{
		"/usr/lib/python3/dist-packages/ansible/modules",
		"/usr/lib/python3*/site-packages/ansible/modules",
		"/usr/local/lib/python3*/dist-packages/ansible/modules",
		"/usr/local/lib/python3*/site-packages/ansible/modules",
		filepath.Join(home, ".local/lib/python3*/site-packages/ansible/modules"),
		filepath.Join(home, ".local/pipx/venvs/*/lib/python3*/site-packages/ansible/modules"),
		filepath.Join(home, ".local/share/pipx/venvs/*/lib/python3*/site-packages/ansible/modules"),
		filepath.Join(home, ".local/share/nvim/mason/packages/*/venv/lib/python3*/site-packages/ansible/modules"),
	} {
		m, _ := filepath.Glob(g)
		cands = append(cands, m...)
	}
	return uniqueDirs(cands)
}

// collectionPaths follows Ansible's defaults: ANSIBLE_COLLECTIONS_PATH(S), ~/.ansible/collections,
// /usr/share/ansible/collections, plus ansible_collections bundled next to ansible-core.
func collectionPaths(coreDirs []string) []string {
	var cands []string
	for _, env := range []string{"ANSIBLE_COLLECTIONS_PATH", "ANSIBLE_COLLECTIONS_PATHS"} {
		cands = append(cands, filepath.SplitList(os.Getenv(env))...)
	}
	home, _ := os.UserHomeDir()
	cands = append(cands, filepath.Join(home, ".ansible", "collections"), "/usr/share/ansible/collections")
	for _, d := range coreDirs {
		// .../site-packages/ansible/modules -> .../site-packages (contains ansible_collections)
		cands = append(cands, filepath.Dir(filepath.Dir(d)))
	}
	return uniqueDirs(cands)
}

func uniqueDirs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range in {
		if d == "" || seen[d] {
			continue
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
