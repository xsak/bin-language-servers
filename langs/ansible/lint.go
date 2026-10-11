package ansible

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/extproc"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// ansible-lint works on files on disk and takes seconds, so it runs when a file is opened or
// saved (not on every keystroke); its results are merged into the diagnostics until the next run.

// lintPath finds ansible-lint: the setting, PATH, or next to the ansible-core that was found.
func (s *Server) lintPath() string {
	set := s.settings()
	if !set.Validation || set.LintPath == "" {
		return ""
	}
	if p := lookPath(set.LintPath); p != "" {
		return p
	}
	if set.LintPath != "ansible-lint" {
		return ""
	}
	for _, dir := range coreModuleDirs(set.AnsiblePath) {
		// .../lib/python3.X/site-packages/ansible/modules -> .../bin/ansible-lint
		prefix := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(dir)))))
		if p := filepath.Join(prefix, "bin", "ansible-lint"); isFile(p) {
			return p
		}
	}
	return ""
}

// DidSave runs ansible-lint and republishes diagnostics.
func (s *Server) DidSave(ctx context.Context, doc *document.Doc) {
	s.runLint(ctx, doc)
}

// lintOnce starts the first ansible-lint run for a document (on open).
func (s *Server) lintOnce(doc *document.Doc) {
	s.mu.Lock()
	_, done := s.lint[doc.URI]
	if !done {
		s.lint[doc.URI] = nil
	}
	s.mu.Unlock()
	if !done {
		go s.runLint(context.Background(), doc)
	}
}

func (s *Server) runLint(ctx context.Context, doc *document.Doc) {
	exe, path := s.lintPath(), doc.Path()
	if exe == "" || path == "" {
		return
	}
	dir := s.Env.RootPath
	if dir == "" {
		dir = filepath.Dir(path)
	}
	args := append([]string{"--offline", "--format", "codeclimate", "--nocolor", "-q"}, s.settings().LintArgs...)
	args = append(args, path)
	res, err := extproc.Run(ctx, dir, "", exe, args...)
	if err != nil {
		s.Env.Logger.Warn("ansible-lint failed", "err", err)
		return
	}
	diags := parseCodeclimate(doc, res.Stdout)
	s.mu.Lock()
	s.lint[doc.URI] = diags
	s.mu.Unlock()
	if s.Env.Client != nil {
		s.Env.Client.RefreshDiagnostics()
	}
}

// parseCodeclimate converts ansible-lint's codeclimate JSON to diagnostics.
func parseCodeclimate(doc *document.Doc, data []byte) []protocol.Diagnostic {
	var items []struct {
		CheckName   string `json:"check_name"`
		Description string `json:"description"`
		Severity    string `json:"severity"`
		URL         string `json:"url"`
		Location    struct {
			Lines struct {
				Begin any `json:"begin"`
			} `json:"lines"`
			Positions struct {
				Begin struct {
					Line   int `json:"line"`
					Column int `json:"column"`
				} `json:"begin"`
			} `json:"positions"`
		} `json:"location"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil
	}
	out := make([]protocol.Diagnostic, 0, len(items))
	for _, it := range items {
		line := it.Location.Positions.Begin.Line
		if line == 0 {
			switch b := it.Location.Lines.Begin.(type) {
			case float64:
				line = int(b)
			case map[string]any:
				if l, ok := b["line"].(float64); ok {
					line = int(l)
				}
			}
		}
		if line < 1 {
			line = 1
		}
		text := doc.Line(line - 1)
		start := doc.Offset(protocol.Position{Line: uint32(line - 1)})
		lead := len(text) - len(strings.TrimLeft(text, " \t-"))
		if col := it.Location.Positions.Begin.Column; col > 0 {
			lead = min(col-1, len(text))
		}
		sev := protocol.SeverityWarning
		switch it.Severity {
		case "blocker", "critical":
			sev = protocol.SeverityError
		case "info":
			sev = protocol.SeverityInformation
		case "minor":
			sev = protocol.SeverityHint
		}
		code := it.CheckName
		d := protocol.Diagnostic{Range: doc.Range(start+lead, start+len(text)), Severity: sev,
			Source: "ansible-lint", Code: code, Message: it.Description}
		if it.URL != "" {
			d.CodeDescription = &protocol.CodeDescription{Href: it.URL}
		} else if code != "" {
			d.CodeDescription = &protocol.CodeDescription{Href: "https://docs.ansible.com/projects/lint/rules/" + strings.SplitN(code, "[", 2)[0] + "/"}
		}
		out = append(out, d)
	}
	return out
}
