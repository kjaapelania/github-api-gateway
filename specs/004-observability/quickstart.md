# Quickstart: Verifying Prometheus Metrics & OpenTelemetry W3C Trace Context

## 1. Run Quality Gate & Unit Tests

```bash
mise run check-all
```

## 2. Scrape Prometheus Metrics Locally

Start the gateway (`mise run bin:run` or `mise run compose:up`) and query `/_gateway/metrics`:

```bash
mise run bin:metrics
# or:
curl -s http://localhost:28280/_gateway/metrics
```

Expected output includes standard Prometheus 0.0.4 headers and metrics:
```text
# HELP github_api_gateway_uptime_seconds Gateway uptime in seconds.
# TYPE github_api_gateway_uptime_seconds gauge
github_api_gateway_uptime_seconds 42.150
# HELP github_api_gateway_requests_total Total HTTP requests processed by the gateway.
# TYPE github_api_gateway_requests_total counter
github_api_gateway_requests_total{method="GET",status="200"} 5
...
```

## 3. Verify W3C Trace Context Propagation

Send a request with a W3C `traceparent` header:

```bash
curl -i \
  -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" \
  http://localhost:28280/repos/golang/go/releases/latest
```

- The HTTP response includes `Traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-<16-hex-child-span-id>-01`.
- The gateway structured log outputs `trace_id=4bf92f3577b34da6a3ce929d0e0e4736`, `parent_span_id=00f067aa0ba902b7`, and the generated `span_id`.
