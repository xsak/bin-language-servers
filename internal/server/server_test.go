package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/jsonrpc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

// fake reports a warning for every "TODO" and hovers the word under the cursor.
type fake struct{ env lang.Env }

func (f *fake) Name() string { return "fake" }

func (f *fake) Init(_ context.Context, env lang.Env) error { f.env = env; return nil }

func (f *fake) Diagnose(_ context.Context, d *document.Doc) []protocol.Diagnostic {
	var out []protocol.Diagnostic
	for i := 0; ; {
		j := strings.Index(d.Text[i:], "TODO")
		if j < 0 {
			return out
		}
		i += j
		out = append(out, protocol.Diagnostic{Range: d.Range(i, i+4), Severity: protocol.SeverityWarning,
			Source: "fake", Message: "todo"})
		i += 4
	}
}

func (f *fake) Hover(_ context.Context, d *document.Doc, p protocol.Position) (*protocol.Hover, error) {
	off := d.Offset(p)
	start, end := off, off
	for start > 0 && d.Text[start-1] != ' ' && d.Text[start-1] != '\n' {
		start--
	}
	for end < len(d.Text) && d.Text[end] != ' ' && d.Text[end] != '\n' {
		end++
	}
	r := d.Range(start, end)
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupMarkdown,
		Value: "`" + d.Text[start:end] + "`"}, Range: &r}, nil
}

func TestLifecycleAndFeatures(t *testing.T) {
	f := &fake{}
	c := lsptest.Start(t, f)

	var res any
	err := c.CallErr("textDocument/hover", map[string]any{}, &res)
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeServerNotInitialized {
		t.Fatalf("before initialize: got %v", err)
	}

	init := c.Initialize("file:///work/proj")
	caps := init.Capabilities
	if !caps.HoverProvider || caps.CompletionProvider != nil || caps.DocumentFormattingProvider {
		t.Fatalf("capabilities not derived from interfaces: %+v", caps)
	}
	if caps.PositionEncoding != protocol.PositionEncodingUTF16 {
		t.Fatalf("encoding = %q", caps.PositionEncoding)
	}
	if f.env.RootPath != "/work/proj" || !f.env.SnippetSupport || !f.env.MarkdownSupported {
		t.Fatalf("env not populated: %+v", f.env)
	}

	uri := c.Open("file:///work/proj/a.txt", "txt", "x TODO y\nTODO")
	if got := c.Diagnostics(uri, 1); len(got) != 2 || got[1].Range.Start.Line != 1 {
		t.Fatalf("diagnostics v1 = %+v", got)
	}

	c.Notify("textDocument/didChange", protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{
			Range: &protocol.Range{Start: protocol.Position{Line: 1}, End: protocol.Position{Line: 1, Character: 4}},
			Text:  "done",
		}},
	})
	if got := c.Diagnostics(uri, 2); len(got) != 1 {
		t.Fatalf("diagnostics v2 = %+v", got)
	}

	h := c.Hover(uri, 1, 2)
	if h == nil || h.Contents.Value != "`done`" {
		t.Fatalf("hover = %+v", h)
	}

	err = c.CallErr("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 0, "character": 0},
	}, &res)
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("unsupported feature: got %v", err)
	}

	c.Call("shutdown", nil, nil)
	if err := c.CallErr("textDocument/hover", map[string]any{}, &res); !errors.As(err, &rpcErr) ||
		rpcErr.Code != jsonrpc.CodeInvalidRequest {
		t.Fatalf("after shutdown: got %v", err)
	}
}
