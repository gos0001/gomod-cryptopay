# syntax=docker/dockerfile:1

# --platform=$BUILDPLATFORM pins the builder to the machine actually running the
# build, and GOOS/GOARCH below come from the *target*. The Go toolchain then
# cross-compiles natively instead of running under QEMU emulation for the foreign
# architecture — the difference between seconds and minutes per arch.
#
# Must not drift below the go directive in go.mod, or the build fails on a
# language feature the toolchain does not have.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src

# Dependencies resolve in their own layer so source edits do not re-download.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO off makes the binary static, which is what lets the runtime stage be this
# thin. -trimpath keeps build paths out of the binary; -s -w drop the symbol and
# DWARF tables.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/cryptopay ./cmd


FROM alpine:3.21

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# org.opencontainers.image.source is what links the package to its repository on
# GHCR, so the image inherits the repo's visibility and README.
LABEL org.opencontainers.image.title="gomod-cryptopay" \
      org.opencontainers.image.description="Crypto payment service: invoices, on-chain watchers for TRON and BSC, signed webhooks" \
      org.opencontainers.image.source="https://github.com/gos0001/gomod-cryptopay" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

# ca-certificates for outbound TLS — every RPC call and every webhook needs it —
# and tzdata so timestamps are not stuck on UTC when TZ is set.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app

COPY --from=builder /out/cryptopay /usr/local/bin/cryptopay

# Never root: a container escape starts from whatever the process already had.
USER app

# One listener. There is no admin port to keep private.
EXPOSE 8080

# busybox wget, already in alpine. Loopback rather than the published address, so
# the check works regardless of how the container is networked.
#
# The port is fixed here while app.addr in the configuration is not: an operator
# who moves the listener has to override this HEALTHCHECK too, or the container
# will report unhealthy while serving perfectly well.
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

# The configuration file is the only input and is deliberately not baked into the
# image — it carries the API keys, the TronGrid key and the database URL. Mount it
# at the path below. Without it the process exits immediately with a message
# naming the path it looked at.
#
# Split across ENTRYPOINT and CMD on purpose: CMD is what arguments replace, so
# `docker run <image> version` and `docker run <image> -config /custom.json` both
# work while the default path is still supplied when nothing is passed.
ENTRYPOINT ["cryptopay"]
CMD ["-config", "/etc/cryptopay/config.json"]
