# Pure-Go build: zetasql-wasm runs through wazero (no CGO), so the
# Dockerfile no longer needs clang / CGO_ENABLED=1 / linkmode=external.
# Reads version.go's `const Version` for the tag (gobump-managed); the
# REVISION build-arg still flows in as the git SHA via -ldflags.
ARG GO_VERSION=1.26
ARG DEBIAN_VERSION=bookworm

FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS builder

WORKDIR /build
# Copy the dep manifests first to leverage the Docker layer cache for
# `go mod download`.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG TARGETOS
ARG TARGETARCH
ARG REVISION

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 \
    go build \
    -trimpath \
    -ldflags "-s -w -X main.revision=${REVISION}" \
    -o /go/bin/bigquery-emulator \
    ./cmd/bigquery-emulator

# distroless/static includes ca-certificates and tzdata while staying
# minimal. Pure-Go binaries are statically linked by default, so
# /static is the right tier (no glibc needed).
FROM gcr.io/distroless/static-debian12

COPY --from=builder /go/bin/bigquery-emulator /bin/bigquery-emulator

WORKDIR /work

ENTRYPOINT ["/bin/bigquery-emulator"]
