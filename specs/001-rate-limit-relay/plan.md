# Implementation Plan: GitHub API Rate Limit Relay

**Branch**: `001-rate-limit-relay` | **Date**: 2026-09-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/001-rate-limit-relay/spec.md`

## Summary

Build a high-performance, zero-external-dependency Go reverse proxy service that intercepts GitHub API traffic to eliminate unauthenticated rate limits. The service injects configured GitHub PAT credentials (elevating quota from 60 to 5,000 req/hr), enforces optional bearer auth protection on the relay, directly redirects latest release downloads (`302 Found`) to offload bandwidth and save API calls, and maintains an in-memory ETag cache that leverages `304 Not Modified` conditional requests for zero-quota repeated queries.

## Technical Context

**Language/Version**: Go 1.22+ (using only standard library)
**Primary Dependencies**: None (Standard library: `net/http`, `net/http/httputil`, `sync`, `log/slog`, `time`)
**Storage**: In-memory LRU/map cache with mutex synchronization (zero external DB required)
**Testing**: Go standard `testing` package with `net/http/httptest`
**Target Platform**: Linux / macOS cross-platform binary or container
**Project Type**: HTTP microservice / daemon
**Performance Goals**: < 2ms proxy overhead, < 30MB memory footprint, 1,000+ concurrent requests
**Constraints**: Zero external dependencies (per Constitution Principle I); strict TDD (Principle III)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Requirement | Compliance Plan | Status |
| :--- | :--- | :--- | :--- |
| **I. Go Standard Library First** | Zero external dependencies | Use exclusively `net/http`, `net/http/httputil`, `sync`, `log/slog`. | **PASS** |
| **II. Rate Limit Relief** | Token injection + ETag caching | Inject `GITHUB_TOKEN` into `Authorization`; leverage `If-None-Match` upstream. | **PASS** |
| **III. Strict TDD** | Tests written before code | Write failing `httptest` cases for each module before implementing code. | **PASS** |
| **IV. Observability & Security** | Token masking + status metrics | Never log raw tokens; expose `/_relay/status` with quota telemetry. | **PASS** |

## Project Structure

### Documentation (this feature)

```text
specs/001-rate-limit-relay/
├── spec.md              # Requirements, user stories, success criteria
├── plan.md              # Architectural plan and package layout
└── tasks.md             # Actionable, TDD-ordered tasks (/speckit-tasks output)
```

### Source Code Layout

```text
cmd/
└── relay/
    └── main.go             # Entry point, flag/env loading, graceful shutdown

internal/
├── config/
│   ├── config.go           # Configuration parser (env vars & flags)
│   └── config_test.go      # TDD tests for default/custom config
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
│   ├── handler.go          # ReverseProxy handler injecting GITHUB_TOKEN & rate limit headers
│   └── handler_test.go     # TDD tests using mock upstream GitHub server
└── telemetry/
    ├── status.go           # /_relay/status handler (exposing rate limit & uptime)
    └── status_test.go      # TDD tests for status endpoint
```

**Structure Decision**: Idiomatic Go standard layout separating binary entry point (`cmd/relay`) from testable, decoupled internal packages (`internal/*`).

## Complexity Tracking

*No violations. All components adhere strictly to the Go standard library and Constitution.*
