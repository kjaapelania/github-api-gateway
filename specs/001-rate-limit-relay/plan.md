# Implementation Plan: GitHub API Rate Limit Relay

**Branch**: `001-rate-limit-relay` | **Date**: 2026-09-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/001-rate-limit-relay/spec.md`

## Summary

Build an enterprise-grade Go reverse proxy service that provides rate-limit relief for GitHub API requests while enforcing strict security boundaries. The service manages a multi-token pool with quota rotation and unauthenticated fallback, and gates all traffic behind a fail-closed YAML access policy. The policy supports Option 2 pattern matching (unambiguous separation between `repo`/`repo_regex` and `path`/`path_regex`), method filtering, and hardcoded denylists for sensitive endpoints. In addition, it optimizes latest release downloads via `302 Found` redirects and leverages in-memory ETag caching for zero-quota `304 Not Modified` responses.

## Technical Context

**Language/Version**: Go 1.22+
**Primary Dependencies**: 
- Go standard library (`net/http`, `net/http/httputil`, `sync`, `regexp`, `log/slog`, `time`, `path`)
- `gopkg.in/yaml.v3` (Authorized by Constitution Principle I Amendment for YAML policy parsing)
**Storage**: In-memory thread-safe maps and sync primitives (resilient across restarts via GitHub's authoritative rate-limit headers)
**Testing**: Go standard `testing` package with `net/http/httptest`
**Target Platform**: Cross-platform binary / container
**Performance Goals**: < 2ms proxy overhead, < 30MB memory footprint, 1,000+ concurrent requests
**Constraints**: Controlled dependencies (Principle I); Strict TDD (Principle III); Fail-closed security (Principle V)

## Constitution Check

*GATE: Must pass before implementation.*

| Principle | Requirement | Compliance Plan | Status |
| :--- | :--- | :--- | :--- |
| **I. Controlled Dependencies** | Standard lib + `yaml.v3` exemption | Use standard library for networking, regex, and concurrency; `gopkg.in/yaml.v3` exclusively for policy parsing. | **PASS** |
| **II. Rate Limit Relief** | Token pool + fallback + ETag cache | Dynamic header-based token pool rotation, unauthenticated fallback, and ETag revalidation. | **PASS** |
| **III. Strict TDD** | Tests written before code | Write failing `httptest` cases for each module before implementation. | **PASS** |
| **IV. Observability & Security** | Token masking + status metrics | Never log raw tokens; expose `/_relay/status` with quota telemetry. | **PASS** |
| **V. Fail-Closed Governance** | Hardcoded default deny & mutual exclusivity | Default deny enforced in code; reject conflicting `repo`/`repo_regex` at startup; block `/user/**`. | **PASS** |

## Source Code Layout

```text
cmd/
└── relay/
    └── main.go             # Entry point, flag/env loading, graceful shutdown

internal/
├── config/
│   ├── config.go           # Configuration parser (GITHUB_TOKENS, POLICY_PATH, etc.)
│   └── config_test.go      # TDD tests for config defaults and parsing
├── policy/
│   ├── model.go            # Rule structs (repo/repo_regex, path/path_regex, methods)
│   ├── evaluator.go        # Fail-closed policy evaluator (denylist, glob, regex, method match)
│   ├── parser.go           # YAML parser (gopkg.in/yaml.v3) & mutual exclusivity validation
│   └── policy_test.go      # TDD tests for policy evaluation, default deny, and validation errors
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
│   ├── handler.go          # ReverseProxy handler integrating policy check & TokenPool
│   └── handler_test.go     # TDD tests using mock upstream GitHub server
└── telemetry/
    ├── status.go           # /_relay/status handler (exposing per-token quota & uptime)
    └── status_test.go      # TDD tests for status endpoint
```

**Structure Decision**: Idiomatic Go standard layout separating binary entry point (`cmd/relay`) from testable, decoupled internal packages (`internal/*`).
