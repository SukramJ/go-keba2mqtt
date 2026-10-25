# SPDX-License-Identifier: MIT
# Multi-stage build for go-keba2mqtt.
#
# CGO is disabled so the resulting binary is statically linked against
# Go's net package — that means the runtime image can be distroless,
# carrying no shell, no package manager and no userland to attack.
#
# The register catalog is compiled in (internal/keba) and the optional
# web UI's static assets are go:embed-ed, so the image carries the binary
# and the annotated config template and nothing else.

# ---------- Stage 1: build ----------
FROM golang:1.27.3-alpine AS builder
WORKDIR /src

# Cache go.mod / go.sum separately so unrelated source edits don't
# bust the dependency-download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/keba2mqtt ./cmd/keba2mqtt && \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-keba2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/keba-util ./cmd/keba-util

# ---------- Stage 2: runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Binaries + the annotated config template for reference.
COPY --from=builder /out/keba2mqtt /out/keba-util /app/
COPY --from=builder /src/config-template.yaml /app/

# /config is the canonical mount point for the operator's config.yaml.
# XDG_CONFIG_HOME steers config.Locate at the mount so a `docker run -v
# ./my-config:/config:ro` Just Works.
VOLUME ["/config"]
ENV XDG_CONFIG_HOME=/config

# Optional web UI. Off by default; when enabled set WEB_BIND to
# 0.0.0.0:8080 (the 127.0.0.1 default is unreachable from outside the
# container) and publish the port with `docker run -p 8080:8080`.
EXPOSE 8080

# Exec form — distroless has no shell to run a CMD-string healthcheck.
# Contract with the daemon's --healthcheck flag: exit 0 when healthy or
# when the diagnostic web UI is disabled, exit 1 when unhealthy.
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/app/keba2mqtt", "--healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/app/keba2mqtt"]
