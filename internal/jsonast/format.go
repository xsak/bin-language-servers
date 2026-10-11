package jsonast

import (
	"strings"
)

// FormatOptions controls Format.
type FormatOptions struct {
	Indent string // e.g. "  " or "\t"
	EOL    string // "\n" or "\r\n"
	// FinalNewline adds a line break at the end of the document.
	FinalNewline bool
}

// Format pretty-prints JSON/JSONC from its tokens, keeping comments and at most one blank line
// between members. It returns ok=false when the document has syntax errors, so a broken
// document is never rewritten.
func Format(src string, opts FormatOptions) (string, bool) {
	res := Parse(src, Options{Comments: true})
	for _, p := range res.Problems {
		if !p.Warning {
			return "", false
		}
	}
	if opts.EOL == "" {
		opts.EOL = "\n"
	}
	toks := res.Tokens

	var b strings.Builder
	depth := 0
	newline := func(extraBlank bool) {
		b.WriteString(opts.EOL)
		if extraBlank {
			b.WriteString(opts.EOL)
		}
		b.WriteString(strings.Repeat(opts.Indent, depth))
	}
	linesBetween := func(i int) int {
		if i == 0 {
			return 0
		}
		return strings.Count(src[toks[i-1].End:toks[i].Start], "\n")
	}

	for i, t := range toks {
		prev := TokEOF
		if i > 0 {
			prev = toks[i-1].Kind
		}
		gap := linesBetween(i)
		text := t.Text(src)

		switch {
		case i == 0:
		case t.Kind == TokLineComment || t.Kind == TokBlockComment:
			if gap == 0 {
				b.WriteString(" ")
			} else {
				newline(gap > 1 && prev != TokLBrace && prev != TokLBracket)
			}
		case t.Kind == TokRBrace || t.Kind == TokRBracket:
			depth--
			if prev == TokLBrace || prev == TokLBracket {
				// Empty container stays on one line: {}
			} else {
				newline(false)
			}
		case prev == TokLBrace || prev == TokLBracket:
			newline(false)
		case prev == TokComma:
			newline(gap > 1)
		case prev == TokColon:
			b.WriteString(" ")
		case prev == TokLineComment:
			newline(gap > 1)
		case prev == TokBlockComment:
			if gap == 0 {
				b.WriteString(" ")
			} else {
				newline(gap > 1)
			}
		}

		if t.Kind == TokBlockComment {
			b.WriteString(reindentBlock(text, opts.EOL))
		} else {
			b.WriteString(text)
		}
		if t.Kind == TokLBrace || t.Kind == TokLBracket {
			depth++
		}
	}
	if opts.FinalNewline && b.Len() > 0 {
		b.WriteString(opts.EOL)
	}
	return b.String(), true
}

func reindentBlock(text, eol string) string {
	if eol == "\n" {
		return strings.ReplaceAll(text, "\r\n", "\n")
	}
	return text
}
