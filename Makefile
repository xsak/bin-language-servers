# Every target runs inside a container (podman by default, docker if podman is missing).
# Override with: make ENGINE=docker <target>

ENGINE  ?= $(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TAGS    ?=
GOARCH  ?= amd64
GO      := ENGINE=$(ENGINE) scripts/go.sh
PREFIX  ?= $(HOME)/.local

.PHONY: all build test vet fmt tidy check fuzz release dist zed-extension generate install clean

all: check build

## build: static binary in ./dist/bls (uses the host-side module cache for speed)
build:
	@mkdir -p dist
	$(GO) build -trimpath -tags "$(TAGS)" -ldflags "-s -w -X main.version=$(VERSION)" -o dist/bls ./cmd/bls

## test: unit and protocol tests
test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

## fmt: gofmt the tree in place
fmt:
	$(GO) fmt ./...

## tidy: update go.mod/go.sum
tidy:
	$(GO) mod tidy

check: vet test

## fuzz: run every parser fuzz test for FUZZTIME (default 20s) each; crashes land in testdata/fuzz
FUZZTIME ?= 20s
FUZZ_PKGS := internal/jsonast internal/yamlast langs/toml langs/css langs/html langs/nginx langs/apache langs/ini langs/dockerfile
fuzz:
	@for p in $(FUZZ_PKGS); do echo "## $$p"; $(GO) test -run='^$$' -fuzz=FuzzParse -fuzztime=$(FUZZTIME) ./$$p || exit 1; done

## release: hermetic build via the Dockerfile (no host cache), result in ./dist/bls
release:
	$(ENGINE) build --target export --build-arg VERSION=$(VERSION) --build-arg TAGS="$(TAGS)" \
		--build-arg TARGETARCH=$(GOARCH) --output type=local,dest=dist .

## dist: release archives for all targets + Zed/Gram extension + SHA256SUMS in ./dist/release
dist:
	ENGINE=$(ENGINE) VERSION=$(VERSION) scripts/dist.sh

## zed-extension: build the Zed/Gram extension into ./dist/zed-extension (Rust in a container)
zed-extension:
	ENGINE=$(ENGINE) scripts/zed-extension.sh

## generate: refresh embedded data (HTML/CSS data, nginx/Apache directives) from the network
generate:
	$(GO) generate ./internal/webdata/ ./internal/confdata/

## install: copy dist/bls to $(PREFIX)/bin
install: build
	install -Dm755 dist/bls $(PREFIX)/bin/bls

clean:
	rm -rf dist
