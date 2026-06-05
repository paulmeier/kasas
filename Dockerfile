# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25-alpine AS build

# ca-certificates are copied into the scratch image so outbound TLS (SimpleFIN
# bridge, Vault) works.
RUN apk add --no-cache ca-certificates

WORKDIR /src

# Download modules first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# CGO is disabled: modernc.org/sqlite is pure Go, so the result is a fully
# static binary that runs on scratch.
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/kasas ./cmd/kasas

# ---- runtime stage ----
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/kasas /kasas

# Run as a non-root user. The mounted /data volume must be writable by this UID
# (see README).
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
