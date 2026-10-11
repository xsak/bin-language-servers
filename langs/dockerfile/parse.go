package dockerfile

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Instruction is one Dockerfile instruction with its continuation lines and heredocs.
type Instruction struct {
	Keyword                  string // upper case
	KeywordStart, KeywordEnd int
	Start, End               int // whole instruction, End excludes the final newline
	Flags                    []Flag
	Args                     string // raw text after the flags (may span lines)
	ArgsStart                int
	JSON                     []string // set when the arguments are in JSON (exec) form
	JSONInvalid              bool     // starts with '[' but isn't a JSON string array
	Heredocs                 []string // heredoc delimiters
}

// Flag is a --name[=value] option.
type Flag struct {
	Name, Value string
	Start, End  int // whole flag
	ValueStart  int // -1 without '='
}

// File is a parsed Dockerfile.
type File struct {
	Instructions []*Instruction
	Directives   map[string]string // escape, syntax, check
	Escape       byte
}

var heredocRe = regexp.MustCompile(`<<(-?)(["']?)([A-Za-z_][A-Za-z0-9_]*)(["']?)`)

// Parse splits text into instructions. It never fails; unknown instructions are kept and
// reported by the linter.
func Parse(text string) *File {
	f := &File{Directives: map[string]string{}, Escape: '\\'}
	lines := splitLines(text)
	directives := true

	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln.text)
		if trimmed == "" {
			directives = false
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if directives {
				if k, v, ok := parseDirective(trimmed); ok {
					f.Directives[k] = v
					if k == "escape" && (v == "`" || v == "\\") {
						f.Escape = v[0]
					}
					continue
				}
				directives = false
			}
			continue
		}
		directives = false

		ins := &Instruction{Start: ln.start}
		lead := len(ln.text) - len(strings.TrimLeft(ln.text, " \t"))
		ins.KeywordStart = ln.start + lead
		kw := strings.Fields(trimmed)[0]
		ins.KeywordEnd = ins.KeywordStart + len(kw)
		ins.Keyword = strings.ToUpper(kw)

		// Join continuation lines; comment lines inside a continuation are skipped.
		end := ln.start + len(strings.TrimRight(ln.text, " \t\r"))
		for continues(lines[i].text, f.Escape) && i+1 < len(lines) {
			i++
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i].text), "#") {
				i++
			}
			if i >= len(lines) {
				break
			}
			if t := strings.TrimRight(lines[i].text, " \t\r"); t != "" {
				end = lines[i].start + len(t)
			}
		}
		ins.End = end

		// Heredocs: the body runs until a line that is exactly the delimiter.
		if ins.Keyword == "RUN" || ins.Keyword == "COPY" || ins.Keyword == "ADD" {
			for _, m := range heredocRe.FindAllStringSubmatch(text[ins.KeywordEnd:ins.End], -1) {
				if m[2] != m[4] {
					continue
				}
				ins.Heredocs = append(ins.Heredocs, m[3])
				for i+1 < len(lines) {
					i++
					body := lines[i].text
					if m[1] == "-" {
						body = strings.TrimLeft(body, "\t")
					}
					ins.End = lines[i].start + len(strings.TrimRight(lines[i].text, "\r"))
					if strings.TrimRight(body, "\r") == m[3] {
						break
					}
				}
			}
		}

		parseArgs(text, ins)
		f.Instructions = append(f.Instructions, ins)
	}
	return f
}

type line struct {
	start int
	text  string
}

func splitLines(text string) []line {
	var out []line
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == '\n' {
			out = append(out, line{start, strings.TrimSuffix(text[start:i], "\r")})
			start = i + 1
		}
	}
	return out
}

func continues(l string, esc byte) bool {
	t := strings.TrimRight(l, " \t\r")
	return len(t) > 0 && t[len(t)-1] == esc
}

var directiveRe = regexp.MustCompile(`^#\s*([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)

func parseDirective(l string) (string, string, bool) {
	m := directiveRe.FindStringSubmatch(l)
	if m == nil {
		return "", "", false
	}
	k := strings.ToLower(m[1])
	switch k {
	case "escape", "syntax", "check":
		return k, m[2], true
	}
	return "", "", false
}

// parseArgs splits flags from the arguments and decodes JSON form.
func parseArgs(text string, ins *Instruction) {
	pos := ins.KeywordEnd
	for {
		for pos < ins.End && (text[pos] == ' ' || text[pos] == '\t') {
			pos++
		}
		if pos+2 > ins.End || text[pos:pos+2] != "--" {
			break
		}
		end := pos
		for end < ins.End && text[end] != ' ' && text[end] != '\t' && text[end] != '\n' && text[end] != '\r' {
			end++
		}
		tok := text[pos:end]
		fl := Flag{Start: pos, End: end, ValueStart: -1}
		if k, v, ok := strings.Cut(tok[2:], "="); ok {
			fl.Name, fl.Value, fl.ValueStart = k, v, pos+2+len(k)+1
		} else {
			fl.Name = tok[2:]
		}
		ins.Flags = append(ins.Flags, fl)
		pos = end
	}
	ins.ArgsStart = pos
	ins.Args = text[pos:ins.End]
	if t := strings.TrimSpace(ins.Args); strings.HasPrefix(t, "[") {
		var arr []string
		if json.Unmarshal([]byte(joinContinuations(t)), &arr) == nil {
			ins.JSON = arr
		} else {
			ins.JSONInvalid = true
		}
	}
}

// joinContinuations removes "\<newline>" so multi-line JSON arrays decode.
func joinContinuations(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimRight(l, " \t\r")
		b.WriteString(strings.TrimSuffix(t, "\\"))
	}
	return b.String()
}

// Words splits the arguments of a shell-form instruction on whitespace (continuations
// removed), with the offset of each word.
func (ins *Instruction) Words() []Word {
	var out []Word
	s := ins.Args
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || (s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\n' || s[i+1] == '\r'))) {
			i++
		}
		start := i
		for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			i++
		}
		if i > start {
			out = append(out, Word{Text: s[start:i], Start: ins.ArgsStart + start, End: ins.ArgsStart + i})
		}
	}
	return out
}

// Word is a whitespace-separated argument.
type Word struct {
	Text       string
	Start, End int
}

// Flag returns the flag with the given name.
func (ins *Instruction) Flag(name string) *Flag {
	for i := range ins.Flags {
		if ins.Flags[i].Name == name {
			return &ins.Flags[i]
		}
	}
	return nil
}

// Stage is a build stage: FROM image [AS name] and the instructions up to the next FROM.
type Stage struct {
	From         *Instruction
	Image        Word
	Name         string
	NameStart    int
	NameEnd      int
	Instructions []*Instruction
}

// Stages groups instructions by FROM.
func (f *File) Stages() []*Stage {
	var out []*Stage
	for _, ins := range f.Instructions {
		if ins.Keyword == "FROM" {
			st := &Stage{From: ins}
			w := ins.Words()
			if len(w) > 0 {
				st.Image = w[0]
			}
			if len(w) >= 3 && strings.EqualFold(w[1].Text, "AS") {
				st.Name, st.NameStart, st.NameEnd = w[2].Text, w[2].Start, w[2].End
			}
			out = append(out, st)
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].Instructions = append(out[len(out)-1].Instructions, ins)
		}
	}
	return out
}

// VarDef is an ARG or ENV definition.
type VarDef struct {
	Name       string
	Value      string
	Start, End int // the name
	Keyword    string
	Ins        *Instruction
}

// Vars returns ARG and ENV definitions in order.
func (f *File) Vars() []VarDef {
	var out []VarDef
	for _, ins := range f.Instructions {
		switch ins.Keyword {
		case "ARG":
			for _, w := range ins.Words() {
				name, val, _ := strings.Cut(w.Text, "=")
				out = append(out, VarDef{Name: name, Value: val, Start: w.Start, End: w.Start + len(name), Keyword: "ARG", Ins: ins})
			}
		case "ENV":
			words := ins.Words()
			if len(words) > 0 && !strings.Contains(words[0].Text, "=") {
				// Legacy "ENV key value".
				val := ""
				if len(words) > 1 {
					val = strings.TrimSpace(ins.Args[words[1].Start-ins.ArgsStart:])
				}
				out = append(out, VarDef{Name: words[0].Text, Value: val, Start: words[0].Start, End: words[0].End, Keyword: "ENV", Ins: ins})
				continue
			}
			for _, w := range words {
				name, val, ok := strings.Cut(w.Text, "=")
				if ok {
					out = append(out, VarDef{Name: name, Value: val, Start: w.Start, End: w.Start + len(name), Keyword: "ENV", Ins: ins})
				}
			}
		}
	}
	return out
}

var varRefRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// VarRef is a $NAME or ${NAME...} use.
type VarRef struct {
	Name       string
	Start, End int // the name
}

// VarRefs finds variable uses in the arguments and flag values of every instruction.
func (f *File) VarRefs(text string) []VarRef {
	var out []VarRef
	for _, ins := range f.Instructions {
		seg := text[ins.KeywordEnd:ins.End]
		for _, m := range varRefRe.FindAllStringSubmatchIndex(seg, -1) {
			if m[0] > 0 && seg[m[0]-1] == '\\' {
				continue
			}
			out = append(out, VarRef{Name: seg[m[2]:m[3]], Start: ins.KeywordEnd + m[2], End: ins.KeywordEnd + m[3]})
		}
	}
	return out
}
