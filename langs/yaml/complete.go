package yaml

import (
	"context"
	"strconv"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
	"github.com/xsak/bin-language-servers/internal/yamlast"
)

// Completion works on lines and indentation instead of the parse tree, because the document
// is usually not valid YAML while a key is being typed.

// lineInfo is the block structure of one line.
type lineInfo struct {
	indent   int
	dashes   []int // columns of "- " sequence markers
	keyCol   int   // column of the mapping key, -1 if none
	key      string
	colon    int // column of the ':' after the key
	hasValue bool
	blank    bool // empty or comment-only
	docSep   bool
}

func parseLine(line string) lineInfo {
	li := lineInfo{keyCol: -1, colon: -1}
	t := strings.TrimRight(line, "\r")
	if t == "---" || strings.HasPrefix(t, "--- ") || t == "..." {
		li.docSep = true
		return li
	}
	i := 0
	for i < len(t) && t[i] == ' ' {
		i++
	}
	li.indent = i
	for i < len(t) && t[i] == '-' && (i+1 == len(t) || t[i+1] == ' ') {
		li.dashes = append(li.dashes, i)
		i++
		for i < len(t) && t[i] == ' ' {
			i++
		}
	}
	rest := t[i:]
	if rest == "" || strings.HasPrefix(rest, "#") {
		li.blank = len(li.dashes) == 0
		return li
	}
	if c := keyColon(rest); c >= 0 {
		li.keyCol = i
		li.colon = i + c
		li.key = strings.Trim(strings.TrimSpace(rest[:c]), `"'`)
		v := strings.TrimSpace(stripComment(rest[c+1:]))
		li.hasValue = v != "" && !strings.HasPrefix(v, "&") && !strings.HasPrefix(v, "!") &&
			v != "|" && v != ">" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">")
	}
	return li
}

// keyColon returns the index of the ':' that ends a mapping key in s, or -1.
func keyColon(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			if i == 0 {
				quote = c
			}
		case c == '#' && i > 0 && s[i-1] == ' ':
			return -1
		case c == '{' || c == '[':
			if i == 0 {
				return -1 // flow collection
			}
		case c == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t'):
			return i
		}
	}
	return -1
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	if strings.HasPrefix(s, "#") {
		return ""
	}
	return s
}

// parentPath walks up from line ln to find the path of the mapping that holds a key at
// column col. curDashes are "- " markers before col on line ln itself.
func parentPath(lines []string, ln, col int, curDashes []int) tree.Path {
	var rev []tree.Segment
	cur := col
	afterDash := false
	for i := len(curDashes) - 1; i >= 0; i-- {
		if curDashes[i] < cur {
			rev = append(rev, tree.IndexSeg(-1))
			cur, afterDash = curDashes[i], true
		}
	}
	for l := ln - 1; l >= 0 && (cur > 0 || afterDash); l-- {
		li := parseLine(lines[l])
		if li.docSep {
			break
		}
		if li.blank {
			continue
		}
		if li.keyCol >= 0 && (li.keyCol < cur || (afterDash && li.keyCol == cur && len(li.dashes) == 0 && !li.hasValue)) {
			rev = append(rev, tree.KeySeg(li.key))
			cur, afterDash = li.keyCol, false
		}
		for i := len(li.dashes) - 1; i >= 0; i-- {
			if li.dashes[i] < cur {
				rev = append(rev, tree.IndexSeg(-1))
				cur, afterDash = li.dashes[i], true
			}
		}
	}
	path := make(tree.Path, len(rev))
	for i, s := range rev {
		path[len(rev)-1-i] = s
	}
	return path
}

// siblingKeys collects the keys already present in the mapping that has keys at column col.
func siblingKeys(lines []string, ln, col int) map[string]bool {
	keys := map[string]bool{}
	firstCol := func(li lineInfo) int {
		if len(li.dashes) > 0 {
			return li.dashes[0]
		}
		if li.keyCol >= 0 {
			return li.keyCol
		}
		return li.indent
	}
	for l := ln - 1; l >= 0; l-- {
		li := parseLine(lines[l])
		if li.docSep {
			break
		}
		if li.blank {
			continue
		}
		if li.keyCol == col {
			keys[li.key] = true
		}
		if firstCol(li) < col {
			break
		}
	}
	for l := ln + 1; l < len(lines); l++ {
		li := parseLine(lines[l])
		if li.docSep {
			break
		}
		if li.blank {
			continue
		}
		if firstCol(li) < col {
			break
		}
		if li.keyCol == col {
			keys[li.key] = true
		}
	}
	return keys
}

func (s *Server) Complete(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	off := doc.Offset(pos)
	lines := strings.Split(doc.Text, "\n")
	ln := int(pos.Line)
	if ln >= len(lines) {
		return nil, nil
	}
	line := strings.TrimRight(lines[ln], "\r")
	lineStart := doc.Offset(protocol.Position{Line: pos.Line})
	col := min(off-lineStart, len(line))
	before := line[:col]

	// The schema needs a document root; use the parsed document if the text is valid,
	// otherwise an empty one (Kubernetes detection then can't work, the rest still does).
	var root *tree.Node
	if d := docAt(yamlast.Parse(doc.Text), off); d != nil && d.Root != nil {
		root = d.Root
	} else {
		root = fallbackRoot(lines, ln)
	}
	sch := s.schemaFor(ctx, doc, root, schemalang.FeatureWait)
	if sch == nil {
		return nil, nil
	}
	value := root.ToValue()
	if root.Kind != tree.Object {
		value = nil
	}

	cur := parseLine(before)
	var items []protocol.CompletionItem
	var start, end int

	if cur.keyCol >= 0 {
		// Value after "key: ".
		path := parentPath(lines, ln, cur.keyCol, cur.dashes).Append(tree.KeySeg(cur.key))
		vstart := cur.colon + 1
		for vstart < len(line) && line[vstart] == ' ' {
			vstart++
		}
		vend := len(strings.TrimRight(stripComment(line[min(vstart, len(line)):]), " ")) + vstart
		start, end = lineStart+min(vstart, col), lineStart+max(vend, col)
		items = schemalang.ValueItems(sch.Describe(sch.At(path, value)), renderValue)
	} else {
		word := before[len(strings.TrimRight(before, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.$/\"'")):]
		keyCol := col - len(word)
		if strings.TrimSpace(before[:keyCol]) != "" && len(cur.dashes) == 0 {
			return nil, nil // inside a plain scalar
		}
		path := parentPath(lines, ln, keyCol, cur.dashes)
		after := line[col:]
		rest := strings.TrimLeft(after, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.$/\"'")
		hasColon := strings.HasPrefix(strings.TrimLeft(rest, " "), ":")
		start, end = lineStart+keyCol, lineStart+col+(len(after)-len(rest))

		existing := siblingKeys(lines, ln, keyCol)
		delete(existing, strings.Trim(word+after[:len(after)-len(rest)], `"'`))
		unit := indentUnit(lines)
		snippets := s.Env.SnippetSupport
		props := sch.Properties(path, value)
		items = schemalang.PropertyItems(props, existing, func(p schema.PropertyInfo) (string, bool) {
			if hasColon {
				return p.Name, false
			}
			if !snippets {
				return p.Name + ": ", false
			}
			return keySnippet(p, keyCol, unit), true
		})
		if len(cur.dashes) > 0 {
			// "- |": the item may also be a scalar.
			items = append(items, schemalang.ValueItems(sch.Describe(sch.At(path, value)), renderValue)...)
		}
	}

	r := doc.Range(start, end)
	for i := range items {
		items[i].TextEdit = &protocol.TextEdit{Range: r, NewText: items[i].InsertText}
		items[i].InsertTextMode = protocol.InsertTextAsIs
	}
	return &protocol.CompletionList{Items: items}, nil
}

// fallbackRoot builds a minimal root with apiVersion/kind from the raw lines of the current
// document, so Kubernetes schemas still apply while the document doesn't parse.
func fallbackRoot(lines []string, ln int) *tree.Node {
	root := &tree.Node{Kind: tree.Object}
	from := 0
	for l := ln; l >= 0; l-- {
		if parseLine(lines[l]).docSep {
			from = l + 1
			break
		}
	}
	for l := from; l < len(lines); l++ {
		li := parseLine(lines[l])
		if li.docSep {
			break
		}
		if li.keyCol == 0 && (li.key == "apiVersion" || li.key == "kind") {
			v := strings.Trim(strings.TrimSpace(stripComment(strings.TrimRight(lines[l], "\r")[li.colon+1:])), `"'`)
			root.Props = append(root.Props, &tree.Prop{Key: li.key, Colon: -1,
				Value: &tree.Node{Kind: tree.String, Value: v}})
		}
	}
	return root
}

func indentUnit(lines []string) int {
	for _, l := range lines {
		li := parseLine(l)
		if !li.blank && !li.docSep && li.indent > 0 {
			return li.indent
		}
	}
	return 2
}

func keySnippet(p schema.PropertyInfo, col, unit int) string {
	name := p.Name
	if len(p.Enum) > 0 {
		choices := make([]string, len(p.Enum))
		for i, v := range p.Enum {
			choices[i] = schemalang.ChoiceEscape(renderValue(v))
		}
		return name + ": ${1|" + strings.Join(choices, ",") + "|}"
	}
	if p.Default != nil {
		if d := *p.Default; d != nil && !isContainer(d) {
			return name + ": ${1:" + schemalang.SnippetEscape(renderValue(d)) + "}"
		}
	}
	pad := strings.Repeat(" ", col+unit)
	t := ""
	if len(p.Types) == 1 {
		t = p.Types[0]
	}
	switch t {
	case "object":
		return name + ":\n" + pad + "$0"
	case "array":
		return name + ":\n" + pad + "- $0"
	case "boolean":
		return name + ": ${1|true,false|}"
	}
	return name + ": $0"
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// renderValue writes a scalar as YAML, quoting strings that would otherwise change type.
func renderValue(v any) string {
	s, ok := v.(string)
	if !ok {
		return schema.JSONLiteral(v)
	}
	if s == "" || needsQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsQuote(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", "y", "n":
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	if strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") || strings.Contains(s, ": ") ||
		strings.Contains(s, " #") || strings.HasSuffix(s, ":") || strings.TrimSpace(s) != s {
		return true
	}
	return false
}
