# Implementation Plan: GitHub API Rate Limit Relay

**Branch**: `001-rate-limit-relay` | **Date**: 2026-09-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/001-rate-limit-relay/spec.md`

## Summary

Build a high-performance, zero-external-dependency Go reverse proxy service that eliminates unauthenticated rate limits. The service manages a pool of GitHub tokens (Classic, Fine-Grained, OAuth, App), tracks live quota through GitHub's response headers, rotates away from tokens whose remaining quota falls below `TOKEN_MIN_REMAINING`, falls back to unauthenticated requests if desired, optionally enforces relay authentication (`RELAY_AUTH_KEY`), redirects latest release downloads (`302 Found`), and leverages in-memory ETag caching for zero-quota `304 Not Modified` revalidation.

## Technical Context

**Language/Version**: Go 1.22+ (using strictly the Go standard library)
**Primary Dependencies**: None (Standard library: `net/http`, `net/http/httputil`, `sync`, `log/slog`, `time`, `strings`)
**Storage**: In-memory thread-safe maps and sync primitives (resilient across restarts via GitHub's authoritative rate-limit headers)
**Testing**: Go standard `testing` package with `net/http/httptest`
**Target Platform**: Cross-platform binary / container
**Performance Goals**: < 2ms proxy overhead, < 30MB memory footprint, 1,000+ concurrent requests
**Constraints**: Zero external dependencies (Constitution Principle I); strict TDD (Principle III)

## Constitution Check

*GATE: Must pass before implementation.*

| Principle | Requirement | Compliance Plan | Status |
| :--- | :--- | :--- | :--- |
| **I. Go Standard Library First** | Zero external dependencies | Standard library `net/http`, `sync`, `log/slog` exclusively. | **PASS** |
| **II. Rate Limit Relief** | Token pool + fallback + ETag cache | Dynamic header-based token pool rotation, unauthenticated fallback, and ETag revalidation. | **PASS** |
| **III. Strict TDD** | Tests written before code | Write failing `httptest` cases for each module before implementation. | **PASS** |
| **IV. Observability & Security** | Token masking + status metrics | Never log raw tokens; expose `/_relay/status` with quota telemetry. | **PASS** |

## Source Code Layout

```text
cmd/
└── relay/
    └── main.go             # Entry point, flag/env loading, graceful shutdown

internal/
├── config/
│   ├── config.go           # Configuration parser (GITHUB_TOKENS, RELAY_AUTH_KEY, etc.)
│   └── config_test.go      # TDD tests for config defaults and parsing
├── token/
│   ├── pool.go             # Thread-safe TokenPool with quota tracking, rotation, & fallback
│   └── pool_test.go        # TDD tests for rotation, thresholds, and fallback behavior
├── auth/
│   ├── middleware.go       # Optional relay protection (RELAY_AUTH_KEY validation)
│   └── middleware_test.go  # TDD tests for 401 Unauthorized vs authorized pass-through
├── redirect/
│   ├── release.go          # 302 Found redirector for latest release assets & URLs
│   └── release_test.go     # TDD tests for release redirect paths
├── cache/
│   ├── memory.go           # In-memory ETag response cache & 304 revalidation
│   └── memory_test.go      # TDD tests for cache hits, misses, and 304 handling
├── proxy/
│   ├── handler.go          # ReverseProxy handler using TokenPool & rate limit header propagation
│   └── handler_test.go     # TDD tests using mock upstream GitHub server
└── telemetry/
    ├── status.go           # /_relay/status handler (exposing per-token quota & uptime)
    └── status_test.go      # TDD tests for status endpoint
```

**Structure Decision**: Idiomatic Go standard layout separating binary entry point (`cmd/relay`) from testable, decoupled internal packages (`internal/*`).
