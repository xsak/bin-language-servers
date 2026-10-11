package json

import (
	"context"
	"fmt"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/internal/schema"
	"github.com/xsak/bin-language-servers/internal/schemalang"
	"github.com/xsak/bin-language-servers/internal/tree"
)

// completionContext says what is being completed at the cursor.
type completionContext struct {
	path     tree.Path  // object path (keys) or value path (values)
	object   *tree.Node // object whose keys are completed
	isKey    bool
	hasColon bool // the key being replaced already has a ':' after it
	// replace range; start == end means insert at the cursor
	start, end int
}

func (s *Server) Complete(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	res := s.parse(doc)
	if res.Root == nil {
		return nil, nil
	}
	off := doc.Offset(pos)
	cc, ok := locate(doc.Text, res.Root, off, nil)
	if !ok {
		return nil, nil
	}
	sch := s.schemaFor(ctx, doc, res.Root, schemalang.FeatureWait)
	if sch == nil {
		return nil, nil
	}
	value := res.Root.ToValue()
	snippets := s.Env.SnippetSupport

	var items []protocol.CompletionItem
	if cc.isKey {
		existing := schemalang.SortedKeys(cc.object)
		if cc.start != cc.end {
			// The key under the cursor is being replaced; don't hide its own name.
			delete(existing, strings.Trim(doc.Text[cc.start:cc.end], `"`))
		}
		props := sch.Properties(cc.path, value)
		items = schemalang.PropertyItems(props, existing, func(p schema.PropertyInfo) (string, bool) {
			if cc.hasColon || !snippets {
				text := fmt.Sprintf("%q", p.Name)
				if !cc.hasColon {
					text += ": "
				}
				return text, false
			}
			return fmt.Sprintf("%q: %s", p.Name, valueSnippet(p)), true
		})
	} else {
		d := sch.Describe(sch.At(cc.path, value))
		items = schemalang.ValueItems(d, schema.JSONLiteral)
	}
	if cc.start != cc.end {
		r := doc.Range(cc.start, cc.end)
		for i := range items {
			items[i].TextEdit = &protocol.TextEdit{Range: r, NewText: items[i].InsertText}
			items[i].FilterText = doc.Text[cc.start:cc.end]
			if cc.isKey {
				items[i].FilterText = fmt.Sprintf("%q", items[i].Label)
			}
		}
	}
	return &protocol.CompletionList{Items: items}, nil
}

// valueSnippet is the snippet inserted after "key": for a property.
func valueSnippet(p schema.PropertyInfo) string {
	if len(p.Enum) > 0 {
		choices := make([]string, len(p.Enum))
		for i, v := range p.Enum {
			choices[i] = schemalang.ChoiceEscape(schema.JSONLiteral(v))
		}
		return "${1|" + strings.Join(choices, ",") + "|}"
	}
	if p.Default != nil {
		lit := schema.JSONLiteral(*p.Default)
		if strings.HasPrefix(lit, `"`) {
			return `"${1:` + schemalang.SnippetEscape(strings.Trim(lit, `"`)) + `}"`
		}
		return "${1:" + schemalang.SnippetEscape(lit) + "}"
	}
	t := ""
	if len(p.Types) == 1 {
		t = p.Types[0]
	}
	switch t {
	case "object":
		return "{$1}"
	case "array":
		return "[$1]"
	case "string":
		return `"$1"`
	case "boolean":
		return "${1|true,false|}"
	case "integer", "number":
		return "${1:0}"
	case "null":
		return "null"
	}
	return "$1"
}

// locate finds the completion context at off inside n (whose path is path).
func locate(src string, n *tree.Node, off int, path tree.Path) (completionContext, bool) {
	inside := off > n.Start && (off < n.End || !closed(src, n))
	switch n.Kind {
	case tree.Object:
		if !inside {
			return completionContext{}, false
		}
		for i, p := range n.Props {
			if off >= p.KeyStart && off <= p.KeyEnd {
				return completionContext{path: path, object: n, isKey: true, hasColon: p.Colon >= 0,
					start: p.KeyStart, end: p.KeyEnd}, true
			}
			if p.Colon < 0 || off <= p.Colon {
				continue
			}
			vpath := path.Append(tree.KeySeg(p.Key))
			if p.Value != nil && off >= p.Value.Start && off <= p.Value.End {
				if p.Value.Kind == tree.Object || p.Value.Kind == tree.Array {
					if cc, ok := locate(src, p.Value, off, vpath); ok {
						return cc, true
					}
					continue
				}
				return completionContext{path: vpath, start: p.Value.Start, end: p.Value.End}, true
			}
			// After the colon with no value yet, and no comma typed since.
			limit := len(src)
			if i+1 < len(n.Props) {
				limit = n.Props[i+1].KeyStart
			}
			if p.Value == nil && off <= limit && !strings.ContainsAny(src[p.Colon+1:off], ",}") {
				return completionContext{path: vpath, start: off, end: off}, true
			}
		}
		return completionContext{path: path, object: n, isKey: true, start: off, end: off}, true
	case tree.Array:
		if !inside {
			return completionContext{}, false
		}
		idx := 0
		for i, it := range n.Items {
			if off >= it.Start && off <= it.End {
				ipath := path.Append(tree.IndexSeg(i))
				if it.Kind == tree.Object || it.Kind == tree.Array {
					return locate(src, it, off, ipath)
				}
				return completionContext{path: ipath, start: it.Start, end: it.End}, true
			}
			if it.End <= off {
				idx = i + 1
			}
		}
		return completionContext{path: path.Append(tree.IndexSeg(idx)), start: off, end: off}, true
	}
	// Scalar root.
	return completionContext{path: path, start: n.Start, end: n.End}, true
}

func closed(src string, n *tree.Node) bool {
	if n.End == 0 || n.End > len(src) {
		return false
	}
	c := src[n.End-1]
	return (n.Kind == tree.Object && c == '}') || (n.Kind == tree.Array && c == ']')
}
