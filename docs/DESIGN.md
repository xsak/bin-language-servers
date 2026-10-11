# bls — Node-free language servers for Zed

One static Go binary (`bls`) that runs as a language server for several languages. No Node.js,
npm or npx is involved at build time or at run time.

## Goals

- Replace the Node-based servers Zed currently downloads: `json-language-server`,
  `yaml-language-server`, `bash-language-server`, `dockerfile-language-server`,
  `docker-compose-langserver`, `vscode-html/css-language-server`, `ansible-language-server`.
- Pure Go, `CGO_ENABLED=0`, a single self-contained binary.
- Go is never installed on the host: every build, test and tooling step runs in a container
  (podman now, docker later). See [BUILD.md](BUILD.md).
- Modular: each language is an isolated package on top of a shared core, and can be left out of
  the build with a build tag.
- Works offline: key schemas and data are embedded, everything else is cached after the first fetch.

## Non-goals

These already have good servers that don't need Node. We only document the Zed config for them:

| Language       | Server                                   | Implementation |
|----------------|------------------------------------------|----------------|
| Terraform      | `terraform-ls`                           | Go             |
| Helm           | `helm-ls`                                | Go             |
| SQL            | `sqls`                                   | Go             |
| Python         | `ruff` (lint/format), `ty` or `pyrefly` (types) | Rust     |
| TOML           | `taplo`                                  | Rust           |

A Python type checker or a TypeScript server is out of scope.

## Invocation

```text
bls <language> [--stdio] [--log-file PATH] [--log-level LEVEL]
bls version
bls list                 # languages compiled into this binary
```

When the binary is called through a symlink (`yaml-ls -> bls`), it uses `argv[0]` to pick the language
(`<lang>-ls` or `bls-<lang>`).

Languages: `json`, `yaml`, `bash`, `dockerfile`, `compose`, `ansible`, `html`, `css`.

## Layout

```text
cmd/bls/                 main: dispatch by subcommand / argv[0]
internal/jsonrpc/        JSON-RPC 2.0 over stdio (Content-Length framing), cancellation
internal/protocol/       LSP 3.17 types (hand-written subset that we actually use)
internal/server/         lifecycle, capability negotiation, request routing, workers
internal/document/       open-document store, incremental sync, UTF-8 <-> UTF-16 positions
internal/tree/           format-neutral value tree (object/array/scalars) with byte ranges
internal/schema/         JSON Schema engine: store + disk cache, catalog, globs, settings, validation, navigation
internal/schemalang/     shared base of schema-driven modules (settings, schema lookup, outline, items)
internal/jsonast/        error-tolerant JSON(C) parser with byte ranges, comment-preserving formatter
internal/yamlast/        YAML (multi-document) to tree, anchors/aliases/merge keys resolved
internal/extproc/        running optional external tools (shellcheck, ansible-lint) with timeouts
internal/lang/           Language interface, optional feature interfaces, module registry
internal/lsptest/        in-process LSP client used by tests
langs/<name>/            one package per language; registers itself with lang.Register in init()
langs/all/               one file per language that imports it, guarded by a build tag
data/                    embedded data (schemas, HTML/CSS custom data), refreshed by `go generate`
```

### The language interface

The core handles transport, lifecycle and document sync. A language implements only the features it
supports. The server finds them with type assertions and only advertises the capabilities that are
implemented. The authoritative definitions are in `internal/lang/lang.go`:

| Interface            | Method                                   | LSP feature                          |
|----------------------|------------------------------------------|--------------------------------------|
| `Language` (required)| `Name()`, `Init(ctx, Env)`               | `initialize`                         |
| `Diagnoser`          | `Diagnose(ctx, doc)`                     | `textDocument/publishDiagnostics`    |
| `Completer`          | `Complete(ctx, doc, pos)`                | `textDocument/completion`            |
| `TriggerCharacterer` | `TriggerCharacters()`                    | completion trigger characters        |
| `Hoverer`            | `Hover(ctx, doc, pos)`                   | `textDocument/hover`                 |
| `Symboler`           | `Symbols(ctx, doc)`                      | `textDocument/documentSymbol`        |
| `Formatter`          | `Format(ctx, doc, opts)`                 | `textDocument/formatting`            |
| `Definer`            | `Definition(ctx, doc, pos)`              | `textDocument/definition`            |
| `Configurable`       | `Configure(settings)`                    | `workspace/didChangeConfiguration`   |
| `SaveListener`       | `DidSave(ctx, doc)`                      | `textDocument/didSave` (slow linters)|

`Env` gives the module its workspace folders, `initializationOptions`, a cache directory
(`~/.cache/bls/<lang>`), a logger, client capabilities (snippets, Markdown), and a `Client` used to log to
the editor and to ask the server to re-run diagnostics.

A module registers itself:

```go
func init() {
    lang.Register(lang.Info{Name: "yaml", Description: "YAML with JSON Schema", New: func() lang.Language { return &Server{} }})
}
```

Documents are immutable snapshots (`document.Doc`); an edit creates a new snapshot, so feature code can read
a document without locks. Diagnostics are debounced (200 ms after the last change) and published per
document; results from a run that was superseded by a newer edit are dropped. Requests are cancelled on
`$/cancelRequest`. The server prefers the UTF-8 position encoding when the client offers it and falls
back to UTF-16.

### Build tags

Every file in `langs/all/` starts with `//go:build !no_<lang>` and imports one language package.
Example of a slim build:

```bash
make build TAGS=no_html,no_css,no_ansible
```

## Languages

### JSON / JSONC (`json`) and YAML (`yaml`)

Both run on the shared `internal/schema` engine:

- Schema association, in priority order: settings (`json.schemas` / `yaml.schemas`, same format as the
  Red Hat / VS Code servers so existing Zed configs keep working), the `$schema` key or a
  `# yaml-language-server: $schema=` modeline, then SchemaStore catalog `fileMatch`.
- Kubernetes: detects `apiVersion`/`kind` and uses the matching schema from a pinned
  kubernetes-json-schema version (CRDs can be mapped through settings).
- Features: validation diagnostics, completion (keys, enum values, defaults, snippets for required
  properties), hover (title/description), document symbols, formatting.
- YAML-only: multi-document support, anchors/aliases, duplicate-key and syntax diagnostics.
- Dependencies: `github.com/santhosh-tekuri/jsonschema/v6` (validation, drafts 4 to 2020-12);
  `github.com/goccy/go-yaml` for the YAML AST (it tolerates errors and keeps positions).

Implementation notes (as built):

- Both formats are converted to `internal/tree` (values with byte ranges). Validation converts the tree to
  plain Go values, runs `santhosh-tekuri/jsonschema/v6` and maps each error's instance location back to a
  range. For `anyOf`/`oneOf` only the branch that got furthest into the document is reported; if all branches
  failed on the type, one "Expected one of …" message is produced.
- Completion and hover walk the compiled schema (`$ref`, `allOf`, `anyOf`/`oneOf` filtered by the current
  value, `if`/`then`/`else`); `markdownDescription` and `enumDescriptions` are read from the raw schema
  document because the validator doesn't keep them.
- YAML completion doesn't use the parse tree: it derives the path from indentation and `- ` markers, so it works
  on half-typed documents. Hover and diagnostics use the tree.
- Schemas load asynchronously (`schema.Store`); a finished load triggers `RefreshDiagnostics`. Failed downloads
  are retried after a minute; 404s are remembered.
- The server pulls settings with `workspace/configuration` after `initialized` (Zed/Gram answer from
  `lsp.<server>.settings`), and also accepts pushed `workspace/didChangeConfiguration`.

### Bash (`bash`)

- Parser and formatter: `mvdan.cc/sh/v3` (`syntax` package; formatting reads `.editorconfig` like
  `shfmt` does).
- Diagnostics: parse errors, plus `shellcheck --format=json1` if it is on `PATH` (it is a Haskell
  binary and doesn't need Node).
- Symbols (functions, variables), go-to-definition and references for functions and variables in the
  file and in `source`d files, hover with `help`/man summaries for builtins, completion for variables,
  functions and builtins.

### Dockerfile (`dockerfile`)

- Parser: `github.com/moby/buildkit/frontend/dockerfile/parser` (the parser Docker itself uses), and
  buildkit lint rules where they can be used as a library.
- Completion and hover for instructions and flags (`--mount=type=cache`, …), stage names in
  `COPY --from` / `FROM`, `ARG` references; symbols per stage.

### Compose (`compose`)

- Built on the YAML module with the compose-spec schema embedded.
- Extra checks: unknown `depends_on`, `networks`, `volumes`, `secrets` and `configs` references;
  go-to-definition for those references; `extends` and `include` files.

### Ansible (`ansible`)

- Built on the YAML module with the ansible-lint schemas (playbook, tasks, vars, meta, galaxy,
  requirements).
- Module and parameter completion and hover come from `ansible-doc --list --json` and
  `ansible-doc --json <module>` (Python), cached per installed collection version.
- Diagnostics: schema, plus `ansible-lint --format=json` on save if it is installed.
- Go-to-definition for roles, `include_tasks` / `import_tasks` and `vars_files`.
- Detection follows Zed's Ansible language (file globs from its `file_types` setting).

### HTML (`html`) and CSS (`css`)

- HTML: tolerant tokenizer `golang.org/x/net/html`. CSS: `github.com/tdewolff/parse/v2/css`.
- Data: Microsoft `vscode-custom-data` (MIT) for elements, attributes, CSS properties and values, and
  MDN references. It is embedded via `go:embed`, and `go generate` downloads it as plain JSON (no npm).
- Features: completion and hover, tag auto-close support (`linkedEditingRange`), CSS diagnostics for
  unknown properties and syntax errors, symbols, formatting. HTML delegates `<style>` and `style=""`
  to the CSS module.

## Zed integration

Point Zed's existing server names at `bls` so the built-in and extension language configurations keep
working. `~/.config/zed/settings.json`:

```jsonc
{
  "lsp": {
    "json-language-server":       { "binary": { "path": "~/.local/bin/bls", "arguments": ["json"] } },
    "yaml-language-server":       { "binary": { "path": "~/.local/bin/bls", "arguments": ["yaml"] } },
    "bash-language-server":       { "binary": { "path": "~/.local/bin/bls", "arguments": ["bash"] } },
    "dockerfile-language-server": { "binary": { "path": "~/.local/bin/bls", "arguments": ["dockerfile"] } },
    "docker-compose":             { "binary": { "path": "~/.local/bin/bls", "arguments": ["compose"] } },
    "ansible-language-server":    { "binary": { "path": "~/.local/bin/bls", "arguments": ["ansible"] } },
    "vscode-html-language-server": { "binary": { "path": "~/.local/bin/bls", "arguments": ["html"] } },
    "vscode-css-language-server":  { "binary": { "path": "~/.local/bin/bls", "arguments": ["css"] } }
  },
  // Zed formats JSON/YAML/CSS/HTML/Markdown with Prettier (Node) by default; turn that off.
  "prettier": { "allowed": false },
  "languages": {
    "Python": { "language_servers": ["ruff", "ty", "!basedpyright", "!pyright"] }
  }
}
```

The exact server names have to be checked against the installed Zed and extension versions. A
`bls doctor` subcommand will print the names it finds and warn if `~/.local/share/zed/node` is still
used. Fallback: a small Zed extension (Rust to WASM, no Node) that registers `bls-*` servers directly.

## Testing

- Unit tests per package. Golden tests that compare diagnostics, completion and hover output in
  `testdata/` against `*.golden` files.
- An in-process LSP client harness (`internal/lsptest`) that runs full sessions without spawning
  processes.
- CI: the same container targets as local builds (`make check`, `make release`), with release builds
  for linux/amd64, linux/arm64 and darwin/arm64.

## Milestones

1. **Core** ✅ (2026-10-11): jsonrpc, protocol, server, document store with UTF-8/16/32 mapping,
   `lsptest` harness, `bls version|list`, container build.
2. **JSON + YAML + schema engine** ✅ (2026-10-11). User documentation: [LANGUAGES.md](LANGUAGES.md).
3. **Bash**.
4. **Dockerfile + Compose**.
5. **Ansible**.
6. **HTML + CSS**.
7. `bls doctor`, release automation, a VS Code client extension (`editors/vscode/`, `.vsix` built in a
   container), a Zed/Gram extension if the binary overrides turn out to be fragile.
