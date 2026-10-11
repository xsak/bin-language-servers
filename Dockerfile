# syntax=docker/dockerfile:1
# Builds bls entirely inside containers. Works with podman and docker.
#
#   podman build --target export --output type=local,dest=dist .
#   docker buildx build --target export --output type=local,dest=dist .

ARG GO_IMAGE=docker.io/library/golang:1.27.2-alpine3.24

FROM ${GO_IMAGE} AS base
WORKDIR /src
ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath
COPY go.mod go.sum* ./
RUN go mod download
COPY . .

FROM base AS test
RUN go vet ./... && go test ./...

FROM base AS build
ARG VERSION=dev
ARG TAGS=""
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -tags "${TAGS}" -ldflags "-s -w -X main.version=${VERSION}" -o /out/bls ./cmd/bls

# Export stage: only the binary, for --output type=local.
FROM scratch AS export
COPY --from=build /out/bls /bls

