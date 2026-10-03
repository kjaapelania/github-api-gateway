# Multi-stage Docker build for github-api-gateway
# Core Port for Containerization supporting dual targets: Debian (default) and Alpine

# ------------------------------------------------------------------------------
# 1. Shared Builder Stage
# ------------------------------------------------------------------------------
FROM golang:1.25-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates git

# Cache Go module dependencies
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# Copy source code and build statically linked binary (CGO_ENABLED=0)
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/bin/gateway ./cmd/gateway

# ------------------------------------------------------------------------------
# 2. Hardened Alpine Target (Traditional minimal image, <10MB, BusyBox /bin/sh)
# ------------------------------------------------------------------------------
FROM alpine:3.20 AS alpine

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

RUN addgroup -g 1000 -S gateway && adduser -u 1000 -S gateway -G gateway

COPY --from=builder --chown=gateway:gateway /app/bin/gateway /app/gateway

USER gateway
EXPOSE 8080
ENTRYPOINT ["/app/gateway"]

# ------------------------------------------------------------------------------
# 3. SOTA Hardened Distroless Target (Zero-trust production, <2MB base, 0 CVEs, 0 shell)
# ------------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS distroless

WORKDIR /app

COPY --from=builder /app/bin/gateway /app/gateway

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/gateway"]

# ------------------------------------------------------------------------------
# 4. Debian Target (Default stage for local dev and debugging, with bash & curl)
# ------------------------------------------------------------------------------
FROM debian:bookworm-slim AS debian

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    curl \
    bash \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

RUN groupadd -g 1000 gateway && useradd -u 1000 -g gateway -s /bin/bash -m gateway

COPY --from=builder --chown=gateway:gateway /app/bin/gateway /app/gateway

USER gateway
EXPOSE 8080
ENTRYPOINT ["/app/gateway"]
