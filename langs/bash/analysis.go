package bash

import (
	"errors"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// span is a named byte range in a file.
type span struct {
	name       string
	start, end int
}

// analysis is what the module knows about one script.
type analysis struct {
	file     *syntax.File
	problems []problem

	funcs   []*syntax.FuncDecl
	funcDef map[string][]span // function name -> declaration name ranges
	calls   map[string][]span // command name -> call sites (first word of a command)
	varDef  map[string][]span // assignments, for/select loop variables, read targets
	varRef  map[string][]span // $x and ${x}
	sources []span            // literal arguments of `source` / `.`
}

type problem struct {
	start, end int
	msg        string
}

// dialect describes the shell a script is written for.
type dialect struct {
	variant    syntax.LangVariant
	shellcheck string // value for shellcheck --shell, "" to skip shellcheck
}

// detectDialect looks at the shebang, then the file extension; bash is the default.
func detectDialect(text, path, override string) dialect {
	if override != "" && override != "auto" {
		var v syntax.LangVariant
		if v.Set(override) == nil {
			return dialectFor(v)
		}
	}
	if strings.HasPrefix(text, "#!") {
		line, _, _ := strings.Cut(text, "\n")
		fields := strings.Fields(strings.TrimPrefix(line, "#!"))
		interp := ""
		for i, f := range fields {
			base := filepath.Base(f)
			if i == 0 && base == "env" {
				continue
			}
			if strings.HasPrefix(f, "-") {
				continue
			}
			interp = base
			break
		}
		switch interp {
		case "sh", "dash", "ash", "posix":
			return dialect{syntax.LangPOSIX, "sh"}
		case "bash":
			return dialect{syntax.LangBash, "bash"}
		case "ksh", "mksh", "ksh93":
			return dialect{syntax.LangMirBSDKorn, "ksh"}
		case "zsh":
			return dialect{syntax.LangZsh, ""}
		case "bats":
			return dialect{syntax.LangBats, "bash"}
		}
	}
	switch filepath.Ext(path) {
	case ".bats":
		return dialect{syntax.LangBats, "bash"}
	case ".zsh":
		return dialect{syntax.LangZsh, ""}
	case ".ksh", ".mksh":
		return dialect{syntax.LangMirBSDKorn, "ksh"}
	}
	return dialect{syntax.LangBash, "bash"}
}

func dialectFor(v syntax.LangVariant) dialect {
	switch v {
	case syntax.LangPOSIX:
		return dialect{v, "sh"}
	case syntax.LangMirBSDKorn:
		return dialect{v, "ksh"}
	case syntax.LangZsh:
		return dialect{v, ""}
	}
	return dialect{v, "bash"}
}

// analyze parses text, recovering from a few errors so that a half-typed script still has
// symbols.
func analyze(text string, d dialect) *analysis {
	a := &analysis{
		funcDef: map[string][]span{}, calls: map[string][]span{},
		varDef: map[string][]span{}, varRef: map[string][]span{},
	}
	// A strict parse finds the first error; with error recovery the parser would silently fill
	// in missing tokens (like a missing "fi"). The recovering parse then keeps the symbols.
	f, err := syntax.NewParser(syntax.Variant(d.variant), syntax.KeepComments(true)).
		Parse(strings.NewReader(text), "")
	if err != nil {
		a.problems = append(a.problems, toProblem(text, err))
		f, _ = syntax.NewParser(syntax.Variant(d.variant), syntax.KeepComments(true), syntax.RecoverErrors(8)).
			Parse(strings.NewReader(text), "")
	}
	if f == nil {
		return a
	}
	a.file = f
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncDecl:
			a.funcs = append(a.funcs, n)
			if n.Name != nil {
				add(a.funcDef, n.Name)
			}
			for _, name := range n.Names {
				add(a.funcDef, name)
			}
		case *syntax.Assign:
			if n.Name != nil {
				add(a.varDef, n.Name)
			}
		case *syntax.WordIter:
			if n.Name != nil {
				add(a.varDef, n.Name)
			}
		case *syntax.ParamExp:
			if n.Param != nil {
				add(a.varRef, n.Param)
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			}
			name := n.Args[0].Lit()
			if name == "" {
				break
			}
			w := n.Args[0]
			a.calls[name] = append(a.calls[name], span{name, off(w.Pos()), off(w.End())})
			switch name {
			case "source", ".":
				if len(n.Args) > 1 {
					if p := n.Args[1].Lit(); p != "" {
						a.sources = append(a.sources, span{p, off(n.Args[1].Pos()), off(n.Args[1].End())})
					}
				}
			case "read", "mapfile", "readarray":
				for _, w := range n.Args[1:] {
					if lit := w.Lit(); lit != "" && !strings.HasPrefix(lit, "-") && validName(lit) {
						a.varDef[lit] = append(a.varDef[lit], span{lit, off(w.Pos()), off(w.End())})
					}
				}
			}
		}
		return true
	})
	return a
}

func add(m map[string][]span, l *syntax.Lit) {
	if l == nil || !l.Pos().IsValid() || l.Pos().IsRecovered() {
		return
	}
	m[l.Value] = append(m[l.Value], span{l.Value, off(l.Pos()), off(l.End())})
}

func off(p syntax.Pos) int { return int(p.Offset()) }

func validName(s string) bool {
	for i, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return s != ""
}

func toProblem(text string, err error) problem {
	var pe syntax.ParseError
	var le syntax.LangError
	var pos syntax.Pos
	msg := err.Error()
	switch {
	case errors.As(err, &pe):
		pos, msg = pe.Pos, pe.Text
	case errors.As(err, &le):
		pos = le.Pos
		msg = le.Error()
		if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, le.Pos.String()) {
			msg = msg[i+2:]
		}
	}
	start := min(off(pos), len(text))
	end := start
	for end < len(text) && text[end] != ' ' && text[end] != '\n' && text[end] != '\t' {
		end++
	}
	if end == start && start < len(text) {
		end = start + 1
	} else if end == start && start > 0 {
		start--
	}
	return problem{start: start, end: end, msg: msg}
}

// symbolAt returns the function or variable name at off and which kind it is.
func (a *analysis) symbolAt(off int) (name string, isVar bool, ok bool) {
	for _, m := range []map[string][]span{a.varRef, a.varDef} {
		for name, spans := range m {
			for _, s := range spans {
				if off >= s.start && off <= s.end {
					return name, true, true
				}
			}
		}
	}
	for _, m := range []map[string][]span{a.funcDef, a.calls} {
		for name, spans := range m {
			for _, s := range spans {
				if off >= s.start && off <= s.end {
					return name, false, true
				}
			}
		}
	}
	return "", false, false
}

// commentBefore returns the comment block directly above byte offset start.
func commentBefore(text string, start int) string {
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	var lines []string
	for lineStart > 0 {
		prevEnd := lineStart - 1
		prevStart := strings.LastIndexByte(text[:prevEnd], '\n') + 1
		l := strings.TrimSpace(text[prevStart:prevEnd])
		if !strings.HasPrefix(l, "#") || strings.HasPrefix(l, "#!") {
			break
		}
		lines = append([]string{strings.TrimSpace(strings.TrimPrefix(l, "#"))}, lines...)
		lineStart = prevStart
	}
	return strings.Join(lines, "\n")
}

// lineAt returns the full line containing off.
func lineAt(text string, off int) string {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : off+end]
}
