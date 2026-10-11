// Package dockerfile is the Dockerfile / Containerfile language module: built-in checks plus
// optional hadolint, completion of instructions, flags, stages and variables, hover,
// navigation between stages and variables, outline and keyword formatting.
package dockerfile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/extproc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "dockerfile",
		Description: "Dockerfile / Containerfile: checks, hadolint, completion, hover, stages and variables",
		New:         func() lang.Language { return &Server{} },
	})
}

// Settings live in the "docker" section.
type Settings struct {
	HadolintPath string   // "" disables hadolint
	HadolintArgs []string // e.g. ["--ignore", "DL3008"]
}

// Server implements the module.
type Server struct {
	Env lang.Env
	mu  sync.Mutex
	set Settings
}

func (s *Server) Name() string { return "dockerfile" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	s.set = Settings{HadolintPath: "hadolint"}
	if len(env.InitOptions) > 0 {
		_ = s.Configure(env.InitOptions)
	}
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{"-", "=", "$", "{", ","} }

func (s *Server) ConfigSections() []string { return []string{"docker"} }

func (s *Server) Configure(raw json.RawMessage) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if inner, ok := top["docker"]; ok {
		if err := json.Unmarshal(inner, &top); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := top["hadolintPath"]; ok {
		_ = json.Unmarshal(v, &s.set.HadolintPath)
	}
	if v, ok := top["hadolintArguments"]; ok {
		var list []string
		var str string
		if json.Unmarshal(v, &list) == nil {
			s.set.HadolintArgs = list
		} else if json.Unmarshal(v, &str) == nil {
			s.set.HadolintArgs = strings.Fields(str)
		}
	}
	return nil
}

func (s *Server) settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

func diag(doc *document.Doc, start, end int, sev protocol.DiagnosticSeverity, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{Range: doc.Range(start, end), Severity: sev, Source: "bls", Message: msg}
}

var jsonInstructions = map[string]bool{"CMD": true, "ENTRYPOINT": true, "RUN": true, "SHELL": true,
	"VOLUME": true, "COPY": true, "ADD": true}

var stageNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]*$`)

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	f := Parse(doc.Text)
	out := lint(doc, f)
	out = append(out, s.hadolint(ctx, doc)...)
	return out
}

// lint implements the built-in checks.
func lint(doc *document.Doc, f *File) []protocol.Diagnostic {
	var out []protocol.Diagnostic
	sawFrom := false
	for _, ins := range f.Instructions {
		kwEnd := ins.KeywordEnd
		if _, known := instructions[ins.Keyword]; !known {
			out = append(out, diag(doc, ins.KeywordStart, kwEnd, protocol.SeverityError,
				fmt.Sprintf("Unknown instruction %s.", ins.Keyword)))
			continue
		}
		if !sawFrom && ins.Keyword != "ARG" && ins.Keyword != "FROM" {
			out = append(out, diag(doc, ins.KeywordStart, kwEnd, protocol.SeverityError,
				"The first instruction must be FROM (only ARG may come before it)."))
		}
		if ins.Keyword == "FROM" {
			sawFrom = true
		}
		if strings.TrimSpace(ins.Args) == "" && len(ins.Heredocs) == 0 {
			out = append(out, diag(doc, ins.KeywordStart, kwEnd, protocol.SeverityError,
				fmt.Sprintf("%s needs at least one argument.", ins.Keyword)))
			continue
		}
		if ins.JSONInvalid && jsonInstructions[ins.Keyword] {
			out = append(out, diag(doc, ins.ArgsStart, ins.End, protocol.SeverityWarning,
				"This looks like JSON (exec) form but isn't a valid JSON string array, so it runs in shell form."))
		}
		switch ins.Keyword {
		case "CMD", "ENTRYPOINT":
			if ins.JSON == nil && !ins.JSONInvalid {
				out = append(out, diag(doc, ins.ArgsStart, ins.End, protocol.SeverityInformation,
					fmt.Sprintf("Use JSON (exec) form for %s so the process receives signals: %s [\"executable\", \"arg\"].", ins.Keyword, ins.Keyword)))
			}
		case "MAINTAINER":
			out = append(out, diag(doc, ins.KeywordStart, kwEnd, protocol.SeverityWarning,
				"MAINTAINER is deprecated; use LABEL org.opencontainers.image.authors=\"...\"."))
		}
	}

	stages := f.Stages()
	names := map[string]*Stage{}
	for _, st := range stages {
		if st.Name != "" {
			key := strings.ToLower(st.Name)
			if names[key] != nil {
				out = append(out, diag(doc, st.NameStart, st.NameEnd, protocol.SeverityError,
					fmt.Sprintf("Duplicate stage name %q.", st.Name)))
			}
			names[key] = st
		}
	}
	for _, st := range stages {
		img := st.Image.Text
		if img != "" && img != "scratch" && names[strings.ToLower(img)] == nil && !strings.Contains(img, "$") &&
			!strings.Contains(img, "@") && !hasTag(img) {
			out = append(out, diag(doc, st.Image.Start, st.Image.End, protocol.SeverityInformation,
				fmt.Sprintf("Pin the base image version (%s:<tag> or a digest) for reproducible builds.", img)))
		}
		last := map[string]*Instruction{}
		workdirSeen := false
		for _, ins := range st.Instructions {
			switch ins.Keyword {
			case "CMD", "ENTRYPOINT", "HEALTHCHECK":
				if prev := last[ins.Keyword]; prev != nil {
					out = append(out, diag(doc, prev.KeywordStart, prev.KeywordEnd, protocol.SeverityWarning,
						fmt.Sprintf("Only the last %s in a stage takes effect; this one is overridden.", ins.Keyword)))
				}
				last[ins.Keyword] = ins
			case "WORKDIR":
				p := strings.TrimSpace(ins.Args)
				if !workdirSeen && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "$") && !strings.Contains(p, ":\\") {
					out = append(out, diag(doc, ins.ArgsStart, ins.End, protocol.SeverityWarning,
						"Relative WORKDIR depends on the base image's working directory; use an absolute path."))
				}
				workdirSeen = true
			case "COPY":
				if fl := ins.Flag("from"); fl != nil && fl.ValueStart >= 0 {
					v := fl.Value
					if stageNameRe.MatchString(v) && !strings.ContainsAny(v, ":/.") && names[strings.ToLower(v)] == nil {
						out = append(out, diag(doc, fl.ValueStart, fl.End, protocol.SeverityWarning,
							fmt.Sprintf("No stage named %q; it will be pulled as an image.", v)))
					}
				}
			}
		}
	}
	return out
}

// hasTag reports whether an image reference has a tag (a ':' after the last '/').
func hasTag(img string) bool {
	i := strings.LastIndexByte(img, '/')
	return strings.Contains(img[i+1:], ":")
}

// hadolint runs hadolint if installed.
func (s *Server) hadolint(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	set := s.settings()
	path := extproc.Find(set.HadolintPath)
	if path == "" {
		return nil
	}
	dir := ""
	if p := doc.Path(); p != "" {
		dir = filepath.Dir(p)
	}
	args := append([]string{"--format", "json", "--no-fail"}, set.HadolintArgs...)
	args = append(args, "-")
	res, err := extproc.Run(ctx, dir, doc.Text, path, args...)
	if err != nil {
		s.Env.Logger.Warn("hadolint failed", "err", err)
		return nil
	}
	var items []struct {
		Line, Column  int
		Code, Message string
		Level         string
	}
	if err := json.Unmarshal(res.Stdout, &items); err != nil {
		return nil
	}
	var out []protocol.Diagnostic
	for _, it := range items {
		start := doc.Offset(protocol.Position{Line: uint32(max(it.Line-1, 0))})
		end := start + len(doc.Line(max(it.Line-1, 0)))
		sev := protocol.SeverityInformation
		switch it.Level {
		case "error":
			sev = protocol.SeverityError
		case "warning":
			sev = protocol.SeverityWarning
		case "style":
			sev = protocol.SeverityHint
		}
		href := "https://github.com/hadolint/hadolint/wiki/" + it.Code
		if strings.HasPrefix(it.Code, "SC") {
			href = "https://www.shellcheck.net/wiki/" + it.Code
		}
		d := diag(doc, start, end, sev, it.Message)
		d.Source, d.Code, d.CodeDescription = "hadolint", it.Code, &protocol.CodeDescription{Href: href}
		out = append(out, d)
	}
	return out
}

// --- navigation ---

// target is what sits under the cursor.
type target struct {
	stage      *Stage // stage referenced or defined
	varName    string
	start, end int
}

func at(doc *document.Doc, f *File, off int) (target, bool) {
	stages := f.Stages()
	byName := map[string]*Stage{}
	for _, st := range stages {
		if st.Name != "" {
			byName[strings.ToLower(st.Name)] = st
		}
	}
	for _, st := range stages {
		if st.Name != "" && off >= st.NameStart && off <= st.NameEnd {
			return target{stage: st, start: st.NameStart, end: st.NameEnd}, true
		}
		if ref := byName[strings.ToLower(st.Image.Text)]; ref != nil && off >= st.Image.Start && off <= st.Image.End {
			return target{stage: ref, start: st.Image.Start, end: st.Image.End}, true
		}
	}
	for _, ins := range f.Instructions {
		for _, fl := range ins.Flags {
			if fl.Name == "from" && fl.ValueStart >= 0 && off >= fl.ValueStart && off <= fl.End {
				if ref := stageRef(stages, byName, fl.Value); ref != nil {
					return target{stage: ref, start: fl.ValueStart, end: fl.End}, true
				}
			}
		}
	}
	for _, r := range f.VarRefs(doc.Text) {
		if off >= r.Start && off <= r.End {
			return target{varName: r.Name, start: r.Start, end: r.End}, true
		}
	}
	for _, v := range f.Vars() {
		if off >= v.Start && off <= v.End {
			return target{varName: v.Name, start: v.Start, end: v.End}, true
		}
	}
	return target{}, false
}

// stageRef resolves a --from value: a stage name or a stage index.
func stageRef(stages []*Stage, byName map[string]*Stage, v string) *Stage {
	if st := byName[strings.ToLower(v)]; st != nil {
		return st
	}
	var i int
	if _, err := fmt.Sscanf(v, "%d", &i); err == nil && fmt.Sprint(i) == v && i >= 0 && i < len(stages) {
		return stages[i]
	}
	return nil
}

func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	f := Parse(doc.Text)
	t, ok := at(doc, f, doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	if t.stage != nil {
		st := t.stage
		start, end := st.From.KeywordStart, st.From.KeywordEnd
		if st.Name != "" {
			start, end = st.NameStart, st.NameEnd
		}
		return []protocol.Location{{URI: doc.URI, Range: doc.Range(start, end)}}, nil
	}
	// The closest definition before the use, else the first one.
	var best *VarDef
	vars := f.Vars()
	for i := range vars {
		v := &vars[i]
		if v.Name != t.varName {
			continue
		}
		if best == nil || v.Start <= t.start {
			best = v
		}
	}
	if best == nil {
		return nil, nil
	}
	return []protocol.Location{{URI: doc.URI, Range: doc.Range(best.Start, best.End)}}, nil
}

func (s *Server) References(_ context.Context, doc *document.Doc, pos protocol.Position, includeDecl bool) ([]protocol.Location, error) {
	f := Parse(doc.Text)
	t, ok := at(doc, f, doc.Offset(pos))
	if !ok {
		return nil, nil
	}
	var spans [][2]int
	if t.stage != nil {
		name := strings.ToLower(t.stage.Name)
		if name == "" {
			return nil, nil
		}
		if includeDecl {
			spans = append(spans, [2]int{t.stage.NameStart, t.stage.NameEnd})
		}
		for _, st := range f.Stages() {
			if strings.ToLower(st.Image.Text) == name {
				spans = append(spans, [2]int{st.Image.Start, st.Image.End})
			}
		}
		for _, ins := range f.Instructions {
			for _, fl := range ins.Flags {
				if fl.Name == "from" && fl.ValueStart >= 0 && strings.ToLower(fl.Value) == name {
					spans = append(spans, [2]int{fl.ValueStart, fl.End})
				}
			}
		}
	} else {
		if includeDecl {
			for _, v := range f.Vars() {
				if v.Name == t.varName {
					spans = append(spans, [2]int{v.Start, v.End})
				}
			}
		}
		for _, r := range f.VarRefs(doc.Text) {
			if r.Name == t.varName {
				spans = append(spans, [2]int{r.Start, r.End})
			}
		}
	}
	out := make([]protocol.Location, 0, len(spans))
	for _, sp := range spans {
		out = append(out, protocol.Location{URI: doc.URI, Range: doc.Range(sp[0], sp[1])})
	}
	return out, nil
}

// --- hover ---

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	f := Parse(doc.Text)
	off := doc.Offset(pos)
	md, start, end := "", 0, 0

	if t, ok := at(doc, f, off); ok {
		start, end = t.start, t.end
		if t.stage != nil {
			md = fmt.Sprintf("Build stage `%s`\n\n```dockerfile\n%s\n```", stageLabel(t.stage),
				strings.TrimSpace(doc.Text[t.stage.From.Start:t.stage.From.End]))
		} else {
			var lines []string
			for _, v := range f.Vars() {
				if v.Name == t.varName {
					lines = append(lines, strings.TrimSpace(firstLine(doc.Text[v.Ins.Start:v.Ins.End])))
				}
			}
			if len(lines) > 0 {
				md = "```dockerfile\n" + strings.Join(lines, "\n") + "\n```"
			} else if isPredefinedArg(t.varName) {
				md = fmt.Sprintf("`%s` is a predefined build argument (BuildKit).", t.varName)
			}
		}
	}
	if md == "" {
		for _, ins := range f.Instructions {
			if off >= ins.KeywordStart && off <= ins.KeywordEnd {
				if d, ok := instructions[ins.Keyword]; ok {
					md = fmt.Sprintf("```dockerfile\n%s\n```\n\n%s\n\n[Reference](%s%s)", d.Syntax, d.Summary,
						referenceURL, strings.ToLower(ins.Keyword))
					start, end = ins.KeywordStart, ins.KeywordEnd
				}
				break
			}
			for _, fl := range ins.Flags {
				if off >= fl.Start && off <= fl.End {
					for _, fd := range instructions[ins.Keyword].Flags {
						if fd.Name == fl.Name {
							md = fmt.Sprintf("`--%s`: %s", fd.Name, fd.Summary)
							start, end = fl.Start, fl.End
						}
					}
				}
			}
		}
	}
	if md == "" {
		return nil, nil
	}
	r := doc.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: md}, Range: &r}, nil
}

func stageLabel(st *Stage) string {
	if st.Name != "" {
		return st.Name
	}
	return st.Image.Text
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

var predefinedArgs = strings.Fields(`TARGETPLATFORM TARGETOS TARGETARCH TARGETVARIANT BUILDPLATFORM BUILDOS
BUILDARCH BUILDVARIANT HTTP_PROXY HTTPS_PROXY FTP_PROXY NO_PROXY ALL_PROXY BUILDKIT_INLINE_CACHE
BUILDKIT_SYNTAX SOURCE_DATE_EPOCH`)

func isPredefinedArg(n string) bool {
	for _, a := range predefinedArgs {
		if a == n {
			return true
		}
	}
	return false
}

// --- outline and formatting ---

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	f := Parse(doc.Text)
	var out []protocol.DocumentSymbol
	insSym := func(ins *Instruction) protocol.DocumentSymbol {
		name := ins.Keyword + " " + strings.TrimSpace(firstLine(ins.Args))
		if len(name) > 60 {
			name = name[:57] + "..."
		}
		kind := protocol.SymbolKey
		if ins.Keyword == "ARG" || ins.Keyword == "ENV" {
			kind = protocol.SymbolVariable
		}
		return protocol.DocumentSymbol{Name: name, Kind: kind, Range: doc.Range(ins.Start, ins.End),
			SelectionRange: doc.Range(ins.KeywordStart, ins.KeywordEnd)}
	}
	for _, ins := range f.Instructions {
		if ins.Keyword == "FROM" {
			break
		}
		out = append(out, insSym(ins)) // global ARGs
	}
	for _, st := range f.Stages() {
		end := st.From.End
		if n := len(st.Instructions); n > 0 {
			end = st.Instructions[n-1].End
		}
		sym := protocol.DocumentSymbol{Name: stageLabel(st), Detail: st.Image.Text, Kind: protocol.SymbolClass,
			Range: doc.Range(st.From.Start, end), SelectionRange: doc.Range(st.From.KeywordStart, st.From.KeywordEnd)}
		if st.Name != "" {
			sym.SelectionRange = doc.Range(st.NameStart, st.NameEnd)
		}
		for _, ins := range st.Instructions {
			sym.Children = append(sym.Children, insSym(ins))
		}
		out = append(out, sym)
	}
	return out, nil
}

// Format upper-cases instruction keywords and the AS of FROM lines; it doesn't move text.
func (s *Server) Format(_ context.Context, doc *document.Doc, _ protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	f := Parse(doc.Text)
	var edits []protocol.TextEdit
	for _, ins := range f.Instructions {
		kw := doc.Text[ins.KeywordStart:ins.KeywordEnd]
		if _, known := instructions[ins.Keyword]; known && kw != ins.Keyword {
			edits = append(edits, protocol.TextEdit{Range: doc.Range(ins.KeywordStart, ins.KeywordEnd), NewText: ins.Keyword})
		}
		if ins.Keyword == "FROM" {
			if w := ins.Words(); len(w) >= 3 && strings.EqualFold(w[1].Text, "AS") && w[1].Text != "AS" {
				edits = append(edits, protocol.TextEdit{Range: doc.Range(w[1].Start, w[1].End), NewText: "AS"})
			}
		}
	}
	sort.Slice(edits, func(i, j int) bool {
		a, b := edits[i].Range.Start, edits[j].Range.Start
		return a.Line < b.Line || (a.Line == b.Line && a.Character < b.Character)
	})
	return edits, nil
}
