package schema

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/xsak/bin-language-servers/internal/tree"
)

// Problem is a schema violation mapped to a byte range of the document.
type Problem struct {
	Start, End int
	Message    string
}

var printer = message.NewPrinter(language.English)

// Validate checks root against the schema and returns problems with source ranges.
func (sch *Schema) Validate(root *tree.Node) []Problem {
	if sch == nil || root == nil {
		return nil
	}
	err := sch.Root.Validate(root.ToValue())
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{Start: root.Start, End: lineEnd(root), Message: err.Error()}}
	}

	var out []Problem
	seen := map[string]bool{}
	for _, leaf := range leaves(ve) {
		for _, p := range problemsFor(root, leaf) {
			k := fmt.Sprintf("%d:%d:%s", p.Start, p.End, p.Message)
			if !seen[k] {
				seen[k] = true
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// leaves flattens the error tree. For anyOf/oneOf it keeps only the branch that got furthest
// into the document, which is almost always the branch the author meant.
func leaves(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	switch e.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		if len(e.Causes) == 0 {
			return []*jsonschema.ValidationError{e}
		}
		best, bestDepth, bestCount := []*jsonschema.ValidationError(nil), -1, 0
		for _, branch := range e.Causes {
			ls := leaves(branch)
			depth := 0
			for _, l := range ls {
				depth = max(depth, len(l.InstanceLocation))
			}
			if depth > bestDepth || (depth == bestDepth && len(ls) < bestCount) {
				best, bestDepth, bestCount = ls, depth, len(ls)
			}
		}
		// Every branch failed right at this value (typically a type mismatch): one message.
		if bestDepth <= len(e.InstanceLocation) && len(e.Causes) > 1 {
			if msg := typeUnion(e.Causes); msg != "" {
				return []*jsonschema.ValidationError{{InstanceLocation: e.InstanceLocation,
					ErrorKind: textKind(msg)}}
			}
		}
		return best
	}
	if len(e.Causes) == 0 {
		return []*jsonschema.ValidationError{e}
	}
	var out []*jsonschema.ValidationError
	for _, c := range e.Causes {
		out = append(out, leaves(c)...)
	}
	return out
}

// typeUnion turns "every branch wanted a different type" into "Incorrect type. Expected one of
// string, object." Returns "" if the branches failed for other reasons.
func typeUnion(branches []*jsonschema.ValidationError) string {
	var want []string
	seen := map[string]bool{}
	for _, b := range branches {
		ls := leaves(b)
		if len(ls) != 1 {
			return ""
		}
		t, ok := ls[0].ErrorKind.(*kind.Type)
		if !ok {
			return ""
		}
		for _, w := range t.Want {
			if !seen[w] {
				seen[w] = true
				want = append(want, w)
			}
		}
	}
	return "Incorrect type. Expected one of " + strings.Join(want, ", ") + "."
}

type textKind string

func (t textKind) KeywordPath() []string                   { return nil }
func (t textKind) LocalizedString(*message.Printer) string { return string(t) }

func problemsFor(root *tree.Node, e *jsonschema.ValidationError) []Problem {
	node, holder := root.Find(e.InstanceLocation)
	if node == nil {
		node = root
	}
	switch k := e.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		var out []Problem
		for _, name := range k.Properties {
			if p := node.Prop(name); p != nil {
				out = append(out, Problem{Start: p.KeyStart, End: p.KeyEnd,
					Message: fmt.Sprintf("Property %s is not allowed.", name)})
			}
		}
		return out
	case *kind.FalseSchema:
		// "additionalProperties": false written as a false subschema (e.g. Compose): the key is
		// the problem, not its value.
		if holder != nil {
			return []Problem{{Start: holder.KeyStart, End: holder.KeyEnd,
				Message: fmt.Sprintf("Property %s is not allowed.", holder.Key)}}
		}
	case *kind.Required:
		start, end := anchor(node, holder)
		return []Problem{{Start: start, End: end, Message: "Missing property " + joinQuoted(k.Missing) + "."}}
	case *kind.Type:
		start, end := valueRange(node, holder)
		return []Problem{{Start: start, End: end,
			Message: fmt.Sprintf("Incorrect type. Expected %s.", strings.Join(k.Want, " or "))}}
	case *kind.Enum:
		start, end := valueRange(node, holder)
		vals := make([]string, 0, len(k.Want))
		for _, v := range k.Want {
			vals = append(vals, fmt.Sprintf("%v", jsonString(v)))
		}
		return []Problem{{Start: start, End: end, Message: "Value is not accepted. Valid values: " +
			strings.Join(vals, ", ") + "."}}
	}
	start, end := valueRange(node, holder)
	msg := e.ErrorKind.LocalizedString(printer)
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return []Problem{{Start: start, End: end, Message: msg}}
}

// anchor is where to report problems about a whole object: its key if it has one, otherwise
// its first line.
func anchor(n *tree.Node, holder *tree.Prop) (int, int) {
	if holder != nil && holder.KeyEnd > holder.KeyStart {
		return holder.KeyStart, holder.KeyEnd
	}
	if n.Kind == tree.Object && len(n.Props) > 0 && n.Start == n.Props[0].KeyStart {
		// Block-style YAML mapping without a key: mark the first key.
		return n.Props[0].KeyStart, n.Props[0].KeyEnd
	}
	return n.Start, lineEnd(n)
}

// valueRange is where to report problems about a value: scalars are marked whole, containers
// by their key (marking a whole block would be noisy).
func valueRange(n *tree.Node, holder *tree.Prop) (int, int) {
	if n.Kind == tree.Object || n.Kind == tree.Array {
		return anchor(n, holder)
	}
	if n.End == n.Start && holder != nil {
		return holder.KeyStart, holder.KeyEnd
	}
	return n.Start, n.End
}

func lineEnd(n *tree.Node) int {
	return min(n.End, n.Start+1)
}

func joinQuoted(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

func jsonString(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	if v == nil {
		return "null"
	}
	return fmt.Sprint(v)
}
