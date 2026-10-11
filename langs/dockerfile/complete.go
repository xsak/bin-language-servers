package dockerfile

import (
	"context"
	"strconv"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

func (s *Server) Complete(_ context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	f := Parse(doc.Text)
	off := doc.Offset(pos)
	lineStart := doc.Offset(protocol.Position{Line: pos.Line})
	before := doc.Text[lineStart:off]

	// Word being typed (letters, digits, - _ . / : and for flags =).
	wstart := off
	for wstart > lineStart && !strings.ContainsRune(" \t$,{", rune(doc.Text[wstart-1])) {
		wstart--
	}
	word := doc.Text[wstart:off]
	r := doc.Range(wstart, off)
	snippets := s.Env.SnippetSupport

	item := func(label string, kind protocol.CompletionItemKind, detail, text string, snippet bool) protocol.CompletionItem {
		it := protocol.CompletionItem{Label: label, Kind: kind, Detail: detail,
			TextEdit: &protocol.TextEdit{Range: r, NewText: text}, FilterText: label}
		if snippet {
			it.InsertTextFormat = protocol.InsertTextSnippet
		}
		return it
	}

	var ins *Instruction
	for _, in := range f.Instructions {
		// Inside the instruction, or after it on its last line.
		if in.KeywordEnd < off && (off <= in.End || !strings.Contains(doc.Text[in.End:off], "\n")) {
			ins = in
		}
	}

	// Variables after $ or ${.
	if i := strings.LastIndexAny(before, "$"); i >= 0 && isVarTail(before[i+1:]) {
		vstart := lineStart + i + 1
		if strings.HasPrefix(before[i+1:], "{") {
			vstart++
		}
		vr := doc.Range(vstart, off)
		seen := map[string]bool{}
		var items []protocol.CompletionItem
		for _, v := range f.Vars() {
			if v.Start < off && !seen[v.Name] {
				seen[v.Name] = true
				items = append(items, protocol.CompletionItem{Label: v.Name, Kind: protocol.CompletionKindVariable,
					Detail: v.Keyword, TextEdit: &protocol.TextEdit{Range: vr, NewText: v.Name}})
			}
		}
		for _, a := range predefinedArgs {
			if !seen[a] {
				items = append(items, protocol.CompletionItem{Label: a, Kind: protocol.CompletionKindConstant,
					Detail: "predefined ARG", TextEdit: &protocol.TextEdit{Range: vr, NewText: a}})
			}
		}
		return &protocol.CompletionList{Items: items}, nil
	}

	// Instruction keyword at the start of a line (also after ONBUILD).
	if ins == nil || strings.TrimSpace(before[:len(before)-len(word)]) == "ONBUILD" {
		if strings.TrimSpace(before) != word && strings.TrimSpace(before[:len(before)-len(word)]) != "ONBUILD" {
			return nil, nil
		}
		var items []protocol.CompletionItem
		for i, kw := range instructionOrder {
			d := instructions[kw]
			text, snip := kw+" ", false
			if snippets && d.Snippet != "" {
				text, snip = d.Snippet, true
			}
			it := item(kw, protocol.CompletionKindKeyword, d.Syntax, text, snip)
			it.SortText = string(rune('a' + i))
			it.Documentation = &protocol.MarkupContent{Kind: protocol.MarkupMarkdown, Value: d.Summary}
			items = append(items, it)
		}
		return &protocol.CompletionList{Items: items}, nil
	}

	stages := f.Stages()

	// --from=<stage> and --mount=<options>: look at the whole flag token, which may contain commas.
	tstart := off
	for tstart > lineStart && doc.Text[tstart-1] != ' ' && doc.Text[tstart-1] != '\t' {
		tstart--
	}
	if k, v, ok := strings.Cut(doc.Text[tstart:off], "="); ok && strings.HasPrefix(k, "--") {
		vr := doc.Range(tstart+len(k)+1, off)
		var items []protocol.CompletionItem
		switch k {
		case "--from":
			for i, st := range stages {
				if st.From.Start >= ins.Start {
					break
				}
				label := st.Name
				if label == "" {
					label = strconv.Itoa(i)
				}
				items = append(items, protocol.CompletionItem{Label: label, Kind: protocol.CompletionKindReference,
					Detail: "stage: " + st.Image.Text, TextEdit: &protocol.TextEdit{Range: vr, NewText: label}})
			}
		case "--mount":
			items = mountItems(doc, v, off)
		case "--network":
			for _, n := range []string{"default", "none", "host"} {
				items = append(items, protocol.CompletionItem{Label: n, Kind: protocol.CompletionKindValue,
					TextEdit: &protocol.TextEdit{Range: vr, NewText: n}})
			}
		}
		return &protocol.CompletionList{Items: items}, nil
	}

	// Flags right after the keyword.
	if strings.HasPrefix(word, "-") || (word == "" && off <= ins.ArgsStart) {
		var items []protocol.CompletionItem
		for _, fd := range instructions[ins.Keyword].Flags {
			if ins.Flag(fd.Name) != nil {
				continue
			}
			text, snip := "--"+fd.Name, false
			if fd.Value != "" {
				text += "="
				if snippets {
					text, snip = text+"${1:"+fd.Value+"}", true
				}
			}
			items = append(items, item("--"+fd.Name, protocol.CompletionKindProperty, fd.Summary, text, snip))
		}
		if len(items) > 0 || strings.HasPrefix(word, "-") {
			return &protocol.CompletionList{Items: items}, nil
		}
	}

	var items []protocol.CompletionItem
	switch ins.Keyword {
	case "FROM":
		w := ins.Words()
		switch {
		case len(w) == 0 || (len(w) == 1 && w[0].End >= off):
			for _, st := range stages {
				if st.Name != "" && st.From.Start < ins.Start {
					items = append(items, item(st.Name, protocol.CompletionKindReference, "stage", st.Name, false))
				}
			}
			items = append(items, item("scratch", protocol.CompletionKindValue, "empty image", "scratch", false))
		case len(w) == 1 || (len(w) == 2 && w[1].End >= off):
			items = append(items, item("AS", protocol.CompletionKindKeyword, "name this stage", "AS ", false))
		}
	case "HEALTHCHECK":
		items = append(items, item("CMD", protocol.CompletionKindKeyword, "check command", "CMD ", false),
			item("NONE", protocol.CompletionKindKeyword, "disable inherited check", "NONE", false))
	case "STOPSIGNAL":
		for _, sig := range []string{"SIGTERM", "SIGINT", "SIGQUIT", "SIGKILL", "SIGHUP", "SIGUSR1", "SIGUSR2"} {
			items = append(items, item(sig, protocol.CompletionKindValue, "", sig, false))
		}
	}
	return &protocol.CompletionList{Items: items}, nil
}

func isVarTail(s string) bool {
	s = strings.TrimPrefix(s, "{")
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// mountItems completes the comma-separated value of --mount.
func mountItems(doc *document.Doc, value string, off int) []protocol.CompletionItem {
	parts := strings.Split(value, ",")
	cur := parts[len(parts)-1]
	r := doc.Range(off-len(cur), off)
	typ := "bind"
	for _, p := range parts[:len(parts)-1] {
		if t, ok := strings.CutPrefix(p, "type="); ok {
			typ = t
		}
	}
	var items []protocol.CompletionItem
	if !strings.Contains(value, "type=") || strings.HasPrefix(cur, "type=") {
		for _, t := range []string{"cache", "bind", "tmpfs", "secret", "ssh"} {
			items = append(items, protocol.CompletionItem{Label: "type=" + t, Kind: protocol.CompletionKindEnum,
				TextEdit: &protocol.TextEdit{Range: r, NewText: "type=" + t}})
		}
		return items
	}
	used := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		k, _, _ := strings.Cut(p, "=")
		used[k] = true
	}
	for _, o := range mountOptions[typ] {
		k, _, _ := strings.Cut(o, "=")
		if used[k] {
			continue
		}
		items = append(items, protocol.CompletionItem{Label: o, Kind: protocol.CompletionKindProperty,
			TextEdit: &protocol.TextEdit{Range: r, NewText: o}})
	}
	return items
}
