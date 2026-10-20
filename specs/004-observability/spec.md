# Feature Specification: Prometheus Metrics & OpenTelemetry W3C Trace Context Observability

**Feature Branch**: `004-observability`

**Created**: 2026-10-21

**Status**: Approved

**Input**: User description: "Implement Prometheus & OpenTelemetry observability (/_gateway/metrics + W3C TraceContext) via SpecKit (specs/004-observability)"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Prometheus Metrics Scraping & Quota Visibility (Priority: P1)

As a platform operator monitoring the GitHub API Gateway in production, I want a standard Prometheus metrics endpoint (`GET /_gateway/metrics` and `GET /_relay/metrics`) exposing real-time request throughput, latency histograms, ETag cache hit/revalidation ratios, policy allow/deny counts, client rate-limiting throttles, and per-token GitHub quota remaining (using masked token identifiers), so that I can build dashboards and alert on quota exhaustion or traffic spikes before builds fail.

**Why this priority**: Real-time metrics scraping is the foundational operational requirement for production alerting on token quota depletion, rate-limit throttling, and upstream latency.

**Independent Test**: Send representative HTTP requests (allowed, policy-denied, rate-limited, cached 304 revalidated) through the gateway, then query `GET /_gateway/metrics` and verify valid Prometheus text exposition format (`text/plain; version=0.0.4; charset=utf-8`) containing accurate counter, gauge, and histogram values with masked token labels.

**Acceptance Scenarios**:

1. **Given** a running gateway that has processed authorized, cached, policy-blocked, and rate-limited requests, **When** an operator queries `GET /_gateway/metrics` (or `GET /_relay/metrics`), **Then** the gateway returns `200 OK` with `Content-Type: text/plain; version=0.0.4; charset=utf-8` and accurate `# HELP`, `# TYPE`, counter, gauge, and histogram lines for requests, durations, cache outcomes, policy decisions, rate-limit decisions, and token pool state.
2. **Given** configured GitHub Personal Access Tokens in the pool, **When** `GET /_gateway/metrics` is scraped, **Then** per-token metrics (`github_api_gateway_token_remaining` and `github_api_gateway_token_in_cooldown`) label each token exclusively by its masked identifier (e.g., `ghp_...18WX`) and never expose raw secret values.
3. **Given** a non-GET request (`POST`, `PUT`, `DELETE`) to `/_gateway/metrics`, **When** received by the gateway, **Then** the gateway responds with `405 Method Not Allowed`.

---

### User Story 2 - OpenTelemetry W3C Trace Context Propagation (Priority: P2)

As a distributed systems engineer debugging multi-service CI/CD pipelines, I want the gateway to parse incoming W3C Trace Context headers (`traceparent` and `tracestate`), generate a child span for the gateway operation (or initiate a new trace when no valid `traceparent` is provided), propagate the updated `traceparent` and `tracestate` upstream, and echo `traceparent` in the client response headers, so that end-to-end traces remain unbroken across service boundaries.

**Why this priority**: Distributed trace propagation connects client requests, gateway middleware execution, and upstream calls into a single trace tree without requiring proprietary agents.

**Independent Test**: Issue an HTTP request with a valid W3C `traceparent` header (`00-<32-hex-trace-id>-<16-hex-parent-id>-01`) and `tracestate` header, verify the upstream server receives the same `trace-id` with a newly generated 16-hex child `span-id` and preserved `tracestate`, and verify the client HTTP response includes the `traceparent` header.

**Acceptance Scenarios**:

1. **Given** an incoming request with a valid W3C `traceparent` (`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`) and `tracestate` (`vendor=opaque`), **When** the gateway proxies the request, **Then** it preserves the 32-hex `trace_id` (`4bf92f3577b34da6a3ce929d0e0e4736`), generates a new 16-hex `span_id`, forwards the updated `traceparent` and `tracestate` upstream, and sets `traceparent` on the HTTP response.
2. **Given** an incoming request without a `traceparent` header (or with a malformed/all-zero header), **When** the gateway processes the request, **Then** it generates a fresh cryptographically random 32-hex `trace_id` and 16-hex `span_id` (`trace-flags` = `01`) and includes the resulting `traceparent` header on both the upstream request and downstream response.

---

### User Story 3 - Trace-Correlated Structured Access Logging (Priority: P3)

As an incident responder investigating a failed or slow API request, I want structured access logs emitted with OpenTelemetry trace correlation fields (`trace_id`, `span_id`, `parent_span_id`, `method`, `path`, `status`, `duration_ms`), so that I can jump directly from a response's `traceparent` header or Prometheus exemplar to the exact log entries for that request.

**Why this priority**: Correlating structured logs with trace IDs eliminates manual timestamp guesswork during incident triage.

**Independent Test**: Capture structured log output while serving a request with a known `traceparent`, and verify the emitted log entry contains the matching `trace_id`, `span_id`, `parent_span_id`, HTTP `method`, `path`, `status`, and `duration_ms`, with zero secret leakage.

**Acceptance Scenarios**:

1. **Given** an incoming API request with a valid `traceparent`, **When** request processing completes, **Then** a structured log entry is emitted containing `trace_id`, `span_id`, `parent_span_id`, `method`, `path`, `status`, and `duration_ms`.
2. **Given** an incoming request carrying `Authorization`, `X-Gateway-Key`, or `X-Relay-Key` headers, **When** structured access logs or metrics are recorded, **Then** no raw credentials or keys ever appear in log fields or metric labels.

---

### Edge Cases

- **Malformed `traceparent` headers**: If an incoming `traceparent` has the wrong number of hyphen-separated segments, unsupported version format, non-lowercase-hex characters, an all-zero `trace-id` (`00000000000000000000000000000000`), or an all-zero `parent-id` (`0000000000000000`), the gateway MUST safely ignore the invalid header and generate a fresh root trace context without rejecting the request.
- **High-cardinality path explosion in metrics**: To prevent unbounded memory growth from arbitrary URL paths (e.g., `/repos/owner/repo/commits/<sha>`), request counters and latency histograms MUST label by bounded attributes (`method` and `status`) rather than unbounded raw URL paths.
- **Concurrent scraping under load**: Scraping `/_gateway/metrics` while hundreds of requests are in flight MUST be data-race-free and lock-free on hot counters.
- **Hot-reload and dynamic state**: Gauges for active auth keys, policy rules, cache entries, active rate-limit buckets, and token pool statuses MUST dynamically reflect the latest state immediately after a hot-reload (`POST /_gateway/reload` or `SIGHUP`).

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The gateway MUST expose `GET /_gateway/metrics` and `GET /_relay/metrics` returning HTTP `200 OK` with `Content-Type: text/plain; version=0.0.4; charset=utf-8` in standard Prometheus text exposition format, and return `405 Method Not Allowed` for non-GET methods.
- **FR-002**: The Prometheus metrics output MUST include `# HELP` and `# TYPE` metadata lines and expose:
  - `github_api_gateway_uptime_seconds` (gauge)
  - `github_api_gateway_requests_total{method="...",status="..."}` (counter)
  - `github_api_gateway_request_duration_seconds` (histogram with buckets `0.005`, `0.01`, `0.025`, `0.05`, `0.1`, `0.25`, `0.5`, `1`, `2.5`, `5`, `10`, `+Inf`)
  - `github_api_gateway_cache_events_total{result="revalidated|miss|bypass"}` (counter)
  - `github_api_gateway_cache_entries` (gauge)
  - `github_api_gateway_policy_decisions_total{decision="allow|deny"}` (counter)
  - `github_api_gateway_policy_rules` (gauge)
  - `github_api_gateway_ratelimit_decisions_total{decision="allow|throttle"}` (counter)
  - `github_api_gateway_ratelimit_active_buckets` (gauge)
  - `github_api_gateway_auth_decisions_total{decision="allow|reject"}` (counter)
  - `github_api_gateway_active_auth_keys` (gauge)
  - `github_api_gateway_tokens_total` (gauge)
  - `github_api_gateway_token_remaining{token_id="..."}` (gauge, masked token ID only)
  - `github_api_gateway_token_in_cooldown{token_id="..."}` (gauge, `1` if in cooldown, `0` otherwise)
- **FR-003**: The gateway MUST implement W3C Trace Context (`traceparent` and `tracestate`) parsing and generation per the W3C Trace Context specification (`00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>`).
- **FR-004**: For every proxied request, the gateway MUST attach the active trace context to the request context, set the child `traceparent` (and preserve `tracestate` if valid/present) on the upstream request, and set `traceparent` on the downstream HTTP response.
- **FR-005**: The gateway MUST emit structured request log records via `log/slog` containing `trace_id`, `span_id`, `parent_span_id` (if derived from an incoming trace), `method`, `path`, `status`, and `duration_ms`.
- **FR-006**: The implementation MUST use only the Go standard library (zero external third-party dependencies added to `go.mod`) and never expose raw GitHub PATs or client auth keys in metrics or logs.
- **FR-007**: A convenience task `bin:metrics` MUST be added to `.mise/tasks/bin.toml` (and documented in `README.md`) to query `http://localhost:${GITHUB_API_GATEWAY_PORT:-28280}/_gateway/metrics`.

### Key Entities

- **MetricsRegistry (`internal/telemetry.Registry`)**: Thread-safe collector maintaining atomic request/decision counters, latency histogram buckets, and dynamic gauge callbacks for pool, cache, policy, auth, and rate-limiter subsystems.
- **TraceContext (`internal/telemetry.TraceContext`)**: Value object holding `Version` (`00`), `TraceID` (32 lowercase hex chars), `SpanID` (16 lowercase hex chars), `ParentSpanID` (16 lowercase hex chars, optional), `Flags` (2 lowercase hex chars), and `TraceState`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Operators can scrape `/_gateway/metrics` in under 10ms with zero data races under concurrent request load (`go test -race ./...`).
- **SC-002**: 100% of proxied HTTP responses include a Spec-compliant W3C `traceparent` header that matches the `trace_id` and `span_id` emitted in structured logs and forwarded to upstream.
- **SC-003**: Zero raw credentials (`ghp_...` tokens or gateway auth keys) ever appear in `/_gateway/metrics` output or structured log records across all test scenarios.
- **SC-004**: Zero external Go module dependencies are added beyond the existing `gopkg.in/yaml.v3` exemption.

## Assumptions

- Prometheus scrapers support the standard Prometheus 0.0.4 text exposition format over HTTP GET without requiring OpenMetrics protobuf negotiation.
- OpenTelemetry trace correlation operates via standard W3C Trace Context HTTP headers (`traceparent` / `tracestate`) and structured JSON/text log attributes (`trace_id`, `span_id`, `parent_span_id`) without requiring an external OTLP gRPC collector sidecar by default.
