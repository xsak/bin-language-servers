#!/usr/bin/env bash
# Build the Zed/Gram extension (Rust -> wasm32-wasip2) in a container; Rust is never installed on
# the host. Output: dist/zed-extension/ (extension.toml, extension.wasm, languages/, LICENSE).
#
#   scripts/zed-extension.sh            # build
#   scripts/zed-extension.sh install    # build, then copy into Gram's and Zed's extension dirs
set -euo pipefail

RUST_IMAGE="${RUST_IMAGE:-docker.io/library/rust:1.99.0-alpine3.24}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CACHE_DIR="${BLS_BUILD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/bls-build}"
OUT="$REPO_ROOT/dist/zed-extension"

engine() {
    if [[ -n "${ENGINE:-}" ]]; then printf '%s\n' "$ENGINE"
    elif command -v podman >/dev/null 2>&1; then printf 'podman\n'
    else printf 'docker\n'; fi
}

build() {
    local e user_args
    e="$(engine)"
    mkdir -p "$CACHE_DIR/cargo" "$CACHE_DIR/rustup-target"
    if [[ "$e" == "podman" ]]; then user_args=(--userns=keep-id); else user_args=(--user "$(id -u):$(id -g)"); fi
    "$e" run --rm "${user_args[@]}" \
        -v "$REPO_ROOT/editors/zed:/src:Z" \
        -v "$CACHE_DIR/cargo:/cargo:Z" \
        -e CARGO_HOME=/cargo -e CARGO_TARGET_DIR=/cargo/target-zed \
        -w /src "$RUST_IMAGE" \
        sh -c 'rustup target add wasm32-wasip2 >/dev/null 2>&1 || true; cargo build --release --target wasm32-wasip2'
    rm -rf "$OUT"
    mkdir -p "$OUT"
    cp "$CACHE_DIR/cargo/target-zed/wasm32-wasip2/release/bls_zed.wasm" "$OUT/extension.wasm"
    cp "$REPO_ROOT/editors/zed/extension.toml" "$REPO_ROOT/LICENSE" "$OUT/"
    cp -r "$REPO_ROOT/editors/zed/languages" "$OUT/"
    printf 'built %s\n' "$OUT"
}

install_into() {
    local dir="$1/extensions/installed/bls"
    [[ -d "$1" ]] || return 0
    rm -rf "$dir"
    mkdir -p "$dir"
    cp -r "$OUT"/. "$dir/"
    printf 'installed into %s (restart the editor)\n' "$dir"
}

build
if [[ "${1:-}" == "install" ]]; then
    install_into "${XDG_DATA_HOME:-$HOME/.local/share}/gram"
    install_into "${XDG_DATA_HOME:-$HOME/.local/share}/zed"
fi
