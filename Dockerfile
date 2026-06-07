# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build

# ca-certificates are copied into the runtime image so outbound TLS (SimpleFIN
# bridge, Vault) works.
RUN apk add --no-cache ca-certificates

WORKDIR /src

# Download modules first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the dashboard WebAssembly client and gzip it so the server can embed it.
RUN GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" \
    -o internal/dashboard/web/app.wasm ./cmd/kasas-wasm \
    && gzip -9 -f internal/dashboard/web/app.wasm

ARG VERSION=dev
# CGO is disabled: modernc.org/sqlite is pure Go, so the result is a fully
# static binary with no libc dependency (runs as-is on the Alpine runtime).
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/kasas ./cmd/kasas

# ---- runtime stage ----
# Alpine (not scratch) so the Unraid Tailscale plugin can inject Tailscale: its
# container hook is a /bin/sh script that shells out to a package manager (it
# detects and uses apk) to pull tailscale/tailscaled at startup. scratch and
# distroless lack both a shell and a package manager, so the hook bails and the
# app starts without Tailscale. Alpine adds ~8 MB and is otherwise unused at runtime.
FROM alpine:3.22

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/kasas /kasas

# Run as a non-root user. The mounted /data volume must be writable by this UID
# (see README).
#
# NOTE: the Unraid Tailscale hook requires root (it runs apk, copies into
# /usr/bin, and starts tailscaled). Unraid overrides the entrypoint but NOT the
# user, so when enabling Tailscale add `--user 0` to the container's Extra
# Parameters -- otherwise the hook prints "No root privileges!" and starts kasas
# without Tailscale.
USER 65532:65532

EXPOSE 8080
VOLUME ["/data"]

ENV KASAS_DATABASE_PATH=/data/kasas.db \
    KASAS_SECRETS_FILE=/data/secrets.json \
    KASAS_SERVER_ADDR=:8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/kasas", "healthcheck"]

ENTRYPOINT ["/kasas"]
CMD ["serve"]
