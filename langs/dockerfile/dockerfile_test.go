package dockerfile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xsak/bin-language-servers/internal/document"
	"github.com/xsak/bin-language-servers/internal/lsptest"
	"github.com/xsak/bin-language-servers/internal/protocol"
)

const sample = `# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27
FROM golang:${GO_VERSION} AS build
WORKDIR /src
RUN --mount=type=cache,target=/root/.cache \
    go build -o /out/app .
RUN <<EOF
echo one
echo two
EOF

FROM alpine
COPY --from=build /out/app /app
CMD ["/app"]
`

func TestParse(t *testing.T) {
	f := Parse(sample)
	if f.Directives["syntax"] != "docker/dockerfile:1" {
		t.Errorf("directives = %v", f.Directives)
	}
	var kws []string
	for _, ins := range f.Instructions {
		kws = append(kws, ins.Keyword)
	}
	if got := strings.Join(kws, " "); got != "ARG FROM WORKDIR RUN RUN FROM COPY CMD" {
		t.Fatalf("instructions = %s", got)
	}
	run := f.Instructions[3]
	if run.Flag("mount").Value != "type=cache,target=/root/.cache" || !strings.Contains(run.Args, "go build") {
		t.Errorf("run = %+v", run)
	}
	if hd := f.Instructions[4]; len(hd.Heredocs) != 1 || !strings.HasSuffix(sample[hd.Start:hd.End], "EOF") {
		t.Errorf("heredoc = %q", sample[hd.Start:hd.End])
	}
	if cmd := f.Instructions[7]; len(cmd.JSON) != 1 || cmd.JSON[0] != "/app" {
		t.Errorf("cmd json = %v", cmd.JSON)
	}
	st := f.Stages()
	if len(st) != 2 || st[0].Name != "build" || st[1].Image.Text != "alpine" {
		t.Fatalf("stages = %+v", st)
	}
	vars := f.Vars()
	refs := f.VarRefs(sample)
	if len(vars) != 1 || vars[0].Name != "GO_VERSION" || len(refs) != 1 || refs[0].Name != "GO_VERSION" {
		t.Fatalf("vars %+v refs %+v", vars, refs)
	}
}

func TestLint(t *testing.T) {
	src := "RUN echo hi\nFROM ubuntu AS base\nFROM base AS base\nfrob x\nWORKDIR app\nCMD echo a\nCMD [\"b\"]\nMAINTAINER me\nCOPY --from=biuld /a /b\nEXPOSE\n"
	doc := document.New("file:///Dockerfile", "dockerfile", 1, src, "")
	msgs := ""
	for _, d := range lint(doc, Parse(src)) {
		msgs += d.Message + "\n"
	}
	for _, want := range []string{
		"first instruction must be FROM", "Pin the base image version (ubuntu", "Duplicate stage name",
		"Unknown instruction FROB", "Relative WORKDIR", "Use JSON (exec) form for CMD",
		"Only the last CMD", "MAINTAINER is deprecated", `No stage named "biuld"`, "EXPOSE needs at least one argument",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
	if strings.Contains(msgs, "Pin the base image version (base") {
		t.Error("stage reference treated as an image")
	}
}

func TestFeatures(t *testing.T) {
	t.Setenv("PATH", "/nonexistent") // no hadolint
	c := lsptest.Start(t, &Server{})
	dir := t.TempDir()
	c.Initialize(document.PathToURI(dir))
	uri := c.Open(document.PathToURI(filepath.Join(dir, "Dockerfile")), "dockerfile", sample)

	labels := func(l *protocol.CompletionList) map[string]bool {
		out := map[string]bool{}
		if l != nil {
			for _, it := range l.Items {
				out[it.Label] = true
			}
		}
		return out
	}
	// Instruction keywords on an empty line.
	c.Replace(uri, sample+"RU")
	if got := labels(c.Complete(uri, 14, 2)); !got["RUN"] || !got["FROM"] {
		t.Errorf("keywords = %v", got)
	}
	// Stage names after --from=.
	c.Replace(uri, sample+"COPY --from=")
	if got := labels(c.Complete(uri, 14, 12)); !got["build"] || !got["1"] {
		t.Errorf("--from = %v", got)
	}
	// Flags of COPY.
	c.Replace(uri, sample+"COPY --")
	if got := labels(c.Complete(uri, 14, 7)); !got["--chown"] || !got["--link"] || got["--mount"] {
		t.Errorf("flags = %v", got)
	}
	// --mount options by type.
	c.Replace(uri, sample+"RUN --mount=type=cache,")
	if got := labels(c.Complete(uri, 14, 23)); !got["target="] || !got["sharing=locked"] {
		t.Errorf("mount = %v", got)
	}
	// Variables.
	c.Replace(uri, sample+"ENV X=${G")
	if got := labels(c.Complete(uri, 14, 9)); !got["GO_VERSION"] || !got["TARGETARCH"] {
		t.Errorf("vars = %v", got)
	}

	c.Replace(uri, sample)
	if h := c.Hover(uri, 2, 1); h == nil || !strings.Contains(h.Contents.Value, "new build stage") {
		t.Errorf("keyword hover = %+v", h)
	}
	if h := c.Hover(uri, 12, 14); h == nil || !strings.Contains(h.Contents.Value, "Build stage `build`") {
		t.Errorf("stage hover = %+v", h)
	}
	if h := c.Hover(uri, 2, 18); h == nil || !strings.Contains(h.Contents.Value, "ARG GO_VERSION=1.27") {
		t.Errorf("var hover = %+v", h)
	}

	var locs []protocol.Location
	c.Call("textDocument/definition", protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Line: 12, Character: 14}}}, &locs)
	if len(locs) != 1 || locs[0].Range.Start.Line != 2 || locs[0].Range.Start.Character != 29 {
		t.Errorf("definition = %+v", locs)
	}

	var syms []protocol.DocumentSymbol
	c.Call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 3 || syms[1].Name != "build" || len(syms[1].Children) != 3 || syms[2].Name != "alpine" {
		t.Errorf("symbols = %+v", syms)
	}

	c.Replace(uri, "from alpine:3 as base\nrun echo\n")
	var edits []protocol.TextEdit
	c.Call("textDocument/formatting", protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &edits)
	if len(edits) != 3 || edits[0].NewText != "FROM" || edits[1].NewText != "AS" || edits[2].NewText != "RUN" {
		t.Errorf("format = %+v", edits)
	}
}
