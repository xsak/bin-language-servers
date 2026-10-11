// Package tree is the format-neutral document model shared by the JSON and YAML modules: a
// JSON-like value tree where every node remembers its byte range in the source text.
//
// The schema engine works only on this model, so it validates JSON and YAML the same way.
package tree

import "strconv"

// Kind is the JSON type of a node.
type Kind int

const (
	Object Kind = iota
	Array
	String
	Number
	Bool
	Null
)

func (k Kind) String() string {
	return [...]string{"object", "array", "string", "number", "boolean", "null"}[k]
}

// Node is a value. Start/End are byte offsets; End is exclusive.
type Node struct {
	Kind       Kind
	Start, End int
	// Value holds scalars: string, json.Number (or float64 for YAML .inf/.nan), bool or nil.
	Value any
	Props []*Prop // Object
	Items []*Node // Array
}

// Prop is an object member. Value is nil when the source is incomplete (`"a": }`).
type Prop struct {
	Key              string
	KeyStart, KeyEnd int
	Colon            int // offset of ':' or -1
	Value            *Node
}

// Segment is one step of a path: a property name or an array index.
type Segment struct {
	Key   string
	Index int // valid when IsIndex
	// IsIndex marks an array step. Index < 0 means "any item" (used by YAML completion when the
	// item position is unknown).
	IsIndex bool
}

// Path is a list of segments from the root.
type Path []Segment

// KeySeg and IndexSeg build segments.
func KeySeg(k string) Segment { return Segment{Key: k} }
func IndexSeg(i int) Segment  { return Segment{Index: i, IsIndex: true} }
func (p Path) Append(s Segment) Path {
	out := make(Path, len(p), len(p)+1)
	copy(out, p)
	return append(out, s)
}

// Pointer renders the path as a JSON pointer.
func (p Path) Pointer() string {
	s := ""
	for _, seg := range p {
		if seg.IsIndex {
			s += "/" + strconv.Itoa(seg.Index)
		} else {
			s += "/" + escape(seg.Key)
		}
	}
	return s
}

func escape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

// Prop returns the last property named key, or nil.
func (n *Node) Prop(key string) *Prop {
	if n == nil {
		return nil
	}
	for i := len(n.Props) - 1; i >= 0; i-- {
		if n.Props[i].Key == key {
			return n.Props[i]
		}
	}
	return nil
}

// StringProp returns the string value of a property, or "".
func (n *Node) StringProp(key string) string {
	if p := n.Prop(key); p != nil && p.Value != nil && p.Value.Kind == String {
		s, _ := p.Value.Value.(string)
		return s
	}
	return ""
}

// ToValue converts the tree into plain Go values (map[string]any, []any, string, json.Number,
// bool, nil) as expected by the JSON Schema validator. Later duplicate keys win.
func (n *Node) ToValue() any {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case Object:
		m := make(map[string]any, len(n.Props))
		for _, p := range n.Props {
			if p.Value != nil {
				m[p.Key] = p.Value.ToValue()
			}
		}
		return m
	case Array:
		a := make([]any, 0, len(n.Items))
		for _, it := range n.Items {
			a = append(a, it.ToValue())
		}
		return a
	default:
		return n.Value
	}
}

// Find follows instance-location segments (as strings, like the validator reports them) and
// returns the node and the property that holds it (nil for array items and the root).
func (n *Node) Find(loc []string) (*Node, *Prop) {
	cur := n
	var holder *Prop
	for _, seg := range loc {
		if cur == nil {
			return nil, nil
		}
		switch cur.Kind {
		case Object:
			p := cur.Prop(seg)
			if p == nil {
				return nil, nil
			}
			holder, cur = p, p.Value
		case Array:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(cur.Items) {
				return nil, nil
			}
			holder, cur = nil, cur.Items[i]
		default:
			return nil, nil
		}
	}
	return cur, holder
}

// Location describes what is at a byte offset.
type Location struct {
	Path  Path  // path of the node (or of the property whose key is under the cursor)
	Node  *Node // innermost node containing the offset, nil if only a key matched
	Prop  *Prop // set when the offset is on a property key
	OnKey bool
}

// At returns the innermost node or property key containing off (inclusive end, so the
// position right after a token still hits it).
func (n *Node) At(off int) (Location, bool) {
	if n == nil || off < n.Start || off > n.End {
		return Location{}, false
	}
	loc := Location{Node: n}
	cur := n
	for {
		next := false
		switch cur.Kind {
		case Object:
			for _, p := range cur.Props {
				if off >= p.KeyStart && off <= p.KeyEnd {
					return Location{Path: loc.Path.Append(KeySeg(p.Key)), Prop: p, OnKey: true}, true
				}
				if p.Value != nil && off >= p.Value.Start && off <= p.Value.End {
					loc.Path = loc.Path.Append(KeySeg(p.Key))
					loc.Node, loc.Prop, cur, next = p.Value, p, p.Value, true
					break
				}
			}
		case Array:
			for i, it := range cur.Items {
				if off >= it.Start && off <= it.End {
					loc.Path = loc.Path.Append(IndexSeg(i))
					loc.Node, loc.Prop, cur, next = it, nil, it, true
					break
				}
			}
		}
		if !next {
			return loc, true
		}
	}
}

// Walk calls fn for every property in depth-first order with its path.
func (n *Node) Walk(fn func(path Path, p *Prop)) {
	var walk func(*Node, Path)
	walk = func(cur *Node, path Path) {
		if cur == nil {
			return
		}
		switch cur.Kind {
		case Object:
			for _, p := range cur.Props {
				pp := path.Append(KeySeg(p.Key))
				fn(pp, p)
				walk(p.Value, pp)
			}
		case Array:
			for i, it := range cur.Items {
				walk(it, path.Append(IndexSeg(i)))
			}
		}
	}
	walk(n, nil)
}
