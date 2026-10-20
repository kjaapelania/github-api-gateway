# Implementation Plan: Prometheus Metrics & OpenTelemetry W3C Trace Context Observability

**Branch**: `004-observability` | **Date**: 2026-10-21 | **Spec**: [specs/004-observability/spec.md](spec.md)

**Input**: Feature specification from `/specs/004-observability/spec.md`

## Summary

Implement zero-dependency, standard-library-native **Prometheus Metrics Exposition (`/_gateway/metrics` & `/_relay/metrics`)** and **OpenTelemetry W3C Trace Context Propagation (`traceparent` & `tracestate`) with Structured Log Correlation (`log/slog`)**:

1. **Prometheus Metrics Collector & Handler (`internal/telemetry/metrics.go`)**:
   - Thread-safe `Registry` using `sync/atomic` and `sync.RWMutex` for bounded request counters (`method`, `status`), latency histograms (`0.005` to `10` seconds), cache outcome counters (`revalidated`, `miss`, `bypass`), policy decision counters (`allow`, `deny`), rate-limit decision counters (`allow`, `throttle`), and auth decision counters (`allow`, `reject`).
   - Live dynamic gauges queried at scrape time from `token.Pool` (`tokens_total`, per-masked-token `token_remaining` and `token_in_cooldown`), `cache.Cache` (`cache_entries`), `policy.Policy` (`policy_rules`), `auth.Manager` (`active_auth_keys`), and `ratelimit.Limiter` (`ratelimit_active_buckets`).
   - Standard Prometheus text exposition 0.0.4 formatter (`text/plain; version=0.0.4; charset=utf-8`).
2. **W3C Trace Context & Structured Logging Middleware (`internal/telemetry/trace.go`)**:
   - W3C Trace Context parser and generator (`00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>`) using `crypto/rand` and `encoding/hex`.
   - Middleware wrapping the gateway request pipeline:
     - Parses incoming `traceparent` (and optional `tracestate`), or generates a fresh root trace context if missing/invalid.
     - Creates a new child `span_id` for the gateway operation while preserving `trace_id` and recording `parent_span_id`.
     - Stores `TraceContext` in `r.Context()`, updates `r.Header.Set("traceparent", ...)` so `httputil.ReverseProxy` propagates the child span upstream, and sets `w.Header().Set("traceparent", ...)` on the downstream response.
     - Records request count and duration in `Registry` and emits a structured `slog.InfoContext` access log with `trace_id`, `span_id`, `parent_span_id`, `method`, `path`, `status`, and `duration_ms`.
3. **Subsystem Instrumentation & Wiring (`cmd/gateway/app.go`, `internal/auth`, `internal/ratelimit`, `internal/cache`, `internal/proxy`)**:
   - Non-intrusive observer callbacks or registry hooks on `auth.Manager`, `ratelimit.Limiter`, `cache.Cache`, and `proxy.Handler` to increment subsystem decision counters accurately.
   - Register `/_gateway/metrics` and `/_relay/metrics` on `App` mux and add `bin:metrics` task to `.mise/tasks/bin.toml`.

## Technical Context

**Language/Version**: Go 1.25.8

**Primary Dependencies**: Go Standard Library (`net/http`, `sync`, `sync/atomic`, `crypto/rand`, `encoding/hex`, `log/slog`, `fmt`, `strings`, `sort`, `time`) + `gopkg.in/yaml.v3` (existing policy exemption)

**Storage**: In-memory atomic counters and histograms (`internal/telemetry.Registry`)

**Testing**: `go test -race -v ./...` (`net/http/httptest`)

**Target Platform**: Cross-platform static binaries (Linux, macOS, Windows) & multi-target containers (Debian, Alpine, Distroless)

**Project Type**: Enterprise HTTP Reverse Proxy / API Gateway

**Performance Goals**: `< 1ms` metrics scrape overhead, `< 5µs` per-request trace/metrics middleware overhead, zero external allocations on atomic counter increments

**Constraints**: Zero third-party observability SDK dependencies (`prometheus/client_golang` and `go.opentelemetry.io/otel` are excluded per Constitution Principle I); zero secret leakage in metric labels or logs

**Scale/Scope**: Bounded metric cardinality (`method` x `status`, masked `token_id`) safe for high-throughput multi-client traffic

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Requirement | Compliance Status |
| :--- | :--- | :--- |
| **I. Go Standard Library First** | Use Go stdlib only (`gopkg.in/yaml.v3` sole exemption) | **PASS** — Prometheus text format 0.0.4 and W3C TraceContext implemented natively in `internal/telemetry` |
| **II. Transparent Reverse Proxy & Rate Limit Relief** | Preserve token injection, ETag caching, and header forwarding | **PASS** — `traceparent` and `tracestate` propagate cleanly through `httputil.ReverseProxy` |
| **III. Strict Test-Driven Development** | Write `httptest` unit/integration tests first (Red-Green-Refactor) | **PASS** — Table-driven unit tests for `metrics_test.go`, `trace_test.go`, and end-to-end integration tests in `cmd/gateway/main_test.go` |
| **IV. Observability & Graceful Failure** | Use `log/slog`, never log raw tokens/secrets | **PASS** — Per-token metrics use `TokenInfo.ID` (`ghp_...XXXX`); logs never record `Authorization` or `X-Gateway-Key` |
| **V. Fail-Closed Enterprise Access Governance** | Default deny & hardcoded denylist preserved | **PASS** — Policy decisions are counted (`allow` vs `deny`) without altering policy evaluation semantics |

## Project Structure

### Documentation (this feature)

```text
specs/004-observability/
├── plan.md              # This file
├── research.md          # Phase 0 architecture & protocol decisions
├── data-model.md        # Phase 1 metric & trace context entities
├── quickstart.md        # Phase 1 operator verification guide
├── checklists/
│   └── requirements.md  # Spec quality checklist
└── tasks.md             # Phase 2 actionable TDD task breakdown
```

### Source Code (repository root)

```text
cmd/gateway/
├── app.go               # Wire telemetry.Registry, /_gateway/metrics, and trace/metrics middleware
└── main_test.go         # End-to-end integration tests for /_gateway/metrics & W3C TraceContext

internal/telemetry/
├── status.go            # Existing JSON status handler (/_gateway/status)
├── status_test.go       # Existing status handler unit tests
├── metrics.go           # Prometheus Registry, counters, histograms, gauges, and 0.0.4 HTTP handler
├── metrics_test.go      # Unit tests for Prometheus exposition, histogram buckets, and masked token gauges
├── trace.go             # W3C traceparent/tracestate parser, child span generator, and slog middleware
└── trace_test.go        # Unit tests for valid/malformed traceparent, root generation, and log correlation

internal/auth/
└── middleware.go        # Optional decision hook for auth allow/reject counters

internal/ratelimit/
└── limiter.go           # Optional decision hook for ratelimit allow/throttle counters

internal/cache/
└── memory.go            # Optional outcome hook for cache revalidated/miss/bypass counters

internal/proxy/
└── handler.go           # Optional decision hook for policy allow/deny counters
```

**Structure Decision**: Extend the existing `internal/telemetry` package with `metrics.go` and `trace.go` so all observability concerns live cohesively alongside `status.go`, with lightweight callback hooks into `auth`, `ratelimit`, `cache`, and `proxy`.
