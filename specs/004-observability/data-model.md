# Phase 1 Data Model: Prometheus Metrics & W3C Trace Context

## 1. `TraceContext` (`internal/telemetry/trace.go`)

Represents an OpenTelemetry-compatible W3C Trace Context attached to an HTTP request lifecycle.

| Field | Type | Validation / Constraints | Description |
| :--- | :--- | :--- | :--- |
| `Version` | `string` | Exactly 2 lowercase hex chars (`"00"`) | W3C Trace Context version |
| `TraceID` | `string` | 32 lowercase hex chars (`[0-9a-f]{32}`), MUST NOT be all zeros | Globally unique 16-byte trace identifier |
| `SpanID` | `string` | 16 lowercase hex chars (`[0-9a-f]{16}`), MUST NOT be all zeros | Gateway operation 8-byte span identifier |
| `ParentSpanID` | `string` | 16 lowercase hex chars (empty if root trace) | Incoming caller's `parent-id` from `traceparent` |
| `Flags` | `string` | 2 lowercase hex chars (`[0-9a-f]{2}`, default `"01"`) | W3C trace flags (bit 0 = sampled) |
| `TraceState` | `string` | Optional vendor key-value list from `tracestate` header | Forwarded untouched when incoming `traceparent` is valid |

### Formatting Invariant
- `TraceParent()` returns `fmt.Sprintf("%s-%s-%s-%s", tc.Version, tc.TraceID, tc.SpanID, tc.Flags)`.

---

## 2. `Registry` (`internal/telemetry/metrics.go`)

Thread-safe metrics registry exposing counters, histograms, and dynamic gauges.

### Exported Prometheus Metrics

| Metric Name | Type | Labels | Source / Description |
| :--- | :--- | :--- | :--- |
| `github_api_gateway_uptime_seconds` | `gauge` | *(none)* | Seconds since `startTime` |
| `github_api_gateway_requests_total` | `counter` | `method`, `status` | Total HTTP requests completed through the gateway |
| `github_api_gateway_request_duration_seconds` | `histogram` | `le` (`0.005`..`10`, `+Inf`) | Cumulative request latency distribution (`_bucket`, `_sum`, `_count`) |
| `github_api_gateway_auth_decisions_total` | `counter` | `decision` (`allow`, `reject`) | Pre-shared key authentication outcomes |
| `github_api_gateway_active_auth_keys` | `gauge` | *(none)* | Number of active pre-shared client auth keys |
| `github_api_gateway_ratelimit_decisions_total` | `counter` | `decision` (`allow`, `throttle`) | Per-client token-bucket rate limiter outcomes |
| `github_api_gateway_ratelimit_active_buckets` | `gauge` | *(none)* | Current active client buckets in `ratelimit.Limiter` |
| `github_api_gateway_cache_events_total` | `counter` | `result` (`revalidated`, `miss`, `bypass`) | ETag cache outcomes (`revalidated` = 304 quota saved) |
| `github_api_gateway_cache_entries` | `gauge` | *(none)* | Current active unexpired entries in `cache.Cache` |
| `github_api_gateway_policy_decisions_total` | `counter` | `decision` (`allow`, `deny`) | Enterprise policy evaluation outcomes |
| `github_api_gateway_policy_rules` | `gauge` | *(none)* | Current active rule count in `policy.Policy` |
| `github_api_gateway_tokens_total` | `gauge` | *(none)* | Total configured GitHub PATs in `token.Pool` |
| `github_api_gateway_token_remaining` | `gauge` | `token_id` (masked `ghp_...XXXX`) | Remaining GitHub API rate-limit quota for each token |
| `github_api_gateway_token_in_cooldown` | `gauge` | `token_id` (masked `ghp_...XXXX`) | `1` if token is in cooldown/sidelined, `0` if active |
