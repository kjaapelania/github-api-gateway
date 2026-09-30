# Implementation Plan: GitHub API Rate Limit Relay

**Branch**: `001-rate-limit-relay` | **Date**: 2026-09-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/001-rate-limit-relay/spec.md`

## Summary

Build an enterprise-grade Go reverse proxy service that provides rate-limit relief for GitHub API requests while enforcing strict security boundaries. The service manages an inline multi-token pool with Largest Remaining Quota selection, unified 10s reset buffer & probe cooldown (zero background threads), considerate default 3000 token reserve, and unauthenticated fallback. It supports multiple relay keys for zero-downtime secret rotation and gates all traffic behind a fail-closed YAML access policy. The policy supports Option 2 pattern matching (unambiguous separation between `repo`/`repo_regex` and `path`/`path_regex`), flexible String-or-Array syntax, mandatory validated HTTP methods, and hardcoded denylists for sensitive endpoints. Zero-quota performance is achieved via standard in-memory ETag conditional revalidation (`304 Not Modified`), with native preservation of GitHub's S3 CDN asset redirects.

## Technical Context

**Language/Version**: Go 1.22+
**Primary Dependencies**: 
- Go standard library (`net/http`, `net/http/httputil`, `sync`, `regexp`, `log/slog`, `time`, `path`, `strings`)
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
| **II. Rate Limit Relief** | Largest-remaining pool + fallback + ETag cache | Dynamic header-based token pool selection, unauthenticated fallback, and ETag revalidation. | **PASS** |
| **III. Strict TDD** | Tests written before code | Write failing `httptest` cases for each module before implementation. | **PASS** |
| **IV. Observability & Security** | Token masking + status metrics | Never log raw tokens; expose `/_relay/status` with quota telemetry. | **PASS** |
| **V. Fail-Closed Governance** | Hardcoded default deny & mutual exclusivity | Default deny enforced in code; reject conflicting `repo`/`repo_regex` at startup; block `/user/**`. | **PASS** |

## Source Code Layout

```text
cmd/
└── gateway/
    ├── app.go              # App lifecycle, component wiring, in-memory zero-restart hot-reload
    ├── main.go             # Entry point, CLI flags, SIGHUP / graceful shutdown handlers
    └── main_test.go        # Comprehensive E2E integration tests (auth, policy, cache, reload, ratelimit)

internal/
├── config/
│   ├── config.go           # Canonical GITHUB_API_GATEWAY_* config parser and dotenv loader
│   └── config_test.go      # TDD tests for config defaults and environment overrides
├── policy/
│   ├── model.go            # Rule structs with StringOrSlice YAML unmarshaler
│   ├── evaluator.go        # Fail-closed policy evaluator (denylist, glob, regex, method match)
│   ├── parser.go           # YAML parser (gopkg.in/yaml.v3) & method/pattern startup validation
│   └── policy_test.go      # TDD tests for policy evaluation, default deny, string/array, and validation errors
├── token/
│   ├── pool.go             # Thread-safe Largest-Remaining TokenPool with 10s cooldown & unauthenticated fallback
│   └── pool_test.go        # TDD tests for largest-remaining selection, 10s cooldown, thresholds, and fallback behavior
├── auth/
│   ├── middleware.go       # Multi-key gateway protection (SetKeys rotation, X-Gateway-Key / X-Relay-Key / Bearer)
│   └── middleware_test.go  # TDD tests for multi-key authorization, rotation, and 401 Unauthorized
├── ratelimit/
│   ├── limiter.go          # Per-client token-bucket rate limiter (60 RPM default, burst 10, dynamic UpdateLimits)
│   └── limiter_test.go     # TDD tests for token bucket math, burst, Retry-After header, and independent client buckets
├── cache/
│   ├── memory.go           # In-memory ETag response cache & 304 revalidation
│   └── memory_test.go      # TDD tests for cache hits, misses, and 304 handling
├── proxy/
│   ├── handler.go          # ReverseProxy handler integrating policy check, cache, & TokenPool
│   └── handler_test.go     # TDD tests using mock upstream GitHub server
└── telemetry/
    ├── status.go           # /_gateway/status (and /_relay/status) health and quota telemetry
    └── status_test.go      # TDD tests for status endpoint
```

**Architecture & Pipeline Ordering**:
$$\text{Request} \longrightarrow \text{AuthMgr} \longrightarrow \text{RateLimiter} \longrightarrow \text{ETag Cache} \longrightarrow \text{Policy Evaluator} \longrightarrow \text{Token Pool} \longrightarrow \text{GitHub}$$

- **Zero-Restart Reload**: `App.Reload()` validates new syntax before atomically swapping pointers; syntax or parse errors safely roll back to previous state without dropping active traffic.
- **Client Rate Limiting**: Pure Go standard library token-bucket with per-second refill, protecting the shared token pool from rogue / noisy neighbor clients.

