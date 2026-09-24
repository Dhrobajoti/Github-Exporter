# Base images are pinned by digest for reproducible, tamper-evident builds.
# Dependabot (docker ecosystem) proposes updates to both the tag and the digest.
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/github-exporter .

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /

COPY --from=builder /usr/local/bin/github-exporter /usr/local/bin/github-exporter
USER 65532:65532

# TCP check on LISTEN_PORT; works with or without TLS and authentication.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["github-exporter", "healthcheck"]

ENTRYPOINT ["github-exporter"]
