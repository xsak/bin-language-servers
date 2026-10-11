# Editor setup

`bls` is a plain stdio language server. Editors in the Zed family (Zed, Gram) cannot define a brand-new
language server from settings alone, but they let you **replace the binary of a server they already know**.
That is how `bls` is plugged in.

> Only languages that `bls list` prints work. Pointing an editor at a language that isn't implemented yet
> makes the server fail to start (`unknown language`).

## Install the binary

```bash
make install            # -> ~/.local/bin/bls
bls list                # check which languages are compiled in
```

Use an **absolute path** in editor settings; `~` is not expanded.

## Gram

Gram is a Zed fork without AI, telemetry or Node. Out of the box it refuses to
download language servers (`Download disabled for language server ...` in the log), so most languages have
no server at all. `bls` fills those gaps.

- Settings: `~/.config/gram/settings.jsonc` (or command palette → `gram: open settings`)
- Log: `~/.local/share/gram/logs/Gram.log` (or `gram: open log`)
- Per-server log: command palette → `dev: open language server logs`

Server names, taken from Gram 3.3.0's log (rows marked * were not in the log yet; they are Zed's names and
need checking the first time you open such a file):

| Language        | Gram server name             | bls subcommand  | Where it comes from              |
|-----------------|------------------------------|-----------------|----------------------------------|
| JSON / JSONC    | `json-language-server`       | `json`          | built in                         |
| YAML            | `yaml-language-server`       | `yaml`          | built in                         |
| Bash            | `bash-language-server`       | `bash`          | built in                         |
| CSS             | `vscode-css-language-server`* | `css`          | built in                         |
| HTML            | `superhtml`                  | `html`          | built in                         |
| Dockerfile      | `dockerfile-language-server` | `dockerfile`    | `dockerfile` extension (not installed now) |
| Docker Compose  | `docker-language-server`     | `docker` (routes Dockerfile + Compose) | `dockerfile` or `docker-compose` extension |
| Ansible         | `ansible-language-server`*   | `ansible`       | `ansible` extension (not installed now) |

Notes:

- `superhtml` is a Zig binary, not Node. Using `bls html` for HTML is optional.
- `docker-language-server` is Docker's Go server, not Node. You can install it instead of using
  `bls compose`. If you use `bls`, remember that the same server name covers both Dockerfile and Compose
  in the `dockerfile` extension.
- For Ansible, install Gram's `ansible` extension first. It provides the language and the
  `ansible-language-server` name; `bls` replaces its binary.

### Ready now: JSON, YAML, Bash, Dockerfile, Compose and Ansible

These work today (`bls list`). Your Gram `settings.jsonc` already has a `json-language-server` entry, so
extend it and add the others:

```jsonc
"lsp": {
  "json-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["json"] },
    "enable_lsp_tasks": true
  },
  "yaml-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["yaml"] },
    "settings": {
      "yaml": {
        "schemas": { "kubernetes": ["k8s/**/*.yaml"] }   // optional, see LANGUAGES.md
      }
    }
  },
  "bash-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["bash"] }
  },
  // Dockerfile + Compose (dockerfile / docker-compose extensions). docker-language-server is attached to
  // both languages, so it gets the routing "docker" mode.
  "docker-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["docker"] }
  },
  "dockerfile-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["dockerfile"] }
  },
  // needs the "ansible" extension, which defines the Ansible language and this server name
  "ansible-language-server": {
    "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["ansible"] }
  }
}
```

Ansible files are only recognized as "Ansible" for the paths the extension maps. To make more YAML files
Ansible, extend `file_types`, e.g. `"file_types": { "Ansible": ["**/playbooks/*.yml", "**/roles/**/*.yml",
"site.yml"] }`.

If both `docker-language-server` and `dockerfile-language-server` run for a Dockerfile, you see every
Dockerfile diagnostic twice. Keep one, e.g. with
`"languages": { "Dockerfile": { "language_servers": ["docker-language-server", "!dockerfile-language-server"] } }`.

Settings for these languages (schema associations, shellcheck/shfmt options, Kubernetes version, turning SchemaStore off) are described in
[LANGUAGES.md](LANGUAGES.md).

### settings.jsonc (all languages)

Merge this into the existing `"lsp"` block (replace `/home/xsak` with your home directory). Only
enable the entries whose language `bls list` shows:

```jsonc
{
  "lsp": {
    "json-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["json"] },
      "enable_lsp_tasks": true
    },
    "yaml-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["yaml"] }
    },
    "bash-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["bash"] }
    },
    "vscode-css-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["css"] }
    },
    "superhtml": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["html"] }
    },
    "dockerfile-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["dockerfile"] }
    },
    "docker-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["docker"] }
    },
    "ansible-language-server": {
      "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["ansible"] }
    }
  }
}
```

Extra arguments go into the same list, for example
`"arguments": ["yaml", "--log-level", "debug", "--log-file", "/tmp/bls-yaml.log"]`.

Schema settings (`json.schemas`, `yaml.schemas`) go under `"settings"` next to `"binary"`, in the same
format as the Red Hat / VS Code servers:

```jsonc
"yaml-language-server": {
  "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["yaml"] },
  "settings": {
    "yaml": { "schemas": { "kubernetes": "k8s/**/*.yaml" } }
  }
}
```

### Check that it works

1. Save the settings. Gram restarts the affected servers; if it doesn't, run `editor: restart language server`
   from the command palette.
2. Open a file of that language and check the per-server log (`dev: open language server logs`). The
   server should show up as `bls-<language>` with the bls version.
3. `grep bls ~/.local/share/gram/logs/Gram.log` must not show `unknown language` or `Download disabled`
   for the servers you replaced.

## Zed

Zed uses the same mechanism as Gram, but unlike Gram it **downloads Node** (into
`~/.local/share/zed/node/`) for its Node-based servers and for Prettier. To keep it from doing that, every
Node-based server has to be either replaced by `bls` or turned off, and Prettier has to be disabled.

- Settings: `~/.config/zed/settings.json` (command palette → `zed: open settings`)
- Log: `~/.local/share/zed/logs/Zed.log`
- Server names (Zed's built-ins and the official extensions; check them in the log, they change between
  versions):

| Language       | Zed server name               | bls subcommand | Source                          |
|----------------|-------------------------------|----------------|---------------------------------|
| JSON / JSONC   | `json-language-server`        | `json`         | built in                        |
| YAML           | `yaml-language-server`        | `yaml`         | built in                        |
| Bash           | `bash-language-server`        | `bash`         | built in                        |
| CSS            | `vscode-css-language-server`  | `css`          | built in                        |
| HTML           | `vscode-html-language-server` | `html`         | `html` extension                |
| Dockerfile     | `dockerfile-language-server`  | `dockerfile`   | `dockerfile` extension          |
| Docker Compose | `docker-language-server`      | `docker`       | `docker-compose` extension      |
| Ansible        | `ansible-language-server`     | `ansible`      | `ansible` extension             |

### settings.json

Merge this into the existing settings (replace `/home/xsak`). Again, only enable what `bls list` shows:

```jsonc
{
  "lsp": {
    "json-language-server":        { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["json"] } },
    "yaml-language-server":        { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["yaml"] } },
    "bash-language-server":        { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["bash"] } },
    "vscode-css-language-server":  { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["css"] } },
    "vscode-html-language-server": { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["html"] } },
    "dockerfile-language-server":  { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["dockerfile"] } },
    "docker-language-server":      { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["docker"] } },
    "ansible-language-server":     { "binary": { "path": "/home/xsak/.local/bin/bls", "arguments": ["ansible"] } }
  },

  // Prettier runs on Node; Zed would download Node just for formatting.
  "prettier": { "allowed": false },

  "languages": {
    // Format with the language server (bls) instead of Prettier.
    "JSON":  { "formatter": "language_server" },
    "JSONC": { "formatter": "language_server" },
    "YAML":  { "formatter": "language_server" },
    "CSS":   { "formatter": "language_server" },
    "HTML":  { "formatter": "language_server" },
    // Python: Rust-based servers only; basedpyright/pyright are Node.
    "Python": { "language_servers": ["ruff", "ty", "!basedpyright", "!pyright"] },
    // No Node-based TypeScript/ESLint servers.
    "TypeScript": { "language_servers": ["!vtsls", "!typescript-language-server", "!eslint", "..."] },
    "JavaScript": { "language_servers": ["!vtsls", "!typescript-language-server", "!eslint", "..."] }
  }
}
```

Checking it works: same as for Gram, plus `ls ~/.local/share/zed/node/` should stay empty after you delete
the directory and use Zed for a while. If a Node version shows up again, `grep -i node
~/.local/share/zed/logs/Zed.log` tells you which server asked for it.

## VS Code

> VS Code itself runs on Electron, which has Node built in, and its built-in JSON, HTML, CSS and
> Markdown support runs Node-based language servers inside it. `bls` can replace those servers, but it
> can't make VS Code Node-free. Nothing extra has to be installed on the system, though: everything runs
> inside VS Code's own runtime.

VS Code can't start a language server from settings alone; every server needs a client extension.
Two options:

### Option A: bls extension (planned, recommended)

A small client extension in this repo (`editors/vscode/`, milestone 7) that registers `bls` for every
language it supports, with settings like:

```jsonc
{
  "bls.path": "/home/xsak/.local/bin/bls",
  "bls.languages": ["json", "yaml", "bash", "dockerfile", "compose", "ansible", "html", "css"],
  "bls.logLevel": "info"
}
```

It will be packaged into a `.vsix` inside a container, like everything else, and installed with
`code --install-extension dist/bls.vsix` (also works with `codium`).

### Option B: generic LSP client (works today, one language)

The [Generic LSP Client](https://github.com/llllvvuu/vscode-glspc) extension
(`llllvvuu.llllvvuu-glspc`) can start any stdio server, but **only one per installation**. That's
enough to use `bls` for a single language, for example YAML. Put this in your user `settings.json`
(command palette → `Preferences: Open User Settings (JSON)`):

```jsonc
{
  "glspc.serverCommand": "/home/xsak/.local/bin/bls",
  "glspc.serverCommandArguments": ["yaml"],
  "glspc.languageId": "yaml",
  "glspc.trace.server": "off"        // "verbose" -> output channel "Generic LSP Client"
}
```

### Avoid duplicate servers

If both `bls` and the original server run, you get every diagnostic and completion twice. Turn the
original off:

| Language   | Turn off                                                                        |
|------------|---------------------------------------------------------------------------------|
| YAML       | disable the `redhat.vscode-yaml` extension                                      |
| Ansible    | disable the `redhat.ansible` extension (or keep it for syntax and set `"ansible.validation.enabled": false`) |
| JSON       | Extensions → `@builtin json` → disable "JSON Language Features"                 |
| HTML / CSS | Extensions → `@builtin` → disable "HTML Language Features" / "CSS Language Features" |
| Bash       | uninstall `mads-hartmann.bash-ide-vscode` if installed                          |
| Dockerfile | uninstall `ms-azuretools.vscode-docker`'s language server, or the whole extension |

Disabling a built-in "Language Features" extension removes only the language server; syntax highlighting
comes from a separate built-in grammar extension and keeps working.
