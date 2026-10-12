#!/usr/bin/env bash
# Build release archives for every target, in containers (same scripts/go.sh as development).
# Output in dist/release/: bls_<version>_<os>_<arch>.tar.gz (Windows: .zip with bls.exe),
# the Zed/Gram extension and SHA256SUMS.
#
#   scripts/dist.sh                 # version from git describe
#   VERSION=v0.1.0 scripts/dist.sh
#   TARGETS="linux/amd64" scripts/dist.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64}"
OUT="$REPO_ROOT/dist/release"

# mkzip is a stdlib-Go helper (scripts/mkzip) built once, in a container, for Windows archives.
# go.sh builds in a container whose cwd is the repo, so the output path is repo-relative.
MKZIP_BIN=""
build_mkzip() {
    mkdir -p "$REPO_ROOT/dist/stage"
    "$REPO_ROOT/scripts/go.sh" build -o dist/stage/mkzip ./scripts/mkzip
    MKZIP_BIN="$REPO_ROOT/dist/stage/mkzip"
}

build_target() {
    local os="$1" arch="$2" name stage bin
    name="bls_${VERSION}_${os}_${arch}"
    stage="$REPO_ROOT/dist/stage/$name"
    rm -rf "$stage"
    mkdir -p "$stage"
    bin="bls"
    [[ "$os" == "windows" ]] && bin="bls.exe"
    GOOS="$os" GOARCH="$arch" "$REPO_ROOT/scripts/go.sh" build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o "dist/stage/$name/$bin" ./cmd/bls
    cp "$REPO_ROOT/README.md" "$REPO_ROOT/LICENSE" "$stage/"
    mkdir -p "$stage/third-party"
    cp "$REPO_ROOT/internal/webdata/LICENSE.vscode-custom-data" "$stage/third-party/"
    if [[ "$os" == "windows" ]]; then
        build_mkzip
        "$MKZIP_BIN" "$stage" "$OUT/$name.zip"
        printf 'built %s\n' "$OUT/$name.zip"
    else
        tar -C "$REPO_ROOT/dist/stage" -czf "$OUT/$name.tar.gz" "$name"
        printf 'built %s\n' "$OUT/$name.tar.gz"
    fi
}

main() {
    rm -rf "$OUT" "$REPO_ROOT/dist/stage"
    mkdir -p "$OUT"
    local t
    for t in $TARGETS; do
        build_target "${t%/*}" "${t#*/}"
    done
    if [[ "${SKIP_EXTENSION:-}" == "" ]]; then
        "$REPO_ROOT/scripts/zed-extension.sh"
        tar -C "$REPO_ROOT/dist" -czf "$OUT/bls-zed-extension_${VERSION}.tar.gz" zed-extension
    fi
    (cd "$OUT" && sha256sum -- * > SHA256SUMS)
    rm -rf "$REPO_ROOT/dist/stage"
    printf '\n'
    cat "$OUT/SHA256SUMS"
}

main "$@"
