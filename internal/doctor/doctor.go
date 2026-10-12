// Package doctor implements `bls doctor`: it checks the bls installation, the optional external
// tools, the schema cache and the Gram/Zed editor configuration, and explains what to fix.
package doctor

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/xsak/bin-language-servers/internal/jsonast"
	"github.com/xsak/bin-language-servers/internal/tree"
)

// Options are the inputs of a run.
type Options struct {
	Version   string
	Languages []string // compiled-in subcommands
	Home      string   // defaults to $HOME
	Self      string   // path of the running bls binary
}

// serverModes maps editor server names to the bls subcommand that replaces them.
var serverModes = map[string]string{
	"json-language-server": "json", "yaml-language-server": "yaml", "bash-language-server": "bash",
	"docker-language-server": "docker", "dockerfile-language-server": "dockerfile", "docker-compose": "compose",
	"ansible-language-server": "ansible", "vscode-html-language-server": "html", "superhtml": "html",
	"vscode-css-language-server": "css", "nginx": "nginx",
}

// nodeServers are servers that need Node.js when they are not replaced.
var nodeServers = []string{"json-language-server", "yaml-language-server", "bash-language-server",
	"vscode-html-language-server", "vscode-css-language-server", "dockerfile-language-server", "docker-compose",
	"ansible-language-server", "basedpyright", "pyright", "vtsls", "typescript-language-server", "eslint",
	"tailwindcss-language-server", "emmet-language-server", "docker-compose-language-service"}

type report struct {
	w      io.Writer
	errors int
	warns  int
}

func (r *report) section(title string) { fmt.Fprintf(r.w, "\n%s\n", title) }
func (r *report) ok(format string, a ...any) {
	fmt.Fprintf(r.w, "  ✓ %s\n", fmt.Sprintf(format, a...))
}
func (r *report) warn(format string, a ...any) {
	r.warns++
	fmt.Fprintf(r.w, "  ! %s\n", fmt.Sprintf(format, a...))
}
func (r *report) fail(format string, a ...any) {
	r.errors++
	fmt.Fprintf(r.w, "  ✗ %s\n", fmt.Sprintf(format, a...))
}
func (r *report) info(format string, a ...any) {
	fmt.Fprintf(r.w, "    %s\n", fmt.Sprintf(format, a...))
}

// Run prints the report and returns the exit code (1 if something is broken).
func Run(w io.Writer, opts Options) int {
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	r := &report{w: w}
	fmt.Fprintf(w, "bls doctor (bls %s)\n", opts.Version)

	r.section("Installation")
	if opts.Self != "" {
		r.ok("binary: %s", opts.Self)
	}
	r.ok("languages: %s", strings.Join(opts.Languages, ", "))
	if p, err := exec.LookPath("bls"); err == nil {
		r.ok("bls on PATH: %s", p)
	} else {
		r.warn("bls is not on PATH; editors need an absolute path in their settings (make install puts it in ~/.local/bin)")
	}

	r.section("Optional tools")
	tool := func(name, purpose string) {
		if p, err := exec.LookPath(name); err == nil {
			r.ok("%s: %s (%s)", name, p, purpose)
		} else {
			r.info("- %s not found (%s)", name, purpose)
		}
	}
	tool("shellcheck", "bash diagnostics")
	tool("hadolint", "extra Dockerfile checks")
	tool("bash", "hover help for builtins")
	tool("whatis", "hover summaries for commands")
	tool("ansible-lint", "Ansible lint on open/save")
	if dirs := ansibleCore(opts.Home); len(dirs) > 0 {
		r.ok("ansible-core modules: %s", strings.Join(dirs, ", "))
	} else {
		r.info("- ansible-core not found (Ansible module completion needs an installed ansible-core)")
	}

	r.section("Schema cache")
	cache := filepath.Join(cacheDir(opts.Home), "bls", "schemas")
	if n, size := dirStats(cache); n > 0 {
		r.ok("%s: %d files, %.1f MB", cache, n, float64(size)/1e6)
	} else {
		r.info("- %s is empty (schemas are downloaded on first use)", cache)
	}
	if os.Getenv("BLS_OFFLINE") != "" {
		r.warn("BLS_OFFLINE is set: schemas are never downloaded")
	}

	langs := map[string]bool{}
	for _, l := range opts.Languages {
		langs[l] = true
	}
	editor(r, "Gram", filepath.Join(configDir(opts.Home), "gram", "settings.jsonc"),
		filepath.Join(dataDir(opts.Home), "gram"), langs, false)
	editor(r, "Zed", filepath.Join(configDir(opts.Home), "zed", "settings.json"),
		filepath.Join(dataDir(opts.Home), "zed"), langs, true)

	fmt.Fprintf(w, "\n%d problem(s), %d warning(s)\n", r.errors, r.warns)
	if r.errors > 0 {
		return 1
	}
	return 0
}

func editor(r *report, name, settingsPath, dataPath string, langs map[string]bool, isZed bool) {
	settings, err := readJSONC(settingsPath)
	if os.IsNotExist(err) {
		return
	}
	r.section(name + " (" + settingsPath + ")")
	if err != nil {
		r.fail("can't read settings: %v", err)
		return
	}
	lsp := object(settings["lsp"])
	pointed := map[string]bool{}
	names := make([]string, 0, len(lsp))
	for n := range lsp {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, server := range names {
		cfg := object(lsp[server])
		bin := object(cfg["binary"])
		path, _ := bin["path"].(string)
		if path == "" {
			continue
		}
		args, _ := bin["arguments"].([]any)
		mode := ""
		if len(args) > 0 {
			mode, _ = args[0].(string)
		}
		name := strings.TrimSuffix(path, ".exe")
		if !strings.Contains(filepath.Base(path), "bls") && !strings.HasSuffix(name, "-ls") {
			continue
		}
		pointed[server] = true
		switch {
		case !isExecutable(path):
			r.fail("%s → %s: file not found or not executable", server, path)
		case mode == "" && !strings.HasSuffix(name, "-ls"):
			r.fail("%s → %s: missing the bls subcommand in \"arguments\"", server, path)
		case mode != "" && !langs[mode]:
			r.fail("%s → bls %s: this bls has no %q language (see bls list)", server, mode, mode)
		default:
			want := serverModes[server]
			if want != "" && mode != want && !(server == "docker-language-server" && mode == "compose") {
				r.warn("%s → bls %s: usually bls %s", server, mode, want)
			} else {
				r.ok("%s → bls %s", server, mode)
			}
		}
	}

	ext := installedExtensions(dataPath)
	if len(ext) > 0 {
		has := func(e string) bool { return ext[e] }
		if has("bls") {
			r.ok("bls extension installed (INI, TOML, Apache)")
		} else {
			r.info("- bls extension not installed: INI, TOML and Apache files get no server (scripts/zed-extension.sh install)")
		}
		for e, server := range map[string]string{"nginx": "nginx", "ansible": "ansible-language-server",
			"dockerfile": "docker-language-server", "docker-compose": "docker-compose", "html": "vscode-html-language-server"} {
			if has(e) && !pointed[server] && server != "vscode-html-language-server" {
				r.warn("extension %q is installed but its server %q isn't pointed at bls", e, server)
			}
		}
	}

	if isZed {
		var unreplaced []string
		for _, s := range nodeServers {
			if !pointed[s] {
				unreplaced = append(unreplaced, s)
			}
		}
		if node := filepath.Join(dataPath, "node"); dirExists(node) {
			r.warn("Zed has downloaded Node.js into %s; delete it after replacing or disabling the Node-based servers", node)
		}
		prettier := object(settings["prettier"])
		if allowed, ok := prettier["allowed"].(bool); !ok || allowed {
			r.warn("Prettier (Node) is allowed; set \"prettier\": { \"allowed\": false }")
		} else {
			r.ok("Prettier disabled")
		}
		if len(unreplaced) > 0 {
			r.info("Node-based servers not pointed at bls (fine if the language is unused or disabled): %s", strings.Join(unreplaced, ", "))
		}
	} else {
		for server, mode := range serverModes {
			if !pointed[server] && langs[mode] && (server == "json-language-server" || server == "yaml-language-server" || server == "bash-language-server") {
				r.warn("%s is not pointed at bls; Gram can't download it, so that language has no server", server)
			}
		}
	}

	logFile := filepath.Join(dataPath, "logs", map[bool]string{true: "Zed.log", false: "Gram.log"}[isZed])
	if lines := logProblems(logFile); len(lines) > 0 {
		r.info("language server errors near the end of %s (may predate your current settings):", logFile)
		for _, l := range lines {
			r.info("  %s", l)
		}
	}
}

// readJSONC reads a JSON-with-comments file into a map.
func readJSONC(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	res := jsonast.Parse(string(data), jsonast.Options{Comments: true})
	if res.Root == nil || res.Root.Kind != tree.Object {
		return map[string]any{}, nil
	}
	for _, p := range res.Problems {
		if !p.Warning {
			return nil, fmt.Errorf("syntax error: %s", p.Message)
		}
	}
	m, _ := res.Root.ToValue().(map[string]any)
	return m, nil
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func isExecutable(p string) bool {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // permission bits carry no executable meaning on Windows
	}
	return fi.Mode()&0o111 != 0
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func installedExtensions(dataPath string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(dataPath, "extensions", "installed"))
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// logProblems returns distinct language-server failure lines from the end of the log (older
// entries usually predate the current settings).
func logProblems(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const tail = 3000
	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > tail {
			ring = ring[1:]
		}
	}
	var keep []string
	seen := map[string]bool{}
	for _, l := range ring {
		low := strings.ToLower(l)
		if !strings.Contains(low, "language server") {
			continue
		}
		bad := false
		for _, p := range []string{"download disabled", "failed", "error:", "not found", "no compatible node", "exited"} {
			if strings.Contains(low, p) {
				bad = true
			}
		}
		if !bad {
			continue
		}
		msg := l
		if i := strings.Index(l, "language server"); i >= 0 {
			msg = l[i:]
		}
		if len(msg) > 140 {
			msg = msg[:137] + "..."
		}
		if !seen[msg] {
			seen[msg] = true
			keep = append(keep, msg)
		}
	}
	if len(keep) > 8 {
		keep = keep[len(keep)-8:]
	}
	return keep
}

func dirStats(dir string) (int, int64) {
	var n int
	var size int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n++
				size += fi.Size()
			}
		}
		return nil
	})
	return n, size
}

// configDir is where editors keep their settings: %APPDATA% on Windows, ~/.config elsewhere.
func configDir(home string) string {
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil && d != "" {
			return d
		}
	}
	return filepath.Join(home, ".config")
}

func cacheDir(home string) string {
	if runtime.GOOS == "windows" {
		if d, err := os.UserCacheDir(); err == nil && d != "" {
			return d
		}
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return d
	}
	return filepath.Join(home, ".cache")
}

// dataDir is where editors keep data (extensions, logs): %APPDATA% on Windows,
// ~/.local/share elsewhere.
func dataDir(home string) string {
	if runtime.GOOS == "windows" {
		return configDir(home)
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	return filepath.Join(home, ".local", "share")
}

// ansibleCore lists ansible-core module directories in the usual places.
func ansibleCore(home string) []string {
	var out []string
	for _, g := range []string{
		"/usr/lib/python3/dist-packages/ansible/modules",
		"/usr/lib/python3*/site-packages/ansible/modules",
		filepath.Join(home, ".local/lib/python3*/site-packages/ansible/modules"),
		filepath.Join(home, ".local/share/pipx/venvs/*/lib/python3*/site-packages/ansible/modules"),
		filepath.Join(home, ".local/share/nvim/mason/packages/*/venv/lib/python3*/site-packages/ansible/modules"),
	} {
		m, _ := filepath.Glob(g)
		out = append(out, m...)
	}
	return out
}
