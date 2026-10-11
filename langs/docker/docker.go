// Package docker serves Dockerfiles and Compose files from one server. Editors such as
// Zed/Gram attach a single server ("docker-language-server") to both languages; `bls docker`
// routes each document to the dockerfile or compose module.
package docker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/protocol"
	"github.com/xsak/bin-language-servers/langs/compose"
	"github.com/xsak/bin-language-servers/langs/dockerfile"
)

func init() {
	lang.Register(lang.Info{
		Name:        "docker",
		Description: "Dockerfile and Docker Compose in one server (routes by file)",
		New:         func() lang.Language { return &Server{} },
	})
}

// Server routes requests to the Dockerfile or Compose module.
type Server struct {
	df  dockerfile.Server
	cmp compose.Server
}

func (s *Server) Name() string { return "docker" }

func (s *Server) Init(ctx context.Context, env lang.Env) error {
	if err := s.df.Init(ctx, env); err != nil {
		return err
	}
	return s.cmp.Init(ctx, env)
}

var dockerfileName = regexp.MustCompile(`(?i)^(docker|container)file([.-].*)?$|\.(docker|container)file$`)

// isDockerfile decides by language id first, then by file name.
func isDockerfile(doc *document.Doc) bool {
	switch doc.LanguageID {
	case "dockerfile":
		return true
	case "dockercompose", "docker-compose", "yaml":
		return false
	}
	return dockerfileName.MatchString(filepath.Base(doc.Path()))
}

func (s *Server) TriggerCharacters() []string {
	return append(s.df.TriggerCharacters(), s.cmp.TriggerCharacters()...)
}

func (s *Server) ConfigSections() []string { return []string{"docker", "yaml"} }

func (s *Server) Configure(raw json.RawMessage) error {
	err1 := s.df.Configure(raw)
	err2 := s.cmp.Configure(raw)
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *Server) Diagnose(ctx context.Context, doc *document.Doc) []protocol.Diagnostic {
	if isDockerfile(doc) {
		return s.df.Diagnose(ctx, doc)
	}
	return s.cmp.Diagnose(ctx, doc)
}

func (s *Server) Complete(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.CompletionList, error) {
	if isDockerfile(doc) {
		return s.df.Complete(ctx, doc, pos)
	}
	return s.cmp.Complete(ctx, doc, pos)
}

func (s *Server) Hover(ctx context.Context, doc *document.Doc, pos protocol.Position) (*protocol.Hover, error) {
	if isDockerfile(doc) {
		return s.df.Hover(ctx, doc, pos)
	}
	return s.cmp.Hover(ctx, doc, pos)
}

func (s *Server) Symbols(ctx context.Context, doc *document.Doc) ([]protocol.DocumentSymbol, error) {
	if isDockerfile(doc) {
		return s.df.Symbols(ctx, doc)
	}
	return s.cmp.Symbols(ctx, doc)
}

func (s *Server) Definition(ctx context.Context, doc *document.Doc, pos protocol.Position) ([]protocol.Location, error) {
	if isDockerfile(doc) {
		return s.df.Definition(ctx, doc, pos)
	}
	return s.cmp.Definition(ctx, doc, pos)
}

func (s *Server) References(ctx context.Context, doc *document.Doc, pos protocol.Position, includeDecl bool) ([]protocol.Location, error) {
	if isDockerfile(doc) {
		return s.df.References(ctx, doc, pos, includeDecl)
	}
	return s.cmp.References(ctx, doc, pos, includeDecl)
}

// Format only applies to Dockerfiles; Compose (YAML) formatting isn't implemented.
func (s *Server) Format(ctx context.Context, doc *document.Doc, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
	if isDockerfile(doc) {
		return s.df.Format(ctx, doc, opts)
	}
	return nil, nil
}
