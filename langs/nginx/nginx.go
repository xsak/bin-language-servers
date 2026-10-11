// Package nginx is the nginx configuration module: syntax checks, unknown directives and
// directives in the wrong context (from the nginx.org directive reference), completion of
// directives allowed in the current block, argument choices and variables, hover, outline,
// re-indenting formatter, and go-to-definition for include files and upstreams.
package nginx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xsak/bin-language-servers/internal/confdata"
	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func init() {
	lang.Register(lang.Info{
		Name:        "nginx",
		Description: "nginx config: directive checks by context (nginx.org reference), completion, hover, formatting",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server implements the module.
type Server struct {
	Env lang.Env
}

func (s *Server) Name() string { return "nginx" }

func (s *Server) Init(_ context.Context, env lang.Env) error {
	s.Env = env
	return nil
}

func (s *Server) TriggerCharacters() []string { return []string{"$"} }

// Stmt is a directive with its arguments and optional block.
type Stmt struct {
	Name                string
	NStart, NEnd        int
	Args                []Word
	End                 int // after ';' or '}'
	Block               []*Stmt
	BlockOpen, BlockEnd int // '{' and after '}'; -1 without a block
	Parent              *Stmt
}

// Word is an argument.
type Word struct {
	Text       string // unquoted
	Start, End int
}

// Config is a parsed file.
type Config struct {
	Stmts    []*Stmt
	Problems []problem
}

type problem struct {
	start, end int
	msg        string
	sev        protocol.DiagnosticSeverity
}

type parser struct {
	src string
	i   int
	cfg *Config
}

// Parse parses nginx configuration.
func Parse(src string) *Config {
	p := &parser{src: src, cfg: &Config{}}
	p.cfg.Stmts = p.block(nil)
	return p.cfg
}

func (p *parser) problem(start, end int, sev protocol.DiagnosticSeverity, format string, args ...any) {
	if end <= start {
		end = min(start+1, len(p.src))
	}
	p.cfg.Problems = append(p.cfg.Problems, problem{start, end, fmt.Sprintf(format, args...), sev})
}

// word reads the next token; kind is 'w' for a word, or the punctuation byte.
func (p *parser) token() (Word, byte) {
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.i++
		case c == '#':
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
		case c == ';' || c == '{' || c == '}':
			p.i++
			return Word{Start: p.i - 1, End: p.i}, c
		case c == '"' || c == '\'':
			start := p.i
			p.i++
			var b strings.Builder
			for p.i < len(p.src) && p.src[p.i] != c {
				if p.src[p.i] == '\\' && p.i+1 < len(p.src) {
					p.i++
				}
				b.WriteByte(p.src[p.i])
				p.i++
			}
			if p.i >= len(p.src) {
				p.problem(start, start+1, protocol.SeverityError, "Unterminated string.")
			} else {
				p.i++
			}
			return Word{Text: b.String(), Start: start, End: p.i}, 'w'
		default:
			start := p.i
			depth := 0 // ${var} and regex braces inside words
			for p.i < len(p.src) {
				c := p.src[p.i]
				if c == '\\' && p.i+1 < len(p.src) {
					p.i += 2
					continue
				}
				if c == '{' && p.i > start && p.src[p.i-1] == '$' {
					depth++
				} else if c == '}' && depth > 0 {
					depth--
				} else if depth == 0 && (c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ';' || c == '{' || c == '}' || c == '#' && p.i == start) {
					break
				}
				p.i++
			}
			return Word{Text: p.src[start:p.i], Start: start, End: p.i}, 'w'
		}
	}
	return Word{Start: len(p.src), End: len(p.src)}, 0
}

func (p *parser) block(parent *Stmt) []*Stmt {
	var out []*Stmt
	for {
		w, kind := p.token()
		switch kind {
		case 0:
			if parent != nil {
				p.problem(parent.NStart, parent.NEnd, protocol.SeverityError, "Block %q is not closed with '}'.", parent.Name)
			}
			return out
		case '}':
			if parent == nil {
				p.problem(w.Start, w.End, protocol.SeverityError, "Unexpected '}'.")
				continue
			}
			parent.BlockEnd = w.End
			parent.End = w.End
			return out
		case ';', '{':
			p.problem(w.Start, w.End, protocol.SeverityError, "Directive name expected before '%c'.", kind)
			continue
		}
		st := &Stmt{Name: w.Text, NStart: w.Start, NEnd: w.End, BlockOpen: -1, BlockEnd: -1, Parent: parent}
		out = append(out, st)
	args:
		for {
			save := p.i
			a, k := p.token()
			switch k {
			case 'w':
				st.Args = append(st.Args, a)
			case ';':
				st.End = a.End
				break args
			case '{':
				st.BlockOpen = a.Start
				st.Block = p.block(st)
				if st.BlockEnd < 0 {
					st.End = len(p.src)
				}
				break args
			default: // '}' or EOF: missing ';'
				end := st.NEnd
				if len(st.Args) > 0 {
					end = st.Args[len(st.Args)-1].End
				}
				p.problem(st.NStart, end, protocol.SeverityError, "Missing ';' after %q.", st.Name)
				st.End = end
				p.i = save
				break args
			}
		}
	}
}

// Walk visits every statement.
func Walk(stmts []*Stmt, fn func(*Stmt)) {
	for _, s := range stmts {
		fn(s)
		Walk(s.Block, fn)
	}
}

// opaqueBlocks contain data lines (key value;) rather than directives.
var opaqueBlocks = map[string]bool{"map": true, "geo": true, "split_clients": true, "types": true,
	"charset_map": true, "match": true}

// contextOf names the context a statement sits in, as the nginx docs do ("main", "http",
// "server", "location", "if in location", ...). "" means unknown (a partial file that is
// included somewhere).
func contextOf(st *Stmt, partial bool) string {
	p := st.Parent
	if p == nil {
		if partial {
			return ""
		}
		return "main"
	}
	if p.Name == "if" && p.Parent != nil {
		return "if in " + p.Parent.Name
	}
	return p.Name
}

// isPartial reports whether a file looks like an included fragment rather than nginx.conf.
func isPartial(cfg *Config) bool {
	for _, st := range cfg.Stmts {
		switch st.Name {
		case "http", "events", "worker_processes", "user", "pid", "stream", "mail", "error_log":
			return false
		}
	}
	return true
}

// family returns the module prefix of the top-level block a statement is in ("ngx_http_",
// "ngx_stream_", "ngx_mail_"), or "" outside them.
func family(parent *Stmt) string {
	var root *Stmt
	for p := parent; p != nil; p = p.Parent {
		root = p
	}
	if root == nil {
		return ""
	}
	switch root.Name {
	case "http", "stream", "mail":
		return "ngx_" + root.Name + "_"
	}
	return ""
}

// inFamily keeps directives of the block family, plus core directives usable anywhere.
func inFamily(d confdata.Directive, fam string) bool {
	if fam == "" {
		return true
	}
	if strings.HasPrefix(d.Module, fam) {
		return true
	}
	for _, f := range []string{"ngx_http_", "ngx_stream_", "ngx_mail_"} {
		if strings.HasPrefix(d.Module, f) {
			return false
		}
	}
	return true
}

// lookup returns the directive entries that apply in the block family of parent.
func lookup(name string, parent *Stmt) []confdata.Directive {
	var out []confdata.Directive
	fam := family(parent)
	for _, d := range confdata.Nginx().Lookup(name) {
		if inFamily(d, fam) {
			out = append(out, d)
		}
	}
	return out
}

// inOpaque reports whether a statement is inside a data block like map or types.
func inOpaque(st *Stmt) bool {
	for p := st.Parent; p != nil; p = p.Parent {
		if opaqueBlocks[p.Name] {
			return true
		}
	}
	return false
}

func (s *Server) Diagnose(_ context.Context, doc *document.Doc) []protocol.Diagnostic {
	cfg := Parse(doc.Text)
	var out []protocol.Diagnostic
	add := func(start, end int, sev protocol.DiagnosticSeverity, msg string) {
		out = append(out, protocol.Diagnostic{Range: doc.Range(start, end), Severity: sev, Source: "bls", Message: msg})
	}
	for _, p := range cfg.Problems {
		add(p.start, p.end, p.sev, p.msg)
	}
	partial := isPartial(cfg)
	table := confdata.Nginx()
	Walk(cfg.Stmts, func(st *Stmt) {
		if inOpaque(st) || strings.HasPrefix(st.Name, "$") {
			return
		}
		if len(table.Lookup(st.Name)) == 0 {
			add(st.NStart, st.NEnd, protocol.SeverityWarning, fmt.Sprintf("Unknown directive %q (not in the nginx.org reference; third-party module?).", st.Name))
			return
		}
		ctx := contextOf(st, partial)
		if ctx == "" {
			return
		}
		entries := lookup(st.Name, st.Parent)
		var allowed []string
		for _, e := range entries {
			if hasContext(e, ctx) {
				return
			}
			allowed = append(allowed, e.Contexts...)
		}
		add(st.NStart, st.NEnd, protocol.SeverityWarning, fmt.Sprintf("%q is not allowed in %s (allowed in: %s).", st.Name, ctx, strings.Join(unique(allowed), ", ")))
	})
	return out
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// stmtAt returns the innermost statement whose range contains off, and the block statement
// whose braces contain off (the completion context).
func stmtAt(stmts []*Stmt, off int) (hit *Stmt, enclosing *Stmt) {
	for _, st := range stmts {
		if st.BlockOpen >= 0 && off > st.BlockOpen && (st.BlockEnd < 0 || off < st.BlockEnd) {
			h, e := stmtAt(st.Block, off)
			if e == nil {
				e = st
			}
			return h, e
		}
		if off >= st.NStart && off <= st.End {
			return st, nil
		}
	}
	return nil, nil
}

var commonVariables = strings.Fields(`$host $hostname $remote_addr $remote_port $remote_user $binary_remote_addr
$request $request_uri $uri $document_uri $args $query_string $is_args $request_method $request_body
$request_time $scheme $server_name $server_addr $server_port $server_protocol $status $body_bytes_sent
$bytes_sent $content_type $content_length $http_host $http_user_agent $http_referer $http_x_forwarded_for
$http_upgrade $connection_upgrade $proxy_add_x_forwarded_for $proxy_host $proxy_port $upstream_addr
$upstream_status $upstream_response_time $realpath_root $document_root $time_local $time_iso8601 $msec
$ssl_protocol $ssl_cipher $ssl_server_name $cookie_ $arg_ $http_ $sent_http_ $request_id $pid $nginx_version`)

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	cfg := Parse(doc.Text)
	off := doc.Offset(pos)
	ws := off
	for ws > 0 && (isWordChar(doc.Text[ws-1])) {
		ws--
	}
	r := doc.Range(ws, off)
	var items []protocol.CompletionItem

	// Variables after '$'.
	if ws > 0 && doc.Text[ws-1] == '$' || ws > 1 && doc.Text[ws-2:ws] == "${" {
		start := ws
		names := map[string]bool{}
		Walk(cfg.Stmts, func(st *Stmt) {
			if (st.Name == "set" || st.Name == "map" || st.Name == "geo") && len(st.Args) > 0 {
				for _, a := range st.Args {
					if strings.HasPrefix(a.Text, "$") {
						names[a.Text] = true
					}
				}
			}
		})
		for _, v := range commonVariables {
			names[v] = true
		}
		var sorted []string
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			items = append(items, protocol.CompletionItem{Label: n, Kind: protocol.CompletionKindVariable,
				TextEdit: &protocol.TextEdit{Range: doc.Range(start, off), NewText: strings.TrimPrefix(n, "$")}})
		}
		return &protocol.CompletionList{Items: items}, nil
	}

	_, enclosing := stmtAt(cfg.Stmts, off)
	table := confdata.Nginx()

	// Argument position (a directive name and a space before the cursor on this line): offer
	// choices from the syntax ("on | off").
	lineStart := strings.LastIndexByte(doc.Text[:ws], '\n') + 1
	if fields := strings.Fields(doc.Text[lineStart:ws]); len(fields) > 0 && !strings.ContainsAny(doc.Text[lineStart:ws], "{};") {
		for _, e := range table.Lookup(fields[0]) {
			for _, c := range choices(e.Syntax) {
				items = append(items, protocol.CompletionItem{Label: c, Kind: protocol.CompletionKindValue,
					TextEdit: &protocol.TextEdit{Range: r, NewText: c}})
			}
		}
		return &protocol.CompletionList{Items: items}, nil
	}

	// Directive position: directives allowed in the enclosing block.
	ctx := "main"
	if enclosing != nil {
		fake := &Stmt{Parent: enclosing}
		ctx = contextOf(fake, false)
		if opaqueBlocks[enclosing.Name] {
			return nil, nil
		}
	} else if isPartial(cfg) {
		ctx = ""
	}
	seen := map[string]bool{}
	fam := family(enclosing)
	for _, d := range table.All {
		if seen[d.Name] || ctx != "" && !hasContext(d, ctx) || !inFamily(d, fam) {
			continue
		}
		seen[d.Name] = true
		text, snip := d.Name+" ", false
		if s.Env.SnippetSupport {
			snip = true
			if isBlock(d) {
				text = d.Name + " ${1}{\n\t$0\n}"
				if d.Name == "http" || d.Name == "events" || d.Name == "server" || d.Name == "stream" || d.Name == "mail" {
					text = d.Name + " {\n\t$0\n}"
				}
			} else {
				text = d.Name + " $0;"
			}
		}
		it := protocol.CompletionItem{Label: d.Name, Kind: protocol.CompletionKindKeyword, Detail: d.Module,
			Documentation: &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: d.Markdown()},
			TextEdit:      &protocol.TextEdit{Range: r, NewText: text}}
		if snip {
			it.InsertTextFormat = protocol.InsertTextSnippet
			it.InsertTextMode = protocol.InsertTextAdjustIndentation
		}
		items = append(items, it)
	}
	return &protocol.CompletionList{Items: items}, nil
}

func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// hasContext matches the docs' context names; the rewrite module says just "if" where others
// say "if in location" / "if in server".
func hasContext(d confdata.Directive, ctx string) bool {
	for _, c := range d.Contexts {
		if c == ctx || c == "any" || c == "if" && strings.HasPrefix(ctx, "if in ") {
			return true
		}
	}
	return false
}

func isBlock(d confdata.Directive) bool {
	for _, s := range d.Syntax {
		if strings.Contains(s, "{ ... }") || strings.HasSuffix(strings.TrimSpace(s), "{ ... }") {
			return true
		}
	}
	return false
}

// choices extracts literal alternatives from a syntax like "gzip on | off;".
func choices(syntax []string) []string {
	var out []string
	for _, s := range syntax {
		f := strings.Fields(strings.TrimSuffix(strings.TrimSpace(s), ";"))
		if len(f) < 4 || !strings.Contains(s, "|") {
			continue
		}
		for _, p := range strings.Split(strings.Join(f[1:], " "), "|") {
			p = strings.TrimSpace(p)
			if p != "" && !strings.ContainsAny(p, " []<>.") && !strings.HasPrefix(p, "$") {
				out = append(out, p)
			}
		}
	}
	return unique(out)
}

func (s *Server) Hover(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	cfg := Parse(doc.Text)
	off := doc.Offset(pos)
	var found *Stmt
	Walk(cfg.Stmts, func(st *Stmt) {
		if off >= st.NStart && off <= st.NEnd {
			found = st
		}
	})
	if found == nil {
		return nil, nil
	}
	entries := lookup(found.Name, found.Parent)
	if len(entries) == 0 {
		return nil, nil
	}
	best := entries[0]
	ctx := contextOf(found, isPartial(cfg))
	for _, e := range entries {
		if hasContext(e, ctx) {
			best = e
			break
		}
	}
	r := doc.Range(found.NStart, found.NEnd)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: best.Markdown()}, Range: &r}, nil
}

func (s *Server) Symbols(_ context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	cfg := Parse(doc.Text)
	var build func([]*Stmt) []protocol.DocumentSymbol
	build = func(stmts []*Stmt) []protocol.DocumentSymbol {
		var out []protocol.DocumentSymbol
		for _, st := range stmts {
			if st.BlockOpen < 0 {
				continue
			}
			name := st.Name
			for _, a := range st.Args {
				name += " " + a.Text
			}
			detail := ""
			if st.Name == "server" {
				for _, c := range st.Block {
					if c.Name == "server_name" || c.Name == "listen" {
						for _, a := range c.Args {
							detail += a.Text + " "
						}
					}
				}
			}
			out = append(out, protocol.DocumentSymbol{Name: name, Detail: strings.TrimSpace(detail), Kind: protocol.SymbolNamespace,
				Range: doc.Range(st.NStart, st.End), SelectionRange: doc.Range(st.NStart, st.NEnd), Children: build(st.Block)})
		}
		return out
	}
	return build(cfg.Stmts), nil
}

// Format re-indents by brace depth, trims trailing whitespace and collapses blank-line runs.
// Text on each line is kept; multi-line strings are left alone. Broken files are not touched.
func (s *Server) Format(_ context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	cfg := Parse(doc.Text)
	for _, p := range cfg.Problems {
		if p.sev == protocol.SeverityError {
			return nil, nil
		}
	}
	indent := "\t"
	if opts.InsertSpaces {
		indent = strings.Repeat(" ", int(max(opts.TabSize, 1)))
	}
	out := reindent(doc.Text, indent)
	if out == doc.Text {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: doc.Range(0, len(doc.Text)), NewText: out}}, nil
}

// reindent indents lines by the number of unclosed '{' before them (ignoring braces in strings,
// comments and ${var}).
func reindent(src, indent string) string {
	eol := "\n"
	if strings.Contains(src, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	depth, blank := 0, 0
	var quote byte
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if quote != 0 { // inside a multi-line string
			out = append(out, strings.TrimRight(l, " \t"))
		} else if t == "" {
			blank++
			continue
		} else {
			if blank > 0 && len(out) > 0 {
				out = append(out, "")
			}
			d := depth
			if strings.HasPrefix(t, "}") {
				d = max(depth-1, 0)
			}
			out = append(out, strings.Repeat(indent, d)+t)
		}
		blank = 0
		for i := 0; i < len(l); i++ {
			c := l[i]
			switch {
			case c == '\\' && quote == 0:
				i++ // escaped character, e.g. \{ in a regex
			case quote != 0:
				if c == '\\' {
					i++
				} else if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '#':
				i = len(l)
			case c == '{' && i > 0 && l[i-1] == '$':
				for i < len(l) && l[i] != '}' {
					i++
				}
			case c == '{':
				depth++
			case c == '}':
				depth = max(depth-1, 0)
			}
		}
	}
	return strings.Join(out, eol) + eol
}

// Definition opens include files and jumps from proxy_pass/fastcgi_pass targets to upstreams.
func (s *Server) Definition(_ context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	cfg := Parse(doc.Text)
	off := doc.Offset(pos)
	var out []protocol.Location
	Walk(cfg.Stmts, func(st *Stmt) {
		for _, a := range st.Args {
			if off < a.Start || off > a.End {
				continue
			}
			switch st.Name {
			case "include":
				pattern := a.Text
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(filepath.Dir(doc.Path()), pattern)
				}
				matches, _ := filepath.Glob(pattern)
				for _, m := range matches {
					if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
						out = append(out, protocol.Location{URI: document.PathToURI(m)})
					}
				}
			case "proxy_pass", "fastcgi_pass", "uwsgi_pass", "scgi_pass", "grpc_pass", "memcached_pass":
				host := a.Text
				if i := strings.Index(host, "://"); i >= 0 {
					host = host[i+3:]
				}
				host, _, _ = strings.Cut(host, "/")
				Walk(cfg.Stmts, func(u *Stmt) {
					if u.Name == "upstream" && len(u.Args) > 0 && u.Args[0].Text == host {
						out = append(out, protocol.Location{URI: doc.URI, Range: doc.Range(u.Args[0].Start, u.Args[0].End)})
					}
				})
			}
		}
	})
	return out, nil
}
