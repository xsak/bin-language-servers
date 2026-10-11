# bls — Node-free language servers

`bls` is a single static Go binary that provides language servers for the
[Zed](https://zed.dev) editor (and any other LSP client), so that **no Node.js / npm / npx** is
needed on the machine.

> Status: **milestone 1 (core) done**. The protocol core, document handling, test harness and container
> build work. Language modules are coming in this order: JSON + YAML, Bash, Dockerfile + Compose, Ansible,
> HTML + CSS. See [docs/DESIGN.md](docs/DESIGN.md#milestones).

## Languages

| Language     | Subcommand    | Replaces (Node-based)                    | Status   |
|--------------|---------------|------------------------------------------|----------|
| JSON / JSONC | `bls json`    | `vscode-json-language-server`            | planned  |
| YAML (+k8s)  | `bls yaml`    | `yaml-language-server`                   | planned  |
| Bash         | `bls bash`    | `bash-language-server`                   | planned  |
| Dockerfile   | `bls dockerfile` | `dockerfile-language-server-nodejs`   | planned  |
| Compose      | `bls compose` | `docker-compose-langserver`              | planned  |
| Ansible      | `bls ansible` | `ansible-language-server`                | planned  |
| HTML         | `bls html`    | `vscode-html-language-server`            | planned  |
| CSS          | `bls css`     | `vscode-css-language-server`             | planned  |

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
langs/all/          build-tag-guarded imports of every language module
scripts/go.sh       run the Go toolchain in a container
```
