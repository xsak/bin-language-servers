#!/usr/bin/env bash
# Run the Go toolchain inside a container; Go is never installed on the host.
#
#   scripts/go.sh test ./...
#   scripts/go.sh mod tidy
#   ENGINE=docker scripts/go.sh build ./...
#
# The module and build caches live in a host directory, so repeated runs are fast and every
# file created in the repo is owned by the calling user (podman and docker alike).
set -euo pipefail

GO_IMAGE="${GO_IMAGE:-docker.io/library/golang:1.27.2-alpine3.24}"
CACHE_DIR="${BLS_BUILD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/bls-build}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

detect_engine() {
    if [[ -n "${ENGINE:-}" ]]; then
        printf '%s\n' "$ENGINE"
    elif command -v podman >/dev/null 2>&1; then
        printf 'podman\n'
    elif command -v docker >/dev/null 2>&1; then
        printf 'docker\n'
    else
        printf 'error: neither podman nor docker found\n' >&2
        return 1
    fi
}

main() {
    local engine
    engine="$(detect_engine)"
    mkdir -p "$CACHE_DIR/mod" "$CACHE_DIR/build"

    local -a user_args
    if [[ "$engine" == "podman" ]]; then
        # Rootless podman: map the host user into the container.
        user_args=(--userns=keep-id)
    else
        user_args=(--user "$(id -u):$(id -g)")
    fi

    local -a tty_args=()
    if [[ -t 0 && -t 1 ]]; then
        tty_args=(-it)
    fi

    exec "$engine" run --rm "${tty_args[@]}" "${user_args[@]}" \
        -v "$REPO_ROOT:/src:Z" \
        -v "$CACHE_DIR:/cache:Z" \
        -e HOME=/cache \
        -e GOMODCACHE=/cache/mod \
        -e GOCACHE=/cache/build \
        -e CGO_ENABLED=0 \
        -e GOFLAGS="${GOFLAGS:-}" \
        -w /src \
        "$GO_IMAGE" go "$@"
}

main "$@"
