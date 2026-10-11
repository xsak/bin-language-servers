# Changelog

## Unreleased

### 2026-10-11: security and robustness review

Changed:

- JSON-RPC bodies are capped at 256 MiB; a bad `Content-Length` is rejected instead of allocated.
- Panics in request handlers become JSON-RPC internal errors; panics on background goroutines are
  logged. The server survives a crashing parser.
- A schema downloaded over HTTP can no longer `$ref` a `file:` URL (local roots and inline schemas can).
- Schema cache writes use a unique temporary file per process before the rename.
- Bash `source` navigation reads regular files of at most 4 MiB only (no FIFOs, devices, huge files).
- nginx/Apache include navigation lists regular files only.
- GitHub Actions: `actions/checkout` pinned to a commit (v6.1.0).
- The document store is created when the server is, not in `initialize`. A module that calls
  `RefreshDiagnostics` from a goroutine started in `Init` (Ansible does) could hit a nil store and
  crash the process; `Store.SetEncoding` applies the negotiated encoding instead.

Fixed (found by the new fuzz tests):

- YAML: `<<: 000` (a merge key with a scalar value) crashed the tree builder.
- HTML: raw-text elements (`<textarea>`, `<script>`, ...) with invalid UTF-8 in their content crashed
  the parser, because the case-insensitive end-tag search lower-cased the text and shifted offsets.

Added:

- `FuzzParse` for every hand-written parser and `make fuzz`.

### 2026-10-11: license

- Added the MIT `LICENSE`; release archives include it.

### 2026-10-11: milestone 8, doctor, releases, HTML formatting

Added:

- `bls doctor`: installation, optional tools (shellcheck, hadolint, bash, whatis, ansible-lint, ansible-core),
  schema cache, Gram/Zed settings (server → subcommand, missing binaries, unknown subcommands), installed
  extensions without bls, Zed's Node.js and Prettier, recent language-server errors. Exit code 1 on problems.
- `make dist` / `scripts/dist.sh`: release archives for linux/darwin × amd64/arm64, Zed/Gram extension,
  SHA256SUMS. `scripts/go.sh` passes `GOOS`/`GOARCH` through.
- GitHub Actions: `ci.yml` (check + build), `release.yml` (tag → check, dist, GitHub release).
- HTML formatting: nesting-based re-indent, verbatim `<pre>`/`<textarea>`, shifted `<script>`/`<style>`.
- `make zed-extension`, `make generate`.

### 2026-10-11: milestone 7, TOML, INI, nginx, Apache, Zed/Gram extension

Added:

- `bls toml`: own tolerant parser to the shared tree, JSON Schema (`#:schema`, settings, SchemaStore), completion
  for tables/dotted keys/values, hover, outline, formatter.
- `bls ini`: INI/.conf/git config, systemd units and podman Quadlet (curated directives: completion, hover,
  closed-value checks), outline, style-preserving formatter.
- `internal/confdata`: nginx (808) and Apache (730) directive tables generated from the official docs.
- `bls nginx`: syntax, unknown directives, context checks per http/stream/mail family, completion, variables,
  hover, outline, re-indent, include/upstream navigation.
- `bls apache`: container structure, unknown directives, context checks (.htaccess aware), completion,
  hover, outline, re-indent, Include navigation.
- `editors/zed`: Zed/Gram extension (API 0.6, wasm32-wasip2) declaring `bls-ini`, `bls-toml`, `bls-apache`
  and an `Apache Config` language; built in a Rust container by `scripts/zed-extension.sh`.
- Docs: Zed's docker-compose server name is `docker-compose`, nginx's is `nginx`.

Checked live: pyproject.toml against SchemaStore, systemd hover, nginx and Apache context errors.

### 2026-10-11: milestone 6, HTML + CSS

Added:

- `internal/webdata`: embedded vscode-custom-data (MIT) HTML/CSS data, refreshed with `go generate` in the
  container.
- `bls css`: parser with nesting, checks (unknown properties/at-rules/pseudo-selectors, invalid keyword
  values), completion, hover with specificity, outline, comment-preserving formatter.
- `bls html`: parser with implied end tags/void/raw-text elements, checks (structure, duplicate
  attributes/ids, unknown elements, img alt), completion (elements, close tags, attributes, values,
  entities), hover, outline, linked editing; CSS features inside `<style>` and `style=""`.
- Server: `textDocument/linkedEditingRange`.

Checked live with the release build (10.1 MB): HTML structure and inline CSS errors, CSS checks, specificity
hover.

### 2026-10-11: milestone 5, Ansible

Added:

- `bls ansible`: ansible-lint schemas per file kind; module index read from installed ansible-core and
  collections (DOCUMENTATION strings, YAML 1.1 booleans, doc markup to Markdown); completion of modules,
  options, choices and roles; module/option hover; checks for unknown modules, invalid choices, missing
  required and unknown options; ansible-lint on open/save (`--offline`, codeclimate); go-to-definition for
  roles, includes/imports, vars files, templates/files and notify → handlers.
- YAML hooks: per-document `Schema`, `Name.Doc` / `Name.Insert`, `yaml.DocAt`.

Checked live: 628 modules indexed from Mason's ansible-core venv and `~/.ansible/collections`, ansible-lint
26.9.0 found automatically, invalid `ufw` rule flagged, `copy` hover shows the real documentation.

### 2026-10-11: milestone 4, Dockerfile + Compose

Added:

- `bls dockerfile`: own parser (continuations, escape directive, heredocs, JSON form), built-in checks,
  optional hadolint, completion (instructions, flags, `--from` stages, `--mount` options, variables), hover,
  definition/references for stages and variables, outline, keyword formatting.
- `bls compose`: YAML module + Compose spec schema + reference checks (services, networks, volumes, secrets,
  configs), name completion, definition, references, service hover.
- `bls docker`: one server for both, routing by language id / file name.
- YAML module `Hooks` (schema URL, extra completion names) and `yaml.SectionKeys`.
- Schema validation: a `false` subschema on a property now reads "Property X is not allowed."

Checked live with the built binary: Compose schema downloaded and applied, undefined service reported,
Dockerfile checks and hovers.

### 2026-10-11: milestone 3, Bash

Added:

- `bls bash`: parse errors (mvdan.cc/sh), shellcheck diagnostics with wiki links, quick fixes (shellcheck fix,
  disable directive), shfmt-compatible formatting with `.editorconfig`, outline, go-to-definition and
  references (including `source`d files), hover (comments, `help`, `whatis`), completion (variables,
  functions, keywords with snippets, builtins, `PATH` commands). Settings: `bashIde` (bash-language-server
  names).
- Protocol/server: `textDocument/references`, `textDocument/codeAction`, `codeDescription` on diagnostics.
- `internal/extproc` for optional external tools.

Checked live with the built binary, real shellcheck 0.9.0, bash `help` and `whatis`.

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
