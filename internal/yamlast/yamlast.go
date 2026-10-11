// Package yamlast parses YAML (all documents of a stream) into tree.Node values with byte
// ranges, using github.com/goccy/go-yaml for the actual parsing.
package yamlast

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"

	"github.com/xsak/bin-language-servers/internal/tree"
)

// Document is one YAML document of a stream.
type Document struct {
	Root       *tree.Node // nil for an empty document
	Start, End int        // byte range of the document text
}

// Problem is a syntax error or warning.
type Problem struct {
	Start, End int
	Message    string
	Warning    bool
}

// Result is the outcome of a parse. On a syntax error Docs is empty and Problems holds the
// error, because the underlying parser stops at the first error.
type Result struct {
	Docs     []Document
	Problems []Problem
}

// Parse parses src.
func Parse(src string) *Result {
	res := &Result{}
	idx := newIndex(src)
	f, err := parser.ParseBytes([]byte(src), 0, parser.AllowDuplicateMapKey())
	if err != nil {
		res.Problems = append(res.Problems, syntaxProblem(err, idx))
		return res
	}
	b := &builder{idx: idx, src: src, res: res, anchors: map[string]*tree.Node{}}
	starts := documentStarts(src)
	for i, d := range f.Docs {
		doc := Document{Start: 0, End: len(src)}
		if i < len(starts) {
			doc.Start = starts[i]
		}
		if i+1 < len(starts) {
			doc.End = starts[i+1]
		}
		if d.Body != nil {
			doc.Root = b.node(d.Body)
		}
		res.Docs = append(res.Docs, doc)
	}
	return res
}

// documentStarts returns the start offset of each document, splitting on "---" lines.
func documentStarts(src string) []int {
	starts := []int{0}
	off := 0
	sawContent := false
	for _, line := range strings.SplitAfter(src, "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "---" || strings.HasPrefix(trimmed, "--- ") {
			if sawContent {
				starts = append(starts, off)
			} else {
				starts[0] = off
			}
			sawContent = true
		} else if t := strings.TrimSpace(trimmed); t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "%") {
			sawContent = true
		}
		off += len(line)
	}
	return starts
}

func syntaxProblem(err error, idx *index) Problem {
	var se *goyaml.SyntaxError
	if errors.As(err, &se) && se.Token != nil {
		start := idx.offset(se.Token.Position)
		end := start + max(1, len(strings.TrimSpace(se.Token.Value)))
		return Problem{Start: start, End: min(end, len(idx.src)), Message: se.Message}
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return Problem{Start: 0, End: min(1, len(idx.src)), Message: msg}
}

type builder struct {
	idx     *index
	src     string
	res     *Result
	anchors map[string]*tree.Node
}

func (b *builder) node(n ast.Node) *tree.Node {
	switch v := n.(type) {
	case *ast.MappingNode:
		out := &tree.Node{Kind: tree.Object, Start: b.idx.offset(v.Start.Position)}
		if v.IsFlowStyle {
			out.Start = b.startOf(v.Start)
		}
		seen := map[string]bool{}
		for _, mv := range v.Values {
			b.mappingValue(out, mv, seen)
		}
		out.End = b.containerEnd(out, v.End)
		if len(v.Values) > 0 && !v.IsFlowStyle {
			out.Start = min(out.Start, out.Props[0].KeyStart)
		}
		return out
	case *ast.MappingValueNode:
		// A single "key: value" pair parses without a wrapping MappingNode.
		out := &tree.Node{Kind: tree.Object}
		b.mappingValue(out, v, map[string]bool{})
		if len(out.Props) > 0 {
			out.Start = out.Props[0].KeyStart
		}
		out.End = b.containerEnd(out, nil)
		return out
	case *ast.SequenceNode:
		out := &tree.Node{Kind: tree.Array, Start: b.startOf(v.Start)}
		for _, item := range v.Values {
			if item == nil {
				continue
			}
			out.Items = append(out.Items, b.node(item))
		}
		out.End = b.containerEnd(out, v.End)
		return out
	case *ast.AnchorNode:
		val := b.node(v.Value)
		if v.Name != nil {
			b.anchors[v.Name.GetToken().Value] = val
		}
		return val
	case *ast.AliasNode:
		start := b.startOf(v.Start)
		name := ""
		if v.Value != nil {
			name = v.Value.GetToken().Value
		}
		end := start + 1 + len(name)
		if target, ok := b.anchors[name]; ok {
			cp := *target
			cp.Start, cp.End = start, end
			return &cp
		}
		b.res.Problems = append(b.res.Problems, Problem{Start: start, End: end,
			Message: fmt.Sprintf("Unknown anchor %q.", name)})
		return &tree.Node{Kind: tree.Null, Start: start, End: end}
	case *ast.TagNode:
		if v.Value == nil {
			start := b.startOf(v.Start)
			return &tree.Node{Kind: tree.Null, Start: start, End: start + len(v.Start.Value)}
		}
		inner := b.node(v.Value)
		inner.Start = min(inner.Start, b.startOf(v.Start))
		return inner
	case *ast.LiteralNode:
		start := b.startOf(v.Start)
		end := start + len(v.Start.Value)
		text := ""
		if v.Value != nil {
			text = v.Value.Value
			// Content starts on the line after the indicator.
			line := b.idx.lineOf(start)
			if line+1 < len(b.idx.lines) {
				cstart := b.idx.lines[line+1]
				end = cstart + len(strings.TrimRight(v.Value.GetToken().Origin, " \t\r\n"))
			}
		}
		return &tree.Node{Kind: tree.String, Start: start, End: min(end, len(b.src)), Value: text}
	case *ast.CommentGroupNode, nil:
		return nil
	}

	// Scalars.
	tk := n.GetToken()
	start := b.startOf(tk)
	end := start + len(strings.TrimSpace(tk.Origin))
	if end <= start {
		end = start + len(tk.Value)
	}
	end = min(end, len(b.src))
	out := &tree.Node{Start: start, End: end}
	switch v := n.(type) {
	case *ast.NullNode:
		out.Kind = tree.Null
		if strings.TrimSpace(tk.Origin) == "null" && !strings.Contains(b.src[start:end], "null") {
			// Implicit null for a missing value is positioned at the ':'; make it empty.
			out.End = out.Start
		}
	case *ast.BoolNode:
		out.Kind, out.Value = tree.Bool, v.Value
	case *ast.IntegerNode:
		out.Kind = tree.Number
		switch iv := v.Value.(type) {
		case int64:
			out.Value = json.Number(strconv.FormatInt(iv, 10))
		case uint64:
			out.Value = json.Number(strconv.FormatUint(iv, 10))
		default:
			out.Value = json.Number(fmt.Sprint(iv))
		}
	case *ast.FloatNode:
		out.Kind, out.Value = tree.Number, json.Number(strconv.FormatFloat(v.Value, 'g', -1, 64))
	case *ast.InfinityNode:
		out.Kind, out.Value = tree.Number, v.Value
	case *ast.NanNode:
		out.Kind, out.Value = tree.Number, math.NaN()
	case *ast.StringNode:
		out.Kind, out.Value = tree.String, v.Value
	default:
		out.Kind, out.Value = tree.String, tk.Value
	}
	return out
}

func (b *builder) mappingValue(obj *tree.Node, mv *ast.MappingValueNode, seen map[string]bool) {
	if mv.Key == nil {
		return
	}
	if _, merge := mv.Key.(*ast.MergeKeyNode); merge {
		// "<<: *base" or "<<: [*a, *b]": copy the referenced members.
		val := b.node(mv.Value)
		var sources []*tree.Node
		if val != nil && val.Kind == tree.Array {
			sources = val.Items
		} else if val != nil {
			sources = []*tree.Node{val}
		}
		for _, src := range sources {
			if src.Kind != tree.Object {
				continue
			}
			for _, p := range src.Props {
				if !seen[p.Key] {
					cp := *p
					obj.Props = append(obj.Props, &cp)
				}
			}
		}
		return
	}
	keyTok := mv.Key.GetToken()
	keyNode := b.node(mv.Key)
	prop := &tree.Prop{Colon: b.idx.offset(mv.Start.Position)}
	if keyNode != nil {
		prop.KeyStart, prop.KeyEnd = keyNode.Start, keyNode.End
	}
	if keyTok != nil {
		prop.Key = keyTok.Value
	}
	if seen[prop.Key] {
		b.res.Problems = append(b.res.Problems, Problem{Start: prop.KeyStart, End: prop.KeyEnd,
			Message: fmt.Sprintf("Map keys must be unique; %q is repeated.", prop.Key), Warning: true})
	}
	seen[prop.Key] = true
	if mv.Value != nil {
		prop.Value = b.node(mv.Value)
		if prop.Value != nil && prop.Value.Kind == tree.Null && prop.Value.End == prop.Value.Start {
			prop.Value.Start, prop.Value.End = prop.Colon+1, prop.Colon+1
		}
	}
	obj.Props = append(obj.Props, prop)
}

// containerEnd is the end of the last child, or of the closing bracket for flow style.
func (b *builder) containerEnd(n *tree.Node, closing *token.Token) int {
	end := n.Start
	if closing != nil {
		return b.startOf(closing) + len(closing.Value)
	}
	for _, p := range n.Props {
		end = max(end, p.KeyEnd)
		if p.Value != nil {
			end = max(end, p.Value.End)
		}
	}
	for _, it := range n.Items {
		end = max(end, it.End)
	}
	return end
}

func (b *builder) startOf(tk *token.Token) int {
	if tk == nil {
		return 0
	}
	return b.idx.offset(tk.Position)
}

// index converts go-yaml positions (1-based line, 1-based rune column) to byte offsets.
type index struct {
	src   string
	lines []int
}

func newIndex(src string) *index {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return &index{src: src, lines: lines}
}

func (x *index) offset(p *token.Position) int {
	if p == nil {
		return 0
	}
	line := p.Line - 1
	if line < 0 {
		return 0
	}
	if line >= len(x.lines) {
		return len(x.src)
	}
	off := x.lines[line]
	for col := 1; col < p.Column && off < len(x.src) && x.src[off] != '\n'; col++ {
		_, size := utf8.DecodeRuneInString(x.src[off:])
		off += size
	}
	return off
}

func (x *index) lineOf(off int) int {
	lo, hi := 0, len(x.lines)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if x.lines[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
