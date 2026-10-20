# Tasks: Prometheus Metrics & OpenTelemetry W3C Trace Context Observability

**Input**: Design documents from `/specs/004-observability/`
**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [quickstart.md](quickstart.md)
**Tests**: Strict Test-Driven Development (Red-Green-Refactor) per Constitution Principle III (`go test -race ./...`).

---

## Phase 1: Setup & Subsystem Instrumentation Hooks

**Purpose**: Establish lightweight observer hooks in existing subsystems so `telemetry.Registry` can record auth, rate-limit, cache, and policy decisions without circular dependencies.

- [x] **T001** [P] Add optional decision callback `SetOnDecision(func(allowed bool))` to `auth.Manager` in `internal/auth/middleware.go` and unit test in `internal/auth/middleware_test.go`.
- [x] **T002** [P] Add optional decision callback `SetOnDecision(func(allowed bool))` to `ratelimit.Limiter` in `internal/ratelimit/limiter.go` and unit test in `internal/ratelimit/limiter_test.go`.
- [x] **T003** [P] Add optional outcome callback `SetOnOutcome(func(result string))` (`"revalidated"`, `"miss"`, `"bypass"`) to `cache.Cache` in `internal/cache/memory.go` and unit test in `internal/cache/memory_test.go`.
- [x] **T004** [P] Add optional policy decision callback `SetOnPolicyDecision(func(allowed bool))` to `proxy.Handler` in `internal/proxy/handler.go` and unit test in `internal/proxy/handler_test.go`.

---

## Phase 2: User Story 1 - Prometheus Metrics Scraping & Quota Visibility (Priority: P1) 🎯 MVP

**Goal**: Expose a standard Prometheus 0.0.4 metrics endpoint (`GET /_gateway/metrics` and `GET /_relay/metrics`) with atomic counters, request latency histograms, and dynamic gauges for token pool (`ghp_...XXXX` masked IDs), cache, policy, auth keys, and rate-limiter buckets.

**Independent Test**: Run `go test -race -v ./internal/telemetry/...` and verify `GET /_gateway/metrics` returns `200 OK` with `Content-Type: text/plain; version=0.0.4; charset=utf-8` and accurate counter, histogram, and gauge metrics with zero raw token leakage.

### Tests for User Story 1 (TDD Red Phase)

- [x] **T005** [US1] Write table-driven unit tests in `internal/telemetry/metrics_test.go` covering:
  - Prometheus `text/plain; version=0.0.4; charset=utf-8` content-type and `405 Method Not Allowed` on non-GET requests.
  - Counter increments (`requests_total{method,status}`, `auth_decisions_total`, `ratelimit_decisions_total`, `cache_events_total`, `policy_decisions_total`).
  - Cumulative histogram buckets (`request_duration_seconds_bucket{le="..."}`, `_sum`, `_count`).
  - Dynamic gauges (`uptime_seconds`, `active_auth_keys`, `policy_rules`, `cache_entries`, `ratelimit_active_buckets`, `tokens_total`, `token_remaining{token_id="ghp_...XXXX"}`, `token_in_cooldown{token_id="ghp_...XXXX"}`) and verification that raw tokens never appear in output.
  - Concurrent `ObserveRequest` and `ServeHTTP` scraping under `-race`.

### Implementation for User Story 1 (TDD Green Phase)

- [x] **T006** [US1] Implement `Registry` and `NewMetricsHandler` in `internal/telemetry/metrics.go` using Go standard library (`sync/atomic`, `sync.RWMutex`, `fmt`, `sort`, `time`).

---

## Phase 3: User Story 2 & User Story 3 - W3C Trace Context Propagation & Trace-Correlated Structured Logging (Priority: P2 & P3)

**Goal**: Parse incoming W3C `traceparent` (`00-<32-hex-trace-id>-<16-hex-parent-id>-<2-hex-flags>`) and `tracestate` headers, generate a child `span_id` (or root trace when absent/malformed), propagate updated `traceparent`/`tracestate` upstream, set `traceparent` on downstream responses, record request metrics in `Registry`, and emit structured `log/slog` records with `trace_id`, `span_id`, `parent_span_id`, `method`, `path`, `status`, and `duration_ms`.

**Independent Test**: Run `go test -race -v ./internal/telemetry/...` verifying valid `traceparent` child span generation, malformed/all-zero fallback to new root trace, `tracestate` preservation, response header injection, and `slog` output correlation with zero secret leakage.

### Tests for User Story 2 & 3 (TDD Red Phase)

- [x] **T007** [US2] [US3] Write table-driven unit tests in `internal/telemetry/trace_test.go` covering:
  - `ParseTraceParent` with valid W3C header (`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`) vs malformed headers (wrong version, uppercase hex, wrong lengths, all-zero `trace-id`, all-zero `parent-id`).
  - Middleware propagation of same `trace_id` + new 16-hex `span_id` + `tracestate` to upstream request and `traceparent` to downstream response.
  - Structured `slog.Logger` output capturing `trace_id`, `span_id`, `parent_span_id`, `method`, `path`, `status`, and `duration_ms` without logging `Authorization` or `X-Gateway-Key`.

### Implementation for User Story 2 & 3 (TDD Green Phase)

- [x] **T008** [US2] [US3] Implement `TraceContext`, `ParseTraceParent`, `FromContext`, and `NewObservabilityMiddleware` in `internal/telemetry/trace.go` using `crypto/rand`, `encoding/hex`, and `log/slog`.

---

## Phase 4: Gateway Wiring, End-to-End Integration & Operational Tasks

**Purpose**: Wire `telemetry.Registry` and `NewObservabilityMiddleware` into `cmd/gateway/app.go`, add end-to-end integration tests in `cmd/gateway/main_test.go`, and add `bin:metrics` to `.mise/tasks/bin.toml` and `README.md`.

- [x] **T009** [US1] [US2] [US3] Write end-to-end integration tests in `cmd/gateway/main_test.go` verifying `/_gateway/metrics` (and `/_relay/metrics`) counters/gauges across allowed, cached (304), policy-blocked (403), and rate-limited (429) requests, plus end-to-end W3C `traceparent` propagation to upstream mock GitHub server and client response.
- [x] **T010** [US1] [US2] [US3] Wire `telemetry.Registry`, subsystem callbacks (`AuthMgr`, `Limiter`, `Cache`, `Proxy`), `NewObservabilityMiddleware`, and `/_gateway/metrics` + `/_relay/metrics` routes in `cmd/gateway/app.go`.
- [x] **T011** [P] Add `bin:metrics` task to `.mise/tasks/bin.toml` (`curl -s http://localhost:${GITHUB_API_GATEWAY_PORT:-28280}/_gateway/metrics`) and document Prometheus metrics & OpenTelemetry W3C Trace Context in `README.md` and `specs/002-github-actions-ci/`.
- [x] **T012** Run full verification gate (`mise run check-all`) with race detector enabled (`-race`) and verify zero warnings or regressions.
