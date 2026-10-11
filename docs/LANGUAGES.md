# Language reference

What each `bls` language does and how to configure it. Editor setup (where to put the settings) is in
[EDITORS.md](EDITORS.md).

## JSON / JSONC (`bls json`)

Status: **available** (milestone 2).

| Feature            | Details                                                                        |
|--------------------|--------------------------------------------------------------------------------|
| Syntax diagnostics | Error-tolerant parser: missing commas/colons/values, unterminated strings, duplicate keys (warning) |
| Schema diagnostics | JSON Schema drafts 4, 6, 7, 2019-09, 2020-12                                   |
| Completion         | Property names (snippets with `"key": …` by type, enum choices, defaults), values (enum, const, default, examples, booleans) |
| Hover              | Title, `markdownDescription`/`description`, type, allowed values (with `enumDescriptions`), default |
| Outline            | Every key; array items named by their `name`/`id`/`key`/`title`                |
| Formatting         | Keeps comments and single blank lines; refuses to touch documents with syntax errors |

JSONC (comments and trailing commas allowed) is used for language id `jsonc`, `*.jsonc`, `tsconfig.json`,
`jsconfig.json`, `.vscode/settings.json`, Zed/Gram `settings.json` / `keymap.json`, `devcontainer.json`
and `.eslintrc.json`. Plain JSON reports comments and trailing commas as errors.

### Which schema is used

1. `"$schema": "<url or path>"` in the document (relative paths are relative to the file)
2. `json.schemas` from the settings, in order
3. The [SchemaStore](https://www.schemastore.org) catalog (`package.json`, `tsconfig.json`, `.eslintrc.json`, and
   hundreds more), unless `json.schemaStore.enable` is `false`

### Settings

The format is the same as VS Code / vscode-json-languageserver:

```jsonc
{
  "json": {
    "validate": { "enable": true },
    "format": { "enable": true },
    "schemaStore": { "enable": true },
    "schemas": [
      // URL, absolute path, or path relative to the workspace root
      { "fileMatch": ["/config/*.json"], "url": "./schemas/config.schema.json" },
      { "fileMatch": ["*.app.json", "!test.app.json"], "url": "https://example.com/app.schema.json" },
      // inline schema
      { "fileMatch": ["local.json"], "schema": { "type": "object", "required": ["name"] } }
    ]
  }
}
```

`fileMatch` patterns: without `/` they match the file name, with `/` the end of the path. A leading `/` anchors
the pattern at the workspace root, `**` spans directories, `{a,b}` gives alternatives, and `!` excludes.

## YAML (`bls yaml`)

Status: **available** (milestone 2).

| Feature            | Details                                                                        |
|--------------------|--------------------------------------------------------------------------------|
| Syntax diagnostics | Parser errors, unknown aliases, duplicate keys (warning)                       |
| Schema diagnostics | Per document in multi-document files (`---`); anchors, aliases and `<<` merges are resolved first |
| Completion         | Keys by indentation (works while the file doesn't parse yet), inside sequences (`- `), values; multi-line snippets for objects (`key:\n  `) and arrays (`key:\n  - `) |
| Hover              | Same as JSON                                                                   |
| Outline            | Keys; with several documents, one entry per document (Kubernetes: `Kind name`) |
| Formatting         | Not implemented yet                                                            |

Strings that would change type (`yes`, `no`, `on`, `1.0`, `null`, …) are quoted when inserted.

### Which schema is used (per document)

1. Modeline anywhere in the file: `# yaml-language-server: $schema=<url or path>`
2. `yaml.schemas` from the settings
3. **Kubernetes**: a document with `apiVersion` and `kind` gets that kind's schema from
   [yannh/kubernetes-json-schema](https://github.com/yannh/kubernetes-json-schema) (standalone, strict:
   unknown fields are reported). Kinds that don't exist there (CRDs, kustomize, …) fall through to step 4.
4. The SchemaStore catalog (GitHub workflows, docker-compose, GitLab CI, Helm `Chart.yaml`, …), unless
   `yaml.schemaStore.enable` is `false`

### Settings

The format is the same as Red Hat's yaml-language-server, plus a `kubernetes` block:

```jsonc
{
  "yaml": {
    "validate": true,
    "schemaStore": { "enable": true },
    "schemas": {
      "https://json.schemastore.org/github-workflow.json": ".github/workflows/*.{yml,yaml}",
      "./schemas/app.schema.json": ["config/*.yaml", "!config/legacy.yaml"],
      // the key "kubernetes" limits Kubernetes schemas to these files
      "kubernetes": ["k8s/**/*.yaml", "manifests/*.yaml"]
    },
    "kubernetes": {
      "autoDetect": true,        // apply to any document with apiVersion + kind (default true)
      "version": "1.33.0",       // pin a Kubernetes version (default: latest, "master")
      "schemaBase": ""           // or point at your own copy of the schema directory
    }
  }
}
```

If `yaml.schemas.kubernetes` is set, Kubernetes schemas only apply to the matching files, whatever
`autoDetect` says.

## Bash and other shells (`bash`)

Status: **available** (milestone 3).

| Feature          | Details                                                                           |
|------------------|-----------------------------------------------------------------------------------|
| Diagnostics      | Parse errors (mvdan.cc/sh, the parser behind shfmt), then **shellcheck** if it is installed. Each finding links to its wiki page |
| Quick fixes      | shellcheck's suggested fix (e.g. adding quotes) and "Disable SCxxxx for this line"    |
| Formatting       | shfmt-compatible: indent from the editor, overridden by `.editorconfig` (`indent_style`, `indent_size`, `binary_next_line`, `switch_case_indent`, `space_redirects`, `keep_padding`, `function_next_line`, `shell_variant`) |
| Outline          | Functions (with the variables first assigned inside them) and global variables      |
| Go to definition | Functions and variables, also in `source`d / `.`-included files (up to 3 levels), and from `source file` to the file |
| References       | Function calls and variable uses in the file                                       |
| Hover            | Your functions/variables: definition line + the comment above it; builtins and keywords: `help` from bash; commands: `whatis` summary and path |
| Completion       | `$`/`${` → variables (file, sourced files, environment, shell variables); otherwise functions, keywords (with `if`/`for`/`while`/`case`/`function` snippets), builtins and commands on `PATH` |

The dialect comes from the shebang (`sh`/`dash` → POSIX, `bash`, `ksh`/`mksh`, `zsh`, `bats`), then the
extension (`.bats`, `.zsh`, `.ksh`); the default is bash. zsh files are parsed but not given to shellcheck
(it doesn't support zsh).

External tools are optional and none of them needs Node: `shellcheck` (diagnostics), `bash` (`help` text),
`whatis` (command summaries).

### Settings

Same names as bash-language-server (`bashIde`), so existing configurations keep working:

```jsonc
{
  "bashIde": {
    "shellcheckPath": "shellcheck",          // "" turns shellcheck off
    "shellcheckArguments": ["--exclude=SC1091"],
    "shfmt": {
      "ignoreEditorconfig": false,
      "languageDialect": "auto",             // auto, bash, posix, mksh, bats, zsh
      "binaryNextLine": false,
      "caseIndent": false,
      "funcNextLine": false,
      "keepPadding": false,
      "spaceRedirects": false,
      "simplifyCode": false
    }
  }
}
```

shellcheck runs from the script's directory with `--external-sources --source-path=SCRIPTDIR`, so
`source ./lib.sh` is followed.

## Dockerfile (`dockerfile`)

Status: **available** (milestone 4).

| Feature          | Details                                                                           |
|------------------|-----------------------------------------------------------------------------------|
| Diagnostics      | Unknown instruction; first instruction not FROM; missing arguments; duplicate stage names; invalid JSON (exec) form; CMD/ENTRYPOINT in shell form (info); several CMD/ENTRYPOINT/HEALTHCHECK in a stage; MAINTAINER deprecated; relative WORKDIR; unpinned base image (info); `COPY --from=` naming no stage (typo check) |
| hadolint         | Runs `hadolint` if installed (optional, not Node); findings link to the hadolint / shellcheck wiki |
| Completion       | Instructions (with snippets) at line start and after ONBUILD; flags per instruction; `--from=` stage names and indexes; `--mount=` types and per-type options; `--network=`; `$`/`${` → ARG/ENV and predefined build args; FROM: earlier stages, `scratch`, `AS`; HEALTHCHECK `CMD`/`NONE`; STOPSIGNAL signals |
| Hover            | Instruction syntax and description with a link to the reference; flags; stage references; variables (their ARG/ENV lines) |
| Go to definition | Stage references (`--from`, `FROM <stage>`) and variables                         |
| References       | Stage names and variables                                                          |
| Outline          | Global ARGs, then one entry per stage with its instructions                        |
| Formatting       | Upper-cases instruction keywords and `AS`; nothing is moved                        |

Parsing handles line continuations (including the `# escape=` directive), comment lines inside continuations,
heredocs (`RUN <<EOF`) and JSON form.

Settings (section `docker`):

```jsonc
{ "docker": { "hadolintPath": "hadolint", "hadolintArguments": ["--ignore", "DL3008"] } }
```

`"hadolintPath": ""` turns hadolint off.

## Docker Compose (`compose`)

Status: **available** (milestone 4).

Everything the YAML module does, with the Compose specification schema
(`compose-spec/compose-go/master/schema/compose-spec.json`, the one SchemaStore uses) applied to every
document, plus:

| Feature          | Details                                                                           |
|------------------|-----------------------------------------------------------------------------------|
| Reference checks | `depends_on`, `links`, `volumes_from`, `extends` (same file), `network_mode: service:x` → services; `networks` → top-level networks (`default` is implicit); named volumes (`name:/path` and `type: volume`) → top-level volumes; `secrets`, `configs` → top-level definitions |
| Completion       | Service names in `depends_on` (list and map form), `links`, `volumes_from`, `extends.service`; network, volume, secret and config names; `network_mode` values. Works while the file doesn't parse |
| Go to definition | From any reference to its definition                                               |
| References       | From a definition or reference: all uses                                           |
| Hover            | On a service reference: image, build and container name of the service; otherwise the schema description |

## Dockerfile + Compose in one server (`docker`)

`bls docker` routes each document: language id `dockerfile` or a file name like `Dockerfile`,
`Containerfile`, `Dockerfile.dev`, `*.dockerfile` → Dockerfile module; everything else → Compose module.
Use it where an editor attaches one server to both languages (Zed's/Gram's `docker-language-server`).

## Ansible (`ansible`)

Status: **available** (milestone 5).

Built on the YAML module. The schema is picked per file kind with ansible-lint's schemas (the ones SchemaStore
uses): playbooks, task lists (`tasks/`, `handlers/`), vars (`vars/`, `defaults/`, `host_vars/`,
`group_vars/`), role `meta/`, `requirements.yml`, `galaxy.yml`, inventories, execution environments. Files
outside those paths are recognized by content (a list of plays is a playbook, another list is a task list).

| Feature          | Details                                                                           |
|------------------|-----------------------------------------------------------------------------------|
| Module data      | Read directly from the installed module sources (`DOCUMENTATION` in each `.py`), no Python or `ansible-doc` run. ansible-core is found next to `ansible` on `PATH` or the configured path, in pip/pipx/system locations and in Mason's ansible-lint venv; collections from `ANSIBLE_COLLECTIONS_PATH`, `~/.ansible/collections`, `/usr/share/ansible/collections` and `site-packages/ansible_collections` |
| Completion       | Module names (FQCN by default) at task positions, with short descriptions for builtin modules; module options with documentation; option choices and booleans; role names |
| Hover            | Module documentation (description and option list) on the module key; option documentation (type, required, choices, default, aliases) on option keys; schema descriptions elsewhere |
| Checks           | Unknown modules (only for collections that are installed, so missing collections don't cause noise); invalid option choices; missing required options; unknown options (only for modules whose documentation is complete, i.e. without documentation fragments) |
| ansible-lint     | Runs when a file is opened and saved (it works on the saved file and takes seconds), with `--offline`; found on `PATH`, in the setting, or next to the detected ansible-core (e.g. Mason's venv). Findings link to the rule page |
| Go to definition | `roles:` items, `role:`, `include_role`/`import_role` `name` → `roles/<name>/tasks/main.yml` (also `ns.collection.role`); `include_tasks`/`import_tasks`/`import_playbook`/`include_vars`/`vars_files` → the file; `template` `src` → `templates/`, `copy`/`script`/`unarchive` `src` → `files/`; `notify` → the handler (by `name` or `listen`, in the playbook's handlers or the role's `handlers/main.yml`) |

Ansible's documentation markup (`C()`, `O()`, `V()`, `I()`, `B()`, `M()`, `L()`, `U()`) is shown as Markdown.

### Settings

Same names as ansible-language-server (section `ansible`); YAML settings (`yaml.schemas`, …) apply too:

```jsonc
{
  "ansible": {
    "ansible": {
      "path": "ansible",                          // used to find ansible-core's modules
      "useFullyQualifiedCollectionNames": true
    },
    "validation": {
      "enabled": true,
      "lint": { "enabled": true, "path": "ansible-lint", "arguments": "--profile production" }
    }
  }
}
```

## Schema download and cache

- Downloaded schemas and the SchemaStore catalog are cached in `~/.cache/bls/schemas/` (shared by all
  languages) and re-downloaded after 24 hours. If the network is down, the cached copy is used even when it's
  older than that.
- Schemas load in the background. Diagnostics appear as soon as the schema is ready; the first time a schema
  is used that takes as long as the download.
- Schemas may be JSON or YAML. Regular expressions that Go doesn't support (lookarounds) are treated as
  "match anything" instead of breaking the whole schema.

Environment variables:

| Variable             | Effect                                                              |
|----------------------|---------------------------------------------------------------------|
| `BLS_OFFLINE=1`      | Never use the network; only the cache and local files               |
| `BLS_SCHEMA_CATALOG` | Use another catalog URL (or `file://` path) instead of SchemaStore  |
| `XDG_CACHE_HOME`     | Moves the cache (`$XDG_CACHE_HOME/bls/schemas`)                     |

## Known limitations

- YAML: when the file has a syntax error, only the syntax error is reported (the parser stops at the first
  error), so schema diagnostics disappear until it's fixed. Completion still works.
- YAML completion inside flow collections (`{a: 1}`, `[x, y]`) isn't supported.
- `if`/`then`/`else` schemas are used for completion and hover only when the current value decides the
  branch; otherwise both branches are offered.
