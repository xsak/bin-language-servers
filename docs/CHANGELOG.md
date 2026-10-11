# Changelog

## Unreleased

### 2026-10-11: milestone 2, JSON + YAML

Added:

- `bls json`: JSON/JSONC diagnostics (tolerant parser), JSON Schema validation, completion (keys with
  snippets, values), hover, outline, comment-preserving formatting.
- `bls yaml`: multi-document YAML with anchors/aliases/merge keys, per-document schema validation, indentation
  based completion (works on invalid documents), hover, outline (one entry per document).
- Schema association: `$schema` / YAML modeline, `json.schemas` / `yaml.schemas` settings (VS Code and Red Hat
  formats, inline schemas), Kubernetes by `apiVersion`/`kind` (yannh/kubernetes-json-schema, strict),
  SchemaStore catalog.
- Schema store: background loading, disk cache in `~/.cache/bls/schemas` (24 h TTL, stale copy when offline),
  YAML schemas, lenient regexps, `BLS_OFFLINE`, `BLS_SCHEMA_CATALOG`.
- Server: settings are pulled with `workspace/configuration`; `insertTextMode` support.
- Docs: `docs/LANGUAGES.md`.

Checked against the live SchemaStore and Kubernetes schemas with the built binary over stdio (GitHub workflow,
Kubernetes Deployment, `package.json`).

### 2026-10-11: editor docs

- `docs/EDITORS.md`: Gram 3.3.0 setup (server names from its log, `settings.jsonc` snippet, checks),
  Zed setup (server names, full settings.json with Prettier and Node-based servers turned off),
  VS Code setup (generic LSP client for one language today, planned bls extension, list of servers to turn
  off).

### 2026-10-11: project start, milestone 1 (core)

Decisions:

- Single binary `bls` with subcommands (plus symlink dispatch). Languages can be left out with
  `-tags no_<lang>`.
- Languages to implement: JSON, YAML, Bash, Dockerfile, Compose, Ansible, HTML, CSS.
- Existing non-Node servers (terraform-ls, helm-ls, sqls, ruff, ty, taplo) get documentation and Zed
  config only.
- No Go on the host: all builds run in containers (podman now, docker later), using the pinned image
  `golang:1.27.2-alpine3.24`.

Added:

- `internal/jsonrpc`: Content-Length framing, ordered notifications, concurrent requests,
  `$/cancelRequest`, server-to-client `Call`.
- `internal/protocol`: the LSP 3.17 types used so far.
- `internal/document`: immutable snapshots, incremental edits, UTF-8/UTF-16/UTF-32 positions, `\n`,
  `\r\n` and `\r` line endings, file URI helpers.
- `internal/lang`: `Language` and the optional feature interfaces, the registry.
- `internal/server`: initialize/shutdown/exit, capabilities derived from the implemented interfaces,
  debounced diagnostics with stale-result dropping, configuration and save hooks.
- `internal/lsptest`: in-process client for end-to-end tests.
- `cmd/bls`: `version`, `list`, language subcommands, `--log-file`, `--log-level`.
- Container build: `scripts/go.sh`, `Makefile`, multi-stage `Dockerfile` with `test` and `export`
  stages.
- Docs: README, DESIGN, BUILD, CHANGELOG.
