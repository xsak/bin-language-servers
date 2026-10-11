// Package jsonast is an error-tolerant JSON / JSONC parser that produces a tree.Node with byte
// ranges. It keeps going after errors so that completion and hover work while a document is
// being edited.
package jsonast

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xsak/bin-language-servers/internal/tree"
)

// Problem is a syntax error or warning.
type Problem struct {
	Start, End int
	Message    string
	Warning    bool
}

// Options controls dialect details.
type Options struct {
	// Comments allows // and /* */ comments and trailing commas (JSONC).
	Comments bool
}

// Result is the outcome of a parse.
type Result struct {
	Root     *tree.Node // nil for an empty document
	Problems []Problem
	Tokens   []Token // all tokens including comments, used by the formatter
}

// TokenKind classifies tokens.
type TokenKind int

const (
	TokEOF TokenKind = iota
	TokLBrace
	TokRBrace
	TokLBracket
	TokRBracket
	TokColon
	TokComma
	TokString
	TokNumber
	TokTrue
	TokFalse
	TokNull
	TokLineComment
	TokBlockComment
	TokInvalid
)

// Token is a lexical token.
type Token struct {
	Kind       TokenKind
	Start, End int
}

// Text returns the token text.
func (t Token) Text(src string) string { return src[t.Start:t.End] }

// Lex splits src into tokens, including comments. Unterminated strings and comments end at
// the end of the line / document.
func Lex(src string) []Token {
	var toks []Token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case c == '{':
			toks = append(toks, Token{TokLBrace, i, i + 1})
		case c == '}':
			toks = append(toks, Token{TokRBrace, i, i + 1})
		case c == '[':
			toks = append(toks, Token{TokLBracket, i, i + 1})
		case c == ']':
			toks = append(toks, Token{TokRBracket, i, i + 1})
		case c == ':':
			toks = append(toks, Token{TokColon, i, i + 1})
		case c == ',':
			toks = append(toks, Token{TokComma, i, i + 1})
		case c == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(src) && src[j] == '"' {
				j++
			}
			toks = append(toks, Token{TokString, i, min(j, len(src))})
			i = min(j, len(src))
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = len(src) - i
			}
			end := i + j
			if end > i && src[end-1] == '\r' {
				end--
			}
			toks = append(toks, Token{TokLineComment, i, end})
			i += j
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			toks = append(toks, Token{TokBlockComment, i, end})
			i = end
			continue
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(src) && strings.IndexByte("0123456789.eE+-", src[j]) >= 0 {
				j++
			}
			toks = append(toks, Token{TokNumber, i, j})
			i = j
			continue
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			kind := TokInvalid
			switch src[i:j] {
			case "true":
				kind = TokTrue
			case "false":
				kind = TokFalse
			case "null":
				kind = TokNull
			}
			toks = append(toks, Token{kind, i, j})
			i = j
			continue
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			toks = append(toks, Token{TokInvalid, i, i + size})
			i += size
			continue
		}
		i++
	}
	return toks
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

type parser struct {
	src  string
	opts Options
	toks []Token // without comments
	pos  int
	res  *Result
}

// Parse parses src.
func Parse(src string, opts Options) *Result {
	all := Lex(src)
	res := &Result{Tokens: all}
	p := &parser{src: src, opts: opts, res: res}
	for _, t := range all {
		if t.Kind == TokLineComment || t.Kind == TokBlockComment {
			if !opts.Comments {
				p.problem(t.Start, t.End, "Comments are not permitted in JSON.", false)
			}
			continue
		}
		p.toks = append(p.toks, t)
	}
	if p.peek().Kind == TokEOF {
		return res
	}
	res.Root = p.value()
	if t := p.peek(); t.Kind != TokEOF {
		p.problem(t.Start, len(src), "End of file expected.", false)
	}
	return res
}

func (p *parser) peek() Token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return Token{Kind: TokEOF, Start: len(p.src), End: len(p.src)}
}

func (p *parser) next() Token {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

func (p *parser) problem(start, end int, msg string, warning bool) {
	if end <= start {
		end = start + 1
		if end > len(p.src) {
			start, end = max(0, len(p.src)-1), len(p.src)
		}
	}
	p.res.Problems = append(p.res.Problems, Problem{Start: start, End: end, Message: msg, Warning: warning})
}

// value parses a value; it returns nil (after reporting) when no value starts here.
func (p *parser) value() *tree.Node {
	t := p.peek()
	switch t.Kind {
	case TokLBrace:
		return p.object()
	case TokLBracket:
		return p.array()
	case TokString:
		p.next()
		return &tree.Node{Kind: tree.String, Start: t.Start, End: t.End, Value: p.unquote(t)}
	case TokNumber:
		p.next()
		text := t.Text(p.src)
		if !json.Valid([]byte(text)) {
			p.problem(t.Start, t.End, "Invalid number format.", false)
		}
		return &tree.Node{Kind: tree.Number, Start: t.Start, End: t.End, Value: json.Number(text)}
	case TokTrue, TokFalse:
		p.next()
		return &tree.Node{Kind: tree.Bool, Start: t.Start, End: t.End, Value: t.Kind == TokTrue}
	case TokNull:
		p.next()
		return &tree.Node{Kind: tree.Null, Start: t.Start, End: t.End}
	case TokInvalid:
		p.next()
		p.problem(t.Start, t.End, "Value expected.", false)
		return &tree.Node{Kind: tree.Null, Start: t.Start, End: t.End}
	}
	p.problem(t.Start, t.End, "Value expected.", false)
	return nil
}

func (p *parser) unquote(t Token) string {
	text := t.Text(p.src)
	if len(text) < 2 || text[len(text)-1] != '"' {
		p.problem(t.Start, t.End, "Unterminated string.", false)
		text = strings.TrimPrefix(text, `"`)
		return text
	}
	s, err := strconv.Unquote(text)
	if err != nil {
		var out string
		if json.Unmarshal([]byte(text), &out) == nil {
			return out
		}
		p.problem(t.Start, t.End, "Invalid escape sequence in string.", false)
		return text[1 : len(text)-1]
	}
	return s
}

func (p *parser) object() *tree.Node {
	open := p.next()
	n := &tree.Node{Kind: tree.Object, Start: open.Start}
	seen := map[string]bool{}
	for {
		t := p.peek()
		switch t.Kind {
		case TokRBrace:
			p.next()
			n.End = t.End
			return n
		case TokEOF:
			p.problem(open.Start, open.End, "Expected '}' to close this object.", false)
			n.End = len(p.src)
			return n
		case TokComma:
			p.next()
			p.problem(t.Start, t.End, "Property expected.", false)
			continue
		}

		prop := &tree.Prop{Colon: -1}
		switch t.Kind {
		case TokString:
			p.next()
			prop.Key, prop.KeyStart, prop.KeyEnd = p.unquote(t), t.Start, t.End
		case TokInvalid, TokTrue, TokFalse, TokNull, TokNumber:
			// Unquoted key while typing: keep it so completion can replace it.
			p.next()
			p.problem(t.Start, t.End, "Property keys must be doublequoted.", false)
			prop.Key, prop.KeyStart, prop.KeyEnd = t.Text(p.src), t.Start, t.End
		default:
			p.problem(t.Start, t.End, "Property expected.", false)
			p.skipTo(TokRBrace, TokComma)
			continue
		}
		if seen[prop.Key] {
			p.problem(prop.KeyStart, prop.KeyEnd, fmt.Sprintf("Duplicate object key %q.", prop.Key), true)
		}
		seen[prop.Key] = true
		n.Props = append(n.Props, prop)

		if c := p.peek(); c.Kind == TokColon {
			p.next()
			prop.Colon = c.Start
			if v := p.peek(); v.Kind == TokRBrace || v.Kind == TokComma || v.Kind == TokEOF {
				p.problem(c.Start, c.End, "Value expected.", false)
			} else {
				prop.Value = p.value()
			}
		} else {
			p.problem(prop.KeyStart, prop.KeyEnd, "Colon expected.", false)
		}

		switch c := p.peek(); c.Kind {
		case TokComma:
			p.next()
			if !p.opts.Comments && p.peek().Kind == TokRBrace {
				p.problem(c.Start, c.End, "Trailing comma.", false)
			}
		case TokRBrace, TokEOF:
		default:
			p.problem(c.Start, c.End, "Expected comma.", false)
		}
	}
}

func (p *parser) array() *tree.Node {
	open := p.next()
	n := &tree.Node{Kind: tree.Array, Start: open.Start}
	for {
		t := p.peek()
		switch t.Kind {
		case TokRBracket:
			p.next()
			n.End = t.End
			return n
		case TokEOF:
			p.problem(open.Start, open.End, "Expected ']' to close this array.", false)
			n.End = len(p.src)
			return n
		case TokComma:
			p.next()
			p.problem(t.Start, t.End, "Value expected.", false)
			continue
		case TokRBrace, TokColon:
			p.next()
			p.problem(t.Start, t.End, "Value expected.", false)
			continue
		}
		if v := p.value(); v != nil {
			n.Items = append(n.Items, v)
		}
		switch c := p.peek(); c.Kind {
		case TokComma:
			p.next()
			if !p.opts.Comments && p.peek().Kind == TokRBracket {
				p.problem(c.Start, c.End, "Trailing comma.", false)
			}
		case TokRBracket, TokEOF:
		default:
			p.problem(c.Start, c.End, "Expected comma.", false)
		}
	}
}

func (p *parser) skipTo(kinds ...TokenKind) {
	for {
		t := p.peek()
		if t.Kind == TokEOF {
			return
		}
		for _, k := range kinds {
			if t.Kind == k {
				if k == TokComma {
					p.next()
				}
				return
			}
		}
		p.next()
	}
}
