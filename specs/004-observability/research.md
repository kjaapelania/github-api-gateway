# Phase 0 Research: Prometheus & OpenTelemetry W3C Trace Context Observability

## Decision 1: Pure Standard Library Prometheus Text Exposition 0.0.4 vs `prometheus/client_golang`

- **Decision**: Implement Prometheus Text Exposition Format (`text/plain; version=0.0.4; charset=utf-8`) directly in `internal/telemetry/metrics.go` using `sync/atomic`, `sync.RWMutex`, and `fmt.Fprintf`.
- **Rationale**:
  1. **Constitution Principle I Compliance**: The project constitution strictly prohibits third-party Go dependencies outside `gopkg.in/yaml.v3`.
  2. **Simplicity & Zero Binary Bloat**: The Prometheus 0.0.4 text format is a straightforward line-oriented specification (`# HELP`, `# TYPE`, `metric_name{label="val"} value`). Implementing counters, gauges, and fixed-bucket cumulative histograms in ~200 lines of Go standard library code avoids pulling in dozens of transitive protobuf/metrics dependencies.
- **Alternatives Considered**:
  - `github.com/prometheus/client_golang`: Rejected due to Constitution Principle I violation and unnecessary transitive dependency footprint.

## Decision 2: W3C Trace Context (`traceparent` / `tracestate`) + `log/slog` vs OpenTelemetry Go SDK

- **Decision**: Implement W3C Trace Context Level 1 (`traceparent` version `00` and `tracestate` passthrough) in `internal/telemetry/trace.go` using `crypto/rand` and `encoding/hex`, and correlate structured request logs via `log/slog` attributes (`trace_id`, `span_id`, `parent_span_id`).
- **Rationale**:
  1. **Vendor-Neutral Interoperability**: W3C Trace Context (`traceparent: 00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>`) is the universal OpenTelemetry wire protocol. Parsing incoming `traceparent`, generating a child `span_id`, propagating `traceparent`/`tracestate` to upstream GitHub requests, echoing `traceparent` on downstream responses, and logging `trace_id`/`span_id` in `slog` provides full trace continuity for OpenTelemetry collectors and log aggregators (Grafana Loki/Tempo, Datadog, Honeycomb, Jaeger) with zero SDK overhead.
  2. **Resilience to Malformed Headers**: Per W3C Trace Context specification, if `traceparent` is missing, malformed, uses uppercase hex, or has an all-zero `trace-id` or `parent-id`, the gateway generates a new root `trace_id` and `span_id` (`flags = 01`).
- **Alternatives Considered**:
  - `go.opentelemetry.io/otel`: Rejected due to Constitution Principle I (Go Standard Library First).

## Decision 3: Metric Cardinality Protection

- **Decision**: Bound `github_api_gateway_requests_total` labels strictly to `method` (e.g. `GET`, `POST`, `HEAD`) and `status` (e.g. `200`, `401`, `403`, `429`), and bound per-token gauges strictly to `token_id` (masked `ghp_...XXXX`).
- **Rationale**: Including raw request paths or client IP addresses in Prometheus labels would enable a malicious or noisy client to trigger a cardinality explosion in memory.
