# Building bls

Go is **not** installed on the host. Every step (building, testing, `go mod tidy`, formatting) runs
inside a container. Podman is the default engine; docker works the same way.

## Requirements

- `podman` (rootless is fine) or `docker`
- `make`, `bash`, `git`

That's all. The Go toolchain comes from the pinned image `docker.io/library/golang:1.27.2-alpine3.24`.

## Quick reference

```bash
make build                 # ./dist/bls (static, CGO_ENABLED=0)
make test                  # go test ./...
make vet                   # go vet ./...
make check                 # vet + test
make fmt                   # gofmt in place
make tidy                  # go mod tidy (updates go.mod / go.sum)
make release               # hermetic build through the Dockerfile, result in ./dist/bls
make install               # build, then copy to ~/.local/bin/bls
make build TAGS=no_html,no_css   # leave languages out
make ENGINE=docker build   # use docker instead of podman
```

## How it works

There are two paths, for two different needs.

### 1. Development: `scripts/go.sh`

`scripts/go.sh <go args>` runs `go <args>` in a throwaway container:

- The repository is bind-mounted at `/src` (`:Z` relabels it for SELinux hosts).
- The module and build caches live on the host in `~/.cache/bls-build` (override with
  `BLS_BUILD_CACHE`), mounted at `/cache`. Repeated runs don't download or recompile everything again.
- Files created in the repo are owned by you:
  - podman: `--userns=keep-id` maps your UID into the container;
  - docker: `--user $(id -u):$(id -g)`. `HOME` points at the cache, so the container doesn't need a
    writable home directory.
- The engine is chosen in this order: `$ENGINE`, then `podman`, then `docker`.

All `make` targets except `release` use this script. You can call it directly too:

```bash
scripts/go.sh test ./internal/document -run TestApply -v
scripts/go.sh get mvdan.cc/sh/v3@latest
GO_IMAGE=docker.io/library/golang:1.27.2-bookworm scripts/go.sh build ./...
```

### 2. Release: `Dockerfile`

A multi-stage `Dockerfile` that doesn't depend on any host cache. It is named `Dockerfile` (not
`Containerfile`) so `docker build` finds it without `-f`; podman reads it too.

| Stage    | Purpose                                                              |
|----------|----------------------------------------------------------------------|
| `base`   | golang image, `go mod download`, copy the sources                    |
| `test`   | `go vet` + `go test` (`podman build --target test .`)                |
| `build`  | static binary, version and tags as build args                        |
| `export` | `FROM scratch` with only `/bls`, for `--output type=local`           |

```bash
podman build --target export --build-arg VERSION=v0.1.0 --output type=local,dest=dist .
docker buildx build --target export --build-arg VERSION=v0.1.0 --output type=local,dest=dist .
podman build --target export --build-arg TARGETARCH=arm64 --output type=local,dest=dist-arm64 .
```

## Embedded data

`internal/webdata` contains the HTML/CSS reference data (gzipped JSON from microsoft/vscode-custom-data,
MIT). The files are committed, so builds stay offline. To refresh them (downloads in the container):

```bash
scripts/go.sh generate ./internal/webdata/
```

## Version

`make` uses `git describe --tags --always --dirty` and passes it with
`-ldflags "-X main.version=..."`. `bls version` prints it.

## Updating the Go version

Change the image tag in both `scripts/go.sh` (`GO_IMAGE`) and `Dockerfile` (`ARG GO_IMAGE`), then change
the `go` line in `go.mod` if needed. To list the available tags:

```bash
podman search --list-tags --limit 5000 docker.io/library/golang | grep alpine | sort -V | tail
```

## Notes

- `go test -race` needs cgo and a C toolchain, which the alpine image doesn't have. If race tests
  become necessary, run them with `GO_IMAGE=...-bookworm` and `CGO_ENABLED=1`.
- The binary is fully static, so a binary built in the container runs on any Linux host.
