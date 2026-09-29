# Multi-stage Docker build for github-api-relay

# Build Stage
FROM golang:1.23-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates git

# Cache module dependencies
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# Copy source code and build statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/bin/relay ./cmd/relay

# Production Minimal Runtime Stage (< 20MB, Non-root)
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Run as non-privileged system user for enterprise container security
RUN addgroup -S relay && adduser -S relay -G relay

COPY --from=builder --chown=relay:relay /app/bin/relay /app/relay

USER relay

EXPOSE 8080

ENTRYPOINT ["/app/relay"]
