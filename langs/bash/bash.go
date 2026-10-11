// Package bash is the shell script language module (bash, sh, mksh, bats; zsh for parsing):
// parse errors and shellcheck diagnostics with quick fixes, shfmt-compatible formatting,
// outline, go-to-definition and references (including `source`d files), hover and completion.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mvdan.cc/editorconfig"
	"mvdan.cc/sh/v3/syntax"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "bash",
		Description: "Shell scripts: parse errors, shellcheck, shfmt formatting, symbols, hover, completion",
		New:         func() lang.Language { return &Server{} },
	})
}

// Settings uses bash-language-server's "bashIde" names so existing configurations work.
type Settings struct {
	ShellcheckPath string   // "" disables shellcheck
	ShellcheckArgs []string // extra arguments, e.g. ["--exclude=SC1091"]
	Shfmt          ShfmtSettings
}

// ShfmtSettings mirror shfmt's flags.
type ShfmtSettings struct {
	IgnoreEditorconfig bool
	LanguageDialect    string // auto, bash, posix, mksh, bats, zsh
	BinaryNextLine     bool
	CaseIndent         bool
	FuncNextLine       bool
	KeepPadding        bool
	SpaceRedirects     bool
	SimplifyCode       bool
}

// Server implements the module.
type Server struct {
	Env lang.Env

	mu   sync.Mutex
	set  Settings
	last map[protocol.DocumentURI]checked // shellcheck results, reused by code actions
}

type checked struct {
	version  int32
	comments []scComment
}

func (s *Server) Name() string { return "bash" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	s.set = Settings{ShellcheckPath: "shellcheck"}
	s.last = map[protocol.DocumentURI]checked{}
	if len(env.InitOptions) > 0 {
		_ = s.Configure(env.InitOptions)
	}
	return nil
}

func (s *Server) settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

func (s *Server) ConfigSections() []string { return []string{"bashIde"} }

func (s *Server) Configure(raw json.RawMessage) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if inner, ok := top["bashIde"]; ok {
		if err := json.Unmarshal(inner, &top); err != nil {
			return err
		}
	}
	set := s.settings()
	if v, ok := top["shellcheckPath"]; ok {
		_ = json.Unmarshal(v, &set.ShellcheckPath)
	}
	if v, ok := top["shellcheckArguments"]; ok {
		var list []string
		var str string
		if json.Unmarshal(v, &list) == nil {
			set.ShellcheckArgs = list
		} else if json.Unmarshal(v, &str) == nil {
			set.ShellcheckArgs = strings.Fields(str)
		}
	}
	if v, ok := top["shfmt"]; ok {
		var f struct {
			IgnoreEditorconfig *bool   `json:"ignoreEditorconfig"`
			LanguageDialect    *string `json:"languageDialect"`
			BinaryNextLine     *bool   `json:"binaryNextLine"`
			CaseIndent         *bool   `json:"caseIndent"`
			FuncNextLine       *bool   `json:"funcNextLine"`
			KeepPadding        *bool   `json:"keepPadding"`
			SpaceRedirects     *bool   `json:"spaceRedirects"`
			SimplifyCode       *bool   `json:"simplifyCode"`
		}
		if err := json.Unmarshal(v, &f); err != nil {
			return err
		}
		setBool := func(dst *bool, src *bool) {
			if src != nil {
				*dst = *src
			}
		}
		setBool(&set.Shfmt.IgnoreEditorconfig, f.IgnoreEditorconfig)
		setBool(&set.Shfmt.BinaryNextLine, f.BinaryNextLine)
		setBool(&set.Shfmt.CaseIndent, f.CaseIndent)
		setBool(&set.Shfmt.FuncNextLine, f.FuncNextLine)
		setBool(&set.Shfmt.KeepPadding, f.KeepPadding)
		setBool(&set.Shfmt.SpaceRedirects, f.SpaceRedirects)
		setBool(&set.Shfmt.SimplifyCode, f.SimplifyCode)
		if f.LanguageDialect != nil {
			set.Shfmt.LanguageDialect = *f.LanguageDialect
		}
	}
	s.mu.Lock()
	s.set = set
	s.mu.Unlock()
	return nil
}

func (s *Server) dialect(doc *document.Doc) dialect {
	return detectDialect(doc.Text, doc.Path(), s.settings().Shfmt.LanguageDialect)
}

func (s *Server) analyze(doc *document.Doc) *analysis {
	return analyze(doc.Text, s.dialect(doc))
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	d := s.dialect(doc)
	a := analyze(doc.Text, d)
	var out []protocol.Diagnostic
	for _, p := range a.problems {
		out = append(out, protocol.Diagnostic{Range: doc.Range(p.start, p.end), Severity: protocol.SeverityError,
			Source: "bls", Message: p.msg})
	}
	if len(a.problems) > 0 {
		// shellcheck would only repeat the parse error.
		return out
	}
	comments := s.shellcheck(ctx, doc, d)
	s.mu.Lock()
	s.last[doc.URI] = checked{version: doc.Version, comments: comments}
	s.mu.Unlock()
	for _, c := range comments {
		out = append(out, c.diagnostic(doc))
	}
	return out
}

func (s *Server) CodeActions(ctx context.Context, doc *document.Doc, rng protocol.Range, _ []protocol.Diagnostic) ([]protocol.CodeAction, error) {
	s.mu.Lock()
	c, ok := s.last[doc.URI]
	s.mu.Unlock()
	if !ok || c.version != doc.Version {
		c = checked{version: doc.Version, comments: s.shellcheck(ctx, doc, s.dialect(doc))}
	}
	return codeActions(doc, c.comments, rng), nil
}

func (s *Server) Format(_ context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	set := s.settings()
	d := s.dialect(doc)
	indent := uint(0)
	if opts.InsertSpaces {
		indent = uint(max(opts.TabSize, 1))
	}
	f := set.Shfmt
	if path := doc.Path(); path != "" && !f.IgnoreEditorconfig {
		if sec, err := editorconfig.Find(path, []string{"shell"}); err == nil {
			switch sec.Get("indent_style") {
			case "tab":
				indent = 0
			case "space":
				if n := sec.IndentSize(); n > 0 {
					indent = uint(n)
				}
			}
			ecBool := func(name string, dst *bool) {
				switch sec.Get(name) {
				case "true":
					*dst = true
				case "false":
					*dst = false
				}
			}
			ecBool("binary_next_line", &f.BinaryNextLine)
			ecBool("switch_case_indent", &f.CaseIndent)
			ecBool("space_redirects", &f.SpaceRedirects)
			ecBool("keep_padding", &f.KeepPadding)
			ecBool("function_next_line", &f.FuncNextLine)
			if v := sec.Get("shell_variant"); v != "" {
				var lv syntax.LangVariant
				if lv.Set(v) == nil {
					d = dialectFor(lv)
				}
			}
		}
	}

	// Format only what parses cleanly: printing a recovered tree would change the script.
	file, err := syntax.NewParser(syntax.Variant(d.variant), syntax.KeepComments(true)).
		Parse(strings.NewReader(doc.Text), "")
	if err != nil {
		return nil, nil
	}
	if f.SimplifyCode {
		syntax.Simplify(file)
	}
	var buf bytes.Buffer
	printer := syntax.NewPrinter(syntax.Indent(indent), syntax.BinaryNextLine(f.BinaryNextLine),
		syntax.SwitchCaseIndent(f.CaseIndent), syntax.SpaceRedirects(f.SpaceRedirects),
		syntax.KeepPadding(f.KeepPadding), syntax.FunctionNextLine(f.FuncNextLine))
	if err := printer.Print(&buf, file); err != nil {
		return nil, err
	}
	out := buf.String()
	if out == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: out}}, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	a := s.analyze(doc)
	if a.file == nil {
		return nil, nil
	}
	var out []protocol.DocumentSymbol
	inFunc := func(o int) bool {
		for _, fn := range a.funcs {
			if o >= off(fn.Pos()) && o < off(fn.End()) {
				return true
			}
		}
		return false
	}
	for _, fn := range a.funcs {
		if fn.Name == nil || fn.Name.Pos().IsRecovered() {
			continue
		}
		sym := protocol.DocumentSymbol{
			Name: fn.Name.Value, Kind: protocol.SymbolFunction,
			Range:          doc.Range(off(fn.Pos()), off(fn.End())),
			SelectionRange: doc.Range(off(fn.Name.Pos()), off(fn.Name.End())),
		}
		// Variables first assigned inside the function become its children.
		seen := map[string]bool{}
		for name, spans := range a.varDef {
			sp := spans[0]
			if sp.start >= off(fn.Pos()) && sp.start < off(fn.End()) && !seen[name] {
				seen[name] = true
				sym.Children = append(sym.Children, varSymbol(doc, sp))
			}
		}
		sortSymbols(sym.Children)
		out = append(out, sym)
	}
	for _, spans := range a.varDef {
		if sp := spans[0]; !inFunc(sp.start) {
			out = append(out, varSymbol(doc, sp))
		}
	}
	sortSymbols(out)
	return out, nil
}

func varSymbol(doc *document.Doc, sp span) protocol.DocumentSymbol {
	r := doc.Range(sp.start, sp.end)
	return protocol.DocumentSymbol{Name: sp.name, Kind: protocol.SymbolVariable, Range: r, SelectionRange: r}
}

func sortSymbols(syms []protocol.DocumentSymbol) {
	sort.Slice(syms, func(i, j int) bool { return before(syms[i].Range.Start, syms[j].Range.Start) })
}

func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	a := s.analyze(doc)
	name, isVar, ok := a.symbolAt(doc.Offset(pos))
	if !ok {
		// `source lib.sh`: jump to the file.
		o := doc.Offset(pos)
		for _, src := range a.sources {
			if o >= src.start && o <= src.end {
				if p := s.resolveSource(doc, src.name); p != "" {
					return []protocol.Location{{URI: document.PathToURI(p)}}, nil
				}
			}
		}
		return nil, nil
	}
	defs := func(an *analysis) []span {
		if isVar {
			return an.varDef[name]
		}
		return an.funcDef[name]
	}
	if spans := defs(a); len(spans) > 0 {
		return []protocol.Location{{URI: doc.URI, Range: doc.Range(spans[0].start, spans[0].end)}}, nil
	}
	for _, f := range s.sourced(doc, a) {
		if spans := defs(f.a); len(spans) > 0 {
			fd := document.New(document.PathToURI(f.path), "shellscript", 0, f.text, doc.Encoding())
			return []protocol.Location{{URI: fd.URI, Range: fd.Range(spans[0].start, spans[0].end)}}, nil
		}
	}
	return nil, nil
}

func (s *Server) References(_ context.Context, doc *document.Doc, pos protocol.Position, includeDecl bool) ([]protocol.Location, error) {
	a := s.analyze(doc)
	name, isVar, ok := a.symbolAt(doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	var spans []span
	if isVar {
		if includeDecl {
			spans = append(spans, a.varDef[name]...)
		}
		spans = append(spans, a.varRef[name]...)
	} else {
		if includeDecl {
			spans = append(spans, a.funcDef[name]...)
		}
		spans = append(spans, a.calls[name]...)
	}
	out := make([]protocol.Location, 0, len(spans))
	for _, sp := range spans {
		out = append(out, protocol.Location{URI: doc.URI, Range: doc.Range(sp.start, sp.end)})
	}
	return out, nil
}

type sourcedFile struct {
	path, text string
	a          *analysis
}

// sourced analyzes the files the script sources (recursively, at most a few levels deep).
func (s *Server) sourced(doc *document.Doc, a *analysis) []sourcedFile {
	var out []sourcedFile
	seen := map[string]bool{doc.Path(): true}
	var visit func(from *document.Doc, an *analysis, depth int)
	visit = func(from *document.Doc, an *analysis, depth int) {
		if depth > 3 {
			return
		}
		for _, src := range an.sources {
			p := s.resolveSource(from, src.name)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			fd := document.New(document.PathToURI(p), "shellscript", 0, string(data), from.Encoding())
			fa := analyze(fd.Text, detectDialect(fd.Text, p, ""))
			out = append(out, sourcedFile{p, fd.Text, fa})
			visit(fd, fa, depth+1)
		}
	}
	visit(doc, a, 0)
	return out
}

// resolveSource finds a sourced file relative to the script, then the workspace root.
func (s *Server) resolveSource(doc *document.Doc, name string) string {
	name = os.ExpandEnv(strings.Trim(name, `"'`))
	if strings.HasPrefix(name, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			name = filepath.Join(home, name[2:])
		}
	}
	candidates := []string{name}
	if !filepath.IsAbs(name) {
		candidates = nil
		if p := doc.Path(); p != "" {
			candidates = append(candidates, filepath.Join(filepath.Dir(p), name))
		}
		if s.Env.RootPath != "" {
			candidates = append(candidates, filepath.Join(s.Env.RootPath, name))
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}
