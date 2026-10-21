# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-10-22

### Added
- **Multi-flavor GitHub Container Registry (`ghcr.io`) publishing.** Automated tagging and publishing for all three container targets (`debian`, `alpine`, and zero-CVE `distroless`) via `mise run docker:push-test` in CI (`ghcr.io/kjaapelania/github-api-gateway-test`) and `mise run docker:push-release` on version tags (`ghcr.io/kjaapelania/github-api-gateway:<version>-<flavor>`, `:<flavor>-latest`, and `:latest`).
- **Turnkey local Prometheus & Grafana observability stack (`observability` Compose profile).** Added `mise run compose:obs-up` and `mise run compose:obs-down` launching Prometheus (`:28290`, scraping `gateway:28280/_gateway/metrics` every 5s) and Grafana (`:28300`) pre-provisioned with the **GitHub API Gateway Overview** dashboard (`deploy/observability/`).
- **Automated dependency and toolchain maintenance via Renovate (`.github/renovate.json`).** Configured Renovate across `gomod`, `mise`, `dockerfile`, `docker-compose`, and `github-actions` managers with grouped pull requests (`Go toolchain`, `Container images`, and `CI & developer tooling`).

## [0.1.0] - 2026-10-21

### Security
- **Fail-closed default-deny access policy (`policy.yaml`).** Blocks all requests with `403 Forbidden` unless explicitly matched by repository glob/regex, path glob/regex, and mandatory HTTP method rules. Permanently hard-blocks sensitive account endpoints (`/user`, `/user/**`, `/admin/**`, `/authorizations/**`, `/settings/**`).
- **Path traversal and URL-encoding canonicalization.** Rejects raw and URL-encoded `..` / `.` segments (`%2e%2e`, `%2f`) before policy evaluation so `**` wildcard rules cannot be bypassed to reach denylisted endpoints.
- **Upstream secret header stripping.** Strips internal gateway authentication headers (`X-Gateway-Key`, `X-Relay-Key`, and client `Authorization`) before forwarding requests to `api.github.com`.
- **Loopback-guarded and Bearer-authenticated admin reload.** Restricts `POST /_gateway/reload` to loopback addresses by default and enforces constant-time Bearer token verification (`GITHUB_API_GATEWAY_ADMIN_TOKEN`) with fail-closed startup validation when `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true`.

### Added
- **Largest-remaining GitHub token pool with considerate quota reserve.** Pools multiple GitHub tokens (`ghp_`, `github_pat_`, `gho_`, `ghs_`), dynamically selects the token with the largest remaining quota above `GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING` (default `3000`), enforces a unified 10-second reset buffer and probe cooldown with stampede protection, sidelines revoked `401` tokens automatically, and supports optional unauthenticated fallback.
- **Zero-quota ETag in-memory cache and direct S3 redirect passthrough.** Caches `200 OK` `GET` responses with bounded LRU/TTL eviction (`10,000` entry cap), sends `If-None-Match` on repeat queries to leverage zero-cost `304 Not Modified` responses, and passes `302 Found` binary asset redirects straight to GitHub's S3 CDN without buffering.
- **Universal external release version checks in default policy.** Includes default `external_rules` for `/repos/*/*/releases/latest`, `/repos/*/*/releases/tags/*`, and `/repos/*/*/releases/download/**` so CI/CD pipelines and developer tools can check latest GitHub-hosted tool versions without exhausting the 60 req/hr unauthenticated rate limit.
- **Per-client token-bucket rate limiting.** Protects the shared token pool from noisy neighbors (`GITHUB_API_GATEWAY_CLIENT_MAX_RPM=60`, `GITHUB_API_GATEWAY_CLIENT_BURST=10`) with independent buckets per gateway key (or `RemoteAddr` IP in open mode) and `429 Too Many Requests` + `Retry-After` diagnostics.
- **Declarative remote policy loading and background reconciliation.** Supports loading `policy.yaml` from remote `http://` / `https://` URLs with periodic ETag (`If-None-Match`) and SHA-256 reconciliation (`GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL`), zero-downtime `SIGHUP` / `POST /_gateway/reload` hot-swaps, and automatic rollback on corrupt YAML.
- **Prometheus 0.0.4 metrics endpoint (`GET /_gateway/metrics` & `GET /_relay/metrics`).** Exposes zero-dependency, thread-safe counters (`requests_total`, `auth_decisions_total`, `ratelimit_decisions_total`, `cache_events_total`, `policy_decisions_total`), request latency histograms (`request_duration_seconds`), and live subsystem gauges (`uptime_seconds`, `active_auth_keys`, `ratelimit_active_buckets`, `cache_entries`, `policy_rules`, `tokens_total`, and per-token `token_remaining` / `token_in_cooldown` labeled strictly by masked token ID `ghp_...XXXX`). Includes `mise run bin:metrics` helper task.
- **OpenTelemetry W3C Trace Context propagation (`traceparent` & `tracestate`) and correlated `slog` logging.** Parses incoming W3C `traceparent` headers (`00-<trace-id>-<parent-id>-<flags>`), spawns a child `span_id` for the gateway operation (or generates a fresh root trace context if absent/malformed), forwards updated `traceparent` and `tracestate` headers upstream, echoes `traceparent` on downstream responses, and correlates `http_request` logs with `trace_id`, `span_id`, and `parent_span_id`.
- **Multi-key client authentication and live JSON status.** Supports zero-downtime client key rotation (`GITHUB_API_GATEWAY_AUTH_KEYS`), live health and masked token quota telemetry at `GET /_gateway/status`, and `-version` / `-healthcheck` CLI flags.
- **Hexagonal `mise` task runner, CI/Release pipelines, multi-target containers, and MIT License.** Provides modular `.mise/tasks/` ports (`check-all`, `build-all` + `build:checksums` for 5 OS/arch targets, `docker:build`, `compose:*`), idempotent GitHub Actions CI & Release workflows, a multi-stage `Dockerfile` producing `debian`, `alpine`, and zero-CVE `distroless` images, and the open-source MIT `LICENSE`.

[0.2.0]: https://github.com/kjaapelania/github-api-gateway/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kjaapelania/github-api-gateway/releases/tag/v0.1.0
