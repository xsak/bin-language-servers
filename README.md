# bls — Node-free language servers

`bls` is a single static Go binary that provides language servers for the
[Zed](https://zed.dev) editor (and any other LSP client), so that **no Node.js / npm / npx** is
needed on the machine.

> Status: **all planned languages are available** (milestones 1–6): JSON, YAML, Bash, Dockerfile, Compose,
> Ansible, HTML and CSS. JSON/YAML: JSON Schema
> validation, completion, hover, outline, SchemaStore and Kubernetes. Bash: shellcheck with quick fixes, shfmt
> formatting, navigation. Dockerfile/Compose: checks, completion, navigation between stages, services, networks
> and volumes. Ansible: module/option completion, hover and checks from the installed modules, navigation,
> ansible-lint. HTML/CSS: checks, completion and hover from MDN data, embedded CSS. Next: `bls doctor`,
> releases and a VS Code extension.
> See [docs/DESIGN.md](docs/DESIGN.md#milestones).

## Languages

| Language     | Subcommand    | Replaces (Node-based)                    | Status   |
|--------------|---------------|------------------------------------------|----------|
| JSON / JSONC | `bls json`    | `vscode-json-language-server`            | ✅ available |
| YAML (+k8s)  | `bls yaml`    | `yaml-language-server`                   | ✅ available |
| Bash         | `bls bash`    | `bash-language-server`                   | ✅ available |
| Dockerfile   | `bls dockerfile` | `dockerfile-language-server-nodejs`   | ✅ available |
| Compose      | `bls compose` | `docker-compose-langserver`              | ✅ available |
| Both above   | `bls docker`  | one server for Dockerfile + Compose (Zed/Gram `docker-language-server`) | ✅ available |
| Ansible      | `bls ansible` | `ansible-language-server`                | ✅ available |
| HTML         | `bls html`    | `vscode-html-language-server`            | ✅ available |
| CSS          | `bls css`     | `vscode-css-language-server`             | ✅ available |

For Terraform, Helm, SQL, Python and TOML, use the existing non-Node servers (`terraform-ls`, `helm-ls`,
`sqls`, `ruff` + `ty`, `taplo`). [docs/DESIGN.md](docs/DESIGN.md#non-goals) has the details.

## Usage

```text
bls <language> [--stdio] [--log-file PATH] [--log-level debug|info|warn|error]
bls list        # languages compiled into this binary
bls version
```

It talks LSP over stdin/stdout. Logs go to stderr, which Zed shows in its language server log. You can
also symlink the binary as `<language>-ls` or `bls-<language>` and drop the subcommand.

## Build

Go is not installed on the host; every build runs in a container (podman by default, docker works too):

```bash
make build      # -> dist/bls
make check      # go vet + go test
make install    # -> ~/.local/bin/bls
```

Details: [docs/BUILD.md](docs/BUILD.md).

## Editor setup (Gram, Zed, VS Code)

Full guide with every server name: [docs/EDITORS.md](docs/EDITORS.md). In short, point the editor's
existing server names at `bls`, for example in Gram's `~/.config/gram/settings.jsonc` or Zed's
`~/.config/zed/settings.json`:

```jsonc
{
  "lsp": {
    "yaml-language-server": { "binary": { "path": "/home/USER/.local/bin/bls", "arguments": ["yaml"] } }
  },
  "prettier": { "allowed": false }
}
```

`"prettier"` only matters in Zed, which downloads Node; Gram ships without it.

## Documentation

- [docs/LANGUAGES.md](docs/LANGUAGES.md): features and settings per language, schema cache, limitations
- [docs/DESIGN.md](docs/DESIGN.md): architecture, module interface, per-language plans, milestones
- [docs/BUILD.md](docs/BUILD.md): container-based build, Dockerfile stages, updating Go
- [docs/EDITORS.md](docs/EDITORS.md): Gram, Zed and VS Code configuration, server names, troubleshooting
- [docs/CHANGELOG.md](docs/CHANGELOG.md): what changed and when

## Layout

```text
cmd/bls/            entry point, subcommand / symlink dispatch
internal/jsonrpc/   JSON-RPC 2.0 + LSP framing, cancellation, server->client calls
internal/protocol/  LSP 3.17 types (subset)
internal/document/  immutable document snapshots, UTF-8/16/32 position mapping
internal/lang/      module interfaces and registry
internal/server/    lifecycle, document sync, debounced diagnostics, request routing
internal/lsptest/   in-process LSP client for tests
internal/tree/      format-neutral value tree with byte ranges (JSON and YAML)
internal/jsonast/   error-tolerant JSON/JSONC parser and formatter
internal/yamlast/   YAML (multi-document) to tree, via goccy/go-yaml
internal/schema/    JSON Schema store/cache, SchemaStore catalog, validation, completion/hover data
internal/schemalang/ shared code of the schema-driven modules
internal/extproc/   running optional external tools (shellcheck, bash help, whatis)
langs/json/, langs/yaml/, langs/bash/, langs/dockerfile/, langs/compose/  language modules
langs/docker/       routes Dockerfiles and Compose files to the right module
langs/ansible/      Ansible (YAML module + module docs read from installed modules + ansible-lint)
langs/css/, langs/html/  CSS and HTML (HTML reuses the CSS functions for <style> and style="")
internal/webdata/   embedded HTML/CSS reference data (vscode-custom-data, MIT)
langs/all/          build-tag-guarded imports of every language module
scripts/go.sh       run the Go toolchain in a container
```

## Third-party data

`internal/webdata` embeds the HTML and CSS data of
[microsoft/vscode-custom-data](https://github.com/microsoft/vscode-custom-data) (MIT License, see
`internal/webdata/LICENSE.vscode-custom-data`). It is plain JSON downloaded by `go generate`; no npm is involved.
