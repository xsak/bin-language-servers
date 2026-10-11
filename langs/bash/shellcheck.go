package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/extproc"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// scComment is one finding in shellcheck's json1 output. Lines and columns are 1-based and
// count characters (a tab is one column).
type scComment struct {
	Line, EndLine     int
	Column, EndColumn int
	Level             string
	Code              int
	Message           string
	Fix               *struct {
		Replacements []struct {
			Line, EndLine     int
			Column, EndColumn int
			Replacement       string
		} `json:"replacements"`
	} `json:"fix"`
}

// shellcheck runs shellcheck on the document text. It returns nil if shellcheck is disabled,
// missing or not applicable to the dialect.
func (s *Server) shellcheck(ctx context.Context, doc *document.Doc, d dialect) []scComment {
	set := s.settings()
	path := extproc.Find(set.ShellcheckPath)
	if path == "" || d.shellcheck == "" {
		return nil
	}
	args := []string{"--format=json1", "--external-sources", "--source-path=SCRIPTDIR"}
	if !strings.HasPrefix(doc.Text, "#!") {
		args = append(args, "--shell="+d.shellcheck)
	}
	args = append(args, set.ShellcheckArgs...)
	args = append(args, "-")
	dir := ""
	if p := doc.Path(); p != "" {
		dir = filepath.Dir(p)
	}
	res, err := extproc.Run(ctx, dir, doc.Text, path, args...)
	if err != nil {
		s.Env.Logger.Warn("shellcheck failed", "err", err)
		return nil
	}
	var out struct {
		Comments []scComment `json:"comments"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		if msg := strings.TrimSpace(string(res.Stderr)); msg != "" {
			s.Env.Logger.Warn("shellcheck", "stderr", msg)
		}
		return nil
	}
	return out.Comments
}

// charOffset converts a 1-based line and character column to a byte offset.
func charOffset(doc *document.Doc, line, col int) int {
	if line < 1 {
		return 0
	}
	start := doc.Offset(protocol.Position{Line: uint32(line - 1)})
	text := doc.Line(line - 1)
	i := 0
	for c := 1; c < col && i < len(text); c++ {
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return start + i
}

func (c scComment) diagnostic(doc *document.Doc) protocol.Diagnostic {
	sev := protocol.SeverityHint
	switch c.Level {
	case "error":
		sev = protocol.SeverityError
	case "warning":
		sev = protocol.SeverityWarning
	case "info":
		sev = protocol.SeverityInformation
	}
	code := fmt.Sprintf("SC%d", c.Code)
	return protocol.Diagnostic{
		Range:           doc.Range(charOffset(doc, c.Line, c.Column), charOffset(doc, c.EndLine, c.EndColumn)),
		Severity:        sev,
		Code:            code,
		CodeDescription: &protocol.CodeDescription{Href: "https://www.shellcheck.net/wiki/" + code},
		Source:          "shellcheck",
		Message:         c.Message,
	}
}

// codeActions builds the quick fixes for the findings that overlap rng: shellcheck's own fix
// when it has one, and a "disable" directive for the line.
func codeActions(doc *document.Doc, comments []scComment, rng protocol.Range) []protocol.CodeAction {
	var out []protocol.CodeAction
	disabled := map[string]bool{}
	for _, c := range comments {
		d := c.diagnostic(doc)
		if !overlaps(d.Range, rng) {
			continue
		}
		code := d.Code.(string)
		if c.Fix != nil && len(c.Fix.Replacements) > 0 {
			var edits []protocol.TextEdit
			for _, r := range c.Fix.Replacements {
				edits = append(edits, protocol.TextEdit{
					Range:   doc.Range(charOffset(doc, r.Line, r.Column), charOffset(doc, r.EndLine, r.EndColumn)),
					NewText: r.Replacement,
				})
			}
			out = append(out, protocol.CodeAction{
				Title: fmt.Sprintf("Fix %s: %s", code, c.Message), Kind: protocol.CodeActionQuickFix,
				Diagnostics: []protocol.Diagnostic{d}, IsPreferred: true,
				Edit: &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{doc.URI: edits}},
			})
		}
		key := fmt.Sprintf("%d:%s", c.Line, code)
		if disabled[key] {
			continue
		}
		disabled[key] = true
		line := doc.Line(c.Line - 1)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		at := doc.Range(charOffset(doc, c.Line, 1), charOffset(doc, c.Line, 1))
		out = append(out, protocol.CodeAction{
			Title: fmt.Sprintf("Disable %s for this line", code), Kind: protocol.CodeActionQuickFix,
			Diagnostics: []protocol.Diagnostic{d},
			Edit: &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{
				doc.URI: {{Range: at, NewText: indent + "# shellcheck disable=" + code + "\n"}},
			}},
		})
	}
	return out
}

func overlaps(a, b protocol.Range) bool {
	return !before(a.End, b.Start) && !before(b.End, a.Start)
}

func before(p, q protocol.Position) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Character < q.Character)
}
