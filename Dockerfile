# Pure-Go build: zetasql-wasm (analyzer) and ncruces/go-sqlite3
# (storage) both run on wazero, so no CGO and no gcc are required.
# Alpine ships Go without a C toolchain; the final image is
# distroless/static (ca-certs + tzdata, no glibc).
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /build
# Copy the dep manifests first to leverage the Docker layer cache for
# `go mod download`.
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG REVISION

# CGO_ENABLED=0 keeps the build pure-Go even though the builder has no
# gcc — without it, Go's default of 1 would cause the build to fail
# trying to invoke a C toolchain. Setting it explicitly also makes
# the static-link intent obvious to readers.
RUN CGO_ENABLED=0 \
    go build \
    -trimpath \
    -ldflags "-s -w -X main.revision=${REVISION}" \
    -o /go/bin/bigquery-emulator \
    ./cmd/bigquery-emulator

# distroless/static carries only ca-certificates and tzdata — enough
# for a statically-linked pure-Go binary. About 20 MB smaller than
# base-debian12 and free of glibc.
FROM gcr.io/distroless/static-debian12

COPY --from=builder /go/bin/bigquery-emulator /bin/bigquery-emulator

WORKDIR /work

ENTRYPOINT ["/bin/bigquery-emulator"]
