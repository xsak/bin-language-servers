package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/xsak/bin-language-servers/internal/tree"
)

// At returns every subschema that may apply at path, with $ref, allOf/anyOf/oneOf and
// if/then/else expanded. value is the document (tree.ToValue form) and is used to pick the
// matching anyOf/oneOf branches when that is possible.
func (sch *Schema) At(path tree.Path, value any) []*jsonschema.Schema {
	if sch == nil {
		return nil
	}
	cur := expand([]*jsonschema.Schema{sch.Root}, value)
	for _, seg := range path {
		value = step(value, seg)
		var next []*jsonschema.Schema
		for _, s := range cur {
			next = append(next, child(s, seg)...)
		}
		cur = expand(next, value)
		if len(cur) == 0 {
			return nil
		}
	}
	return cur
}

func step(v any, seg tree.Segment) any {
	switch c := v.(type) {
	case map[string]any:
		if !seg.IsIndex {
			return c[seg.Key]
		}
	case []any:
		if seg.IsIndex && seg.Index >= 0 && seg.Index < len(c) {
			return c[seg.Index]
		}
	}
	return nil
}

func child(s *jsonschema.Schema, seg tree.Segment) []*jsonschema.Schema {
	if !seg.IsIndex {
		if p, ok := s.Properties[seg.Key]; ok {
			return []*jsonschema.Schema{p}
		}
		var out []*jsonschema.Schema
		for re, p := range s.PatternProperties {
			if re.MatchString(seg.Key) {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
		if ap, ok := s.AdditionalProperties.(*jsonschema.Schema); ok {
			return []*jsonschema.Schema{ap}
		}
		return nil
	}
	i := seg.Index
	if i >= 0 && i < len(s.PrefixItems) {
		return []*jsonschema.Schema{s.PrefixItems[i]}
	}
	if s.Items2020 != nil {
		return []*jsonschema.Schema{s.Items2020}
	}
	switch it := s.Items.(type) {
	case *jsonschema.Schema:
		return []*jsonschema.Schema{it}
	case []*jsonschema.Schema:
		if i >= 0 && i < len(it) {
			return []*jsonschema.Schema{it[i]}
		}
		if ai, ok := s.AdditionalItems.(*jsonschema.Schema); ok {
			return []*jsonschema.Schema{ai}
		}
	}
	return nil
}

// expand follows references and combinators. Branches of anyOf/oneOf that are known not to
// match value are dropped (unless that would drop all of them).
func expand(in []*jsonschema.Schema, value any) []*jsonschema.Schema {
	var out []*jsonschema.Schema
	seen := map[*jsonschema.Schema]bool{}
	var visit func(*jsonschema.Schema)
	visit = func(s *jsonschema.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
		visit(s.Ref)
		if s.DynamicRef != nil {
			visit(s.DynamicRef.Ref)
		}
		visit(s.RecursiveRef)
		for _, a := range s.AllOf {
			visit(a)
		}
		for _, a := range pickBranches(s.AnyOf, value) {
			visit(a)
		}
		for _, a := range pickBranches(s.OneOf, value) {
			visit(a)
		}
		if s.If != nil {
			if value != nil && s.If.Validate(value) == nil {
				visit(s.Then)
			} else if value != nil {
				visit(s.Else)
			} else {
				visit(s.Then)
				visit(s.Else)
			}
		}
	}
	for _, s := range in {
		visit(s)
	}
	return out
}

func pickBranches(branches []*jsonschema.Schema, value any) []*jsonschema.Schema {
	if value == nil || len(branches) < 2 {
		return branches
	}
	var ok []*jsonschema.Schema
	for _, b := range branches {
		if b.Validate(value) == nil {
			ok = append(ok, b)
		}
	}
	if len(ok) > 0 {
		return ok
	}
	// Nothing validates yet (the document is being edited): keep branches whose type fits.
	var typed []*jsonschema.Schema
	for _, b := range branches {
		if typeFits(b, value) {
			typed = append(typed, b)
		}
	}
	if len(typed) > 0 {
		return typed
	}
	return branches
}

func typeFits(s *jsonschema.Schema, v any) bool {
	for s != nil && s.Types == nil && s.Ref != nil {
		s = s.Ref
	}
	if s == nil || s.Types == nil {
		return true
	}
	for _, t := range s.Types.ToStrings() {
		switch t {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "number", "integer":
			if _, ok := v.(json.Number); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}

// PropertyInfo describes a property that may appear in an object.
type PropertyInfo struct {
	Name        string
	Description string // markdown
	Types       []string
	Required    bool
	Deprecated  bool
	Enum        []any
	Default     *any
}

// Properties lists the properties allowed in the object at path.
func (sch *Schema) Properties(path tree.Path, value any) []PropertyInfo {
	schemas := sch.At(path, value)
	required := map[string]bool{}
	for _, s := range schemas {
		for _, r := range s.Required {
			required[r] = true
		}
	}
	byName := map[string]*PropertyInfo{}
	var order []string
	for _, s := range schemas {
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ps := s.Properties[name]
			info, ok := byName[name]
			if !ok {
				info = &PropertyInfo{Name: name, Required: required[name]}
				byName[name] = info
				order = append(order, name)
			}
			d := sch.Describe(expand([]*jsonschema.Schema{ps}, nil))
			if info.Description == "" {
				info.Description = d.Description
			}
			info.Types = union(info.Types, d.Types)
			info.Deprecated = info.Deprecated || d.Deprecated
			if len(info.Enum) == 0 {
				info.Enum = d.Enum
			}
			if info.Default == nil {
				info.Default = d.Default
			}
		}
	}
	out := make([]PropertyInfo, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

// Description is what the schemas at a location say about a value.
type Description struct {
	Title       string
	Description string // markdown
	Types       []string
	Enum        []any
	EnumDocs    []string // markdown, parallel to Enum (may be shorter)
	Default     *any
	Examples    []any
	Deprecated  bool
}

// Describe merges the annotations of schemas (as returned by At).
func (sch *Schema) Describe(schemas []*jsonschema.Schema) Description {
	var d Description
	seenEnum := map[string]bool{}
	for _, s := range schemas {
		raw := sch.RawAt(s.Location)
		if d.Title == "" {
			d.Title = s.Title
		}
		if d.Description == "" {
			if md, ok := raw["markdownDescription"].(string); ok && md != "" {
				d.Description = md
			} else {
				d.Description = s.Description
			}
		}
		if s.Types != nil {
			d.Types = union(d.Types, s.Types.ToStrings())
		}
		if s.Enum != nil {
			docs := stringList(raw["markdownEnumDescriptions"])
			if docs == nil {
				docs = stringList(raw["enumDescriptions"])
			}
			for i, v := range s.Enum.Values {
				k := fmt.Sprint(v)
				if seenEnum[k] {
					continue
				}
				seenEnum[k] = true
				d.Enum = append(d.Enum, v)
				doc := ""
				if i < len(docs) {
					doc = docs[i]
				}
				d.EnumDocs = append(d.EnumDocs, doc)
			}
		}
		if s.Const != nil && !seenEnum[fmt.Sprint(*s.Const)] {
			seenEnum[fmt.Sprint(*s.Const)] = true
			d.Enum = append(d.Enum, *s.Const)
			d.EnumDocs = append(d.EnumDocs, "")
		}
		if d.Default == nil && s.Default != nil {
			d.Default = s.Default
		}
		d.Examples = append(d.Examples, s.Examples...)
		d.Deprecated = d.Deprecated || s.Deprecated
	}
	return d
}

// Markdown renders a hover text.
func (d Description) Markdown() string {
	var b strings.Builder
	if d.Title != "" {
		fmt.Fprintf(&b, "**%s**\n\n", d.Title)
	}
	if d.Deprecated {
		b.WriteString("*Deprecated.*\n\n")
	}
	if d.Description != "" {
		b.WriteString(d.Description)
		b.WriteString("\n\n")
	}
	if len(d.Types) > 0 {
		fmt.Fprintf(&b, "Type: `%s`", strings.Join(d.Types, " | "))
		b.WriteString("\n\n")
	}
	if len(d.Enum) > 0 && len(d.Enum) <= 30 {
		b.WriteString("Allowed values:")
		for i, v := range d.Enum {
			fmt.Fprintf(&b, "\n- `%s`", jsonLiteral(v))
			if i < len(d.EnumDocs) && d.EnumDocs[i] != "" {
				b.WriteString(": " + d.EnumDocs[i])
			}
		}
		b.WriteString("\n\n")
	}
	if d.Default != nil {
		fmt.Fprintf(&b, "Default: `%s`\n\n", jsonLiteral(*d.Default))
	}
	return strings.TrimSpace(b.String())
}

// Values lists value suggestions at a location: enum/const values, defaults, examples and
// booleans/null for those types.
func (d Description) Values() []any {
	var out []any
	seen := map[string]bool{}
	add := func(v any) {
		k := jsonLiteral(v)
		if !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	for _, v := range d.Enum {
		add(v)
	}
	if d.Default != nil {
		add(*d.Default)
	}
	for _, v := range d.Examples {
		if isScalar(v) {
			add(v)
		}
	}
	if len(d.Enum) == 0 {
		for _, t := range d.Types {
			switch t {
			case "boolean":
				add(true)
				add(false)
			case "null":
				if len(d.Types) == 1 { // "string | null" fields: null is rarely what you want
					add(nil)
				}
			}
		}
	}
	return out
}

func isScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func jsonLiteral(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// JSONLiteral renders a value as JSON text.
func JSONLiteral(v any) string { return jsonLiteral(v) }

func union(a, b []string) []string {
	for _, x := range b {
		found := false
		for _, y := range a {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			a = append(a, x)
		}
	}
	return a
}

func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i], _ = x.(string)
	}
	return out
}
