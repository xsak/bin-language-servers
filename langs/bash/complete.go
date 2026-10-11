package bash

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/extproc"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

var builtins = strings.Fields(`alias bg bind break builtin caller cd command compgen complete compopt
continue declare dirs disown echo enable eval exec exit export false fc fg getopts hash help history
jobs kill let local logout mapfile popd printf pushd pwd read readarray readonly return set shift shopt
source suspend test times trap true type typeset ulimit umask unalias unset wait`)

var keywords = strings.Fields(`if then else elif fi case esac for select while until do done in
function time coproc`)

// keywordSnippets are offered for the compound commands.
var keywordSnippets = map[string]string{
	"if":       "if ${1:condition}; then\n\t$0\nfi",
	"for":      "for ${1:item} in ${2:list}; do\n\t$0\ndone",
	"while":    "while ${1:condition}; do\n\t$0\ndone",
	"until":    "until ${1:condition}; do\n\t$0\ndone",
	"case":     "case ${1:word} in\n\t${2:pattern})\n\t\t$0\n\t\t;;\nesac",
	"function": "${1:name}() {\n\t$0\n}",
	"select":   "select ${1:item} in ${2:list}; do\n\t$0\ndone",
}

func isBuiltin(w string) bool { return contains(builtins, w) }
func isKeyword(w string) bool { return contains(keywords, w) || w == "[[" || w == "]]" }

func contains(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

func isNameChar(c byte) bool {
	return c == '_' || c == '-' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// wordAt returns the shell word around off (letters, digits, _ - .).
func wordAt(text string, off int) (string, int, int) {
	start, end := off, off
	for start > 0 && isNameChar(text[start-1]) {
		start--
	}
	for end < len(text) && isNameChar(text[end]) {
		end++
	}
	return text[start:end], start, end
}

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	o := doc.Offset(pos)
	start := o
	for start > 0 && isNameChar(doc.Text[start-1]) && doc.Text[start-1] != '-' && doc.Text[start-1] != '.' {
		start--
	}
	prefix := doc.Text[start:o]
	a := s.analyze(doc)
	r := doc.Range(start, o)
	item := func(label string, kind protocol.CompletionItemKind, detail string) protocol.CompletionItem {
		return protocol.CompletionItem{Label: label, Kind: kind, Detail: detail,
			TextEdit: &protocol.TextEdit{Range: r, NewText: label}}
	}

	// Variables after $ or ${.
	if start > 0 && (doc.Text[start-1] == '$' || (start > 1 && doc.Text[start-2:start] == "${")) {
		seen := map[string]bool{}
		var items []protocol.CompletionItem
		addVar := func(name, detail string) {
			if !seen[name] && strings.HasPrefix(name, prefix) {
				seen[name] = true
				items = append(items, item(name, protocol.CompletionKindVariable, detail))
			}
		}
		for name := range a.varDef {
			addVar(name, "variable")
		}
		for _, f := range s.sourced(doc, a) {
			for name := range f.a.varDef {
				addVar(name, "from "+filepath.Base(f.path))
			}
		}
		for _, kv := range os.Environ() {
			if name, _, ok := strings.Cut(kv, "="); ok && validName(name) {
				addVar(name, "environment")
			}
		}
		for _, sp := range []string{"HOME", "PATH", "PWD", "OLDPWD", "IFS", "RANDOM", "LINENO", "SECONDS",
			"BASH_SOURCE", "FUNCNAME", "PIPESTATUS", "BASH_REMATCH", "EUID", "UID", "HOSTNAME"} {
			addVar(sp, "shell variable")
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
		return &protocol.CompletionList{Items: items}, nil
	}

	var items []protocol.CompletionItem
	seen := map[string]bool{}
	addItem := func(it protocol.CompletionItem) {
		if !seen[it.Label] && strings.HasPrefix(it.Label, prefix) {
			seen[it.Label] = true
			items = append(items, it)
		}
	}
	for name := range a.funcDef {
		addItem(item(name, protocol.CompletionKindFunction, "function"))
	}
	for _, f := range s.sourced(doc, a) {
		for name := range f.a.funcDef {
			addItem(item(name, protocol.CompletionKindFunction, "function from "+filepath.Base(f.path)))
		}
	}
	for _, k := range keywords {
		it := item(k, protocol.CompletionKindKeyword, "keyword")
		if snip, ok := keywordSnippets[k]; ok && s.Env.SnippetSupport {
			it.TextEdit.NewText = snip
			it.InsertTextFormat = protocol.InsertTextSnippet
			it.InsertTextMode = protocol.InsertTextAdjustIndentation
		}
		addItem(it)
	}
	for _, b := range builtins {
		addItem(item(b, protocol.CompletionKindFunction, "builtin"))
	}
	incomplete := false
	if prefix != "" {
		// Commands on PATH; the list is long, so only matching names are sent.
		for _, c := range pathCommands() {
			if strings.HasPrefix(c, prefix) {
				addItem(item(c, protocol.CompletionKindModule, "command"))
			}
		}
	} else {
		incomplete = true
	}
	for i := range items {
		items[i].SortText = fmt.Sprintf("%d%s", sortRank(items[i].Kind), items[i].Label)
	}
	return &protocol.CompletionList{IsIncomplete: incomplete, Items: items}, nil
}

func sortRank(k protocol.CompletionItemKind) int {
	switch k {
	case protocol.CompletionKindVariable:
		return 0
	case protocol.CompletionKindFunction:
		return 1
	case protocol.CompletionKindKeyword:
		return 2
	}
	return 3
}

var (
	pathOnce sync.Once
	pathCmds []string
)

// pathCommands lists the executables on PATH once per process.
func pathCommands() []string {
	pathOnce.Do(func() {
		seen := map[string]bool{}
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if seen[e.Name()] || e.IsDir() {
					continue
				}
				if info, err := e.Info(); err == nil && info.Mode()&0o111 != 0 {
					seen[e.Name()] = true
					pathCmds = append(pathCmds, e.Name())
				}
			}
		}
		sort.Strings(pathCmds)
	})
	return pathCmds
}

// --- hover ---

var (
	helpMu    sync.Mutex
	helpCache = map[string]string{}
)

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	o := doc.Offset(pos)
	a := s.analyze(doc)
	md := ""
	start, end := 0, 0

	if name, isVar, ok := a.symbolAt(o); ok {
		md = s.describeSymbol(doc, a, name, isVar)
		_, start, end = wordAt(doc.Text, o)
	}
	if md == "" {
		w, ws, we := wordAt(doc.Text, o)
		if w == "" {
			return nil, nil
		}
		start, end = ws, we
		switch {
		case isBuiltin(w) || isKeyword(w):
			md = s.builtinHelp(ctx, w)
		case len(a.calls[w]) > 0:
			md = s.commandSummary(ctx, w)
		}
	}
	if md == "" {
		return nil, nil
	}
	r := doc.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
}

// describeSymbol shows the definition line and the comment above it.
func (s *Server) describeSymbol(doc *document.Doc, a *analysis, name string, isVar bool) string {
	find := func(an *analysis) []span {
		if isVar {
			return an.varDef[name]
		}
		return an.funcDef[name]
	}
	text, where := doc.Text, ""
	spans := find(a)
	if len(spans) == 0 {
		for _, f := range s.sourced(doc, a) {
			if sp := find(f.a); len(sp) > 0 {
				spans, text, where = sp, f.text, filepath.Base(f.path)
				break
			}
		}
	}
	if len(spans) == 0 {
		if isVar {
			if v, ok := os.LookupEnv(name); ok {
				return fmt.Sprintf("```bash\n%s=%s\n```\n\n(environment)", name, truncate(v, 200))
			}
			return ""
		}
		return ""
	}
	sp := spans[0]
	line := strings.TrimSpace(lineAt(text, sp.start))
	var b strings.Builder
	fmt.Fprintf(&b, "```bash\n%s\n```", truncate(line, 300))
	if c := commentBefore(text, sp.start); c != "" {
		b.WriteString("\n\n" + c)
	}
	if where != "" {
		b.WriteString("\n\nDefined in `" + where + "`")
	}
	return b.String()
}

// builtinHelp returns `help name` from bash, cached.
func (s *Server) builtinHelp(ctx context.Context, name string) string {
	return cached("help:"+name, func() string {
		bash := extproc.Find("bash")
		if bash == "" {
			return ""
		}
		res, err := extproc.Run(ctx, "", "", bash, "-c", `help -- "$1"`, "bls", name)
		if err != nil || res.ExitCode != 0 {
			return ""
		}
		return "```text\n" + strings.TrimSpace(firstLines(string(res.Stdout), 40)) + "\n```"
	})
}

// commandSummary returns the whatis line for an external command, cached.
func (s *Server) commandSummary(ctx context.Context, name string) string {
	return cached("cmd:"+name, func() string {
		whatis := extproc.Find("whatis")
		if whatis == "" || extproc.Find(name) == "" {
			return ""
		}
		res, err := extproc.Run(ctx, "", "", whatis, "--", name)
		if err != nil || res.ExitCode != 0 {
			return "`" + name + "` (" + extproc.Find(name) + ")"
		}
		return "**" + name + "**: " + strings.TrimSpace(firstLines(string(res.Stdout), 3)) + "\n\n`" + extproc.Find(name) + "`"
	})
}

func cached(key string, fn func() string) string {
	helpMu.Lock()
	v, ok := helpCache[key]
	helpMu.Unlock()
	if ok {
		return v
	}
	v = fn()
	helpMu.Lock()
	helpCache[key] = v
	helpMu.Unlock()
	return v
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
