# Feature Specification: GitHub API Rate Limit Relay

**Feature Branch**: `001-rate-limit-relay`
**Created**: 2026-09-29
**Status**: Draft
**Input**: "GitHub API Rate Limit Relay with token injection, optional relay auth protection, zero-quota caching, and latest release redirect optimization"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Transparent Token Injection Reverse Proxy (Priority: P1)

An unauthenticated developer or CI worker sends standard GitHub REST API requests to the relay instead of `api.github.com`. The relay transparently forwards the request upstream, injecting a configured GitHub Personal Access Token (PAT) so that requests operate under the authenticated 5,000 req/hr quota instead of the unauthenticated 60 req/hr limit.

**Why this priority**: Core MVP functionality. Delivers immediate value by bypassing unauthenticated IP rate limits.

**Independent Test**: Can be tested by starting the relay with a mock upstream GitHub server, sending `GET /repos/golang/go`, and asserting that the upstream received `Authorization: Bearer <TOKEN>` and that the client received the upstream body and rate-limit headers.

**Acceptance Scenarios**:
1. **Given** the relay is running with `GITHUB_TOKEN=ghp_secret123`, **When** a client sends `GET /repos/octocat/Hello-World`, **Then** the relay proxies the request to `https://api.github.com/repos/octocat/Hello-World` with header `Authorization: Bearer ghp_secret123`.
2. **Given** upstream GitHub responds with status `200 OK` and headers `X-RateLimit-Limit: 5000`, `X-RateLimit-Remaining: 4999`, **Then** the relay forwards the exact status and headers back to the client.
3. **Given** upstream GitHub is unreachable or times out, **Then** the relay returns `502 Bad Gateway` with a structured JSON error body.

---

### User Story 2 - Optional Relay Authentication Protection (Priority: P2)

The operator running the relay wants to protect their 5,000 req/hr quota from being abused by unauthorized network clients. When configured, incoming requests must supply a valid relay key.

**Why this priority**: Critical operational security to prevent quota theft or denial-of-service when deployed on a shared network or server.

**Independent Test**: Send requests with no key, invalid key, and valid key, verifying `401 Unauthorized` vs `200 OK`.

**Acceptance Scenarios**:
1. **Given** `RELAY_AUTH_KEY=my-internal-secret` is set in the relay environment, **When** a client sends a request without `Authorization: Bearer my-internal-secret` or `X-Relay-Key: my-internal-secret`, **Then** the relay immediately returns `401 Unauthorized` without calling upstream GitHub.
2. **Given** `RELAY_AUTH_KEY=my-internal-secret`, **When** a client provides a valid key via `X-Relay-Key` or `Authorization`, **Then** the request is authorized and forwarded upstream (with the client's auth header replaced by `GITHUB_TOKEN`).
3. **Given** `RELAY_AUTH_KEY` is empty or unset, **Then** the relay operates in open/public mode and accepts all incoming requests without authentication.

---

### User Story 3 - Latest Release Redirect Optimization (Priority: P3)

GitHub's web frontend (`https://github.com/{owner}/{repo}/releases/latest`) performs an HTTP 302 redirect containing the latest release tag without counting against GitHub REST API rate limits. Furthermore, binary downloads (`.../releases/latest/download/{asset}`) redirect directly to GitHub's release CDN. The relay leverages this to offload bandwidth and save API quota.

**Why this priority**: Avoids burning API quota and proxying multi-megabyte binary assets through the relay.

**Independent Test**: Send `GET /repos/{owner}/{repo}/releases/latest/download/{asset}` and verify the relay returns a `302 Found` with `Location: https://github.com/{owner}/{repo}/releases/latest/download/{asset}` directly without proxying raw file bytes.

**Acceptance Scenarios**:
1. **Given** a client requests a latest release asset download at `GET /repos/{owner}/{repo}/releases/latest/download/{asset}`, **When** processed by the relay, **Then** the relay responds with an HTTP `302 Found` directing the client to `https://github.com/{owner}/{repo}/releases/latest/download/{asset}`, saving proxy bandwidth and 0 API rate limit.
2. **Given** a client queries latest release tag information, **When** the relay resolves latest release without hitting the REST API, **Then** it can inspect the `Location` header from `HEAD https://github.com/{owner}/{repo}/releases/latest` to resolve the tag version with zero REST API quota usage.

---

### User Story 4 - Zero-Quota ETag Caching (Priority: P4)

Clients frequently query the same API endpoints (e.g. repo details, branches, tags). The relay caches responses and uses HTTP conditional headers (`If-None-Match: <ETag>`) when revalidating. When GitHub returns `304 Not Modified`, GitHub charges **zero rate limit**, and the relay serves the response from memory.

**Why this priority**: Maximizes rate limit efficiency and minimizes latency for repeated calls.

**Independent Test**: Make two identical `GET` requests against a mock upstream that returns an ETag. Verify the second request sends `If-None-Match` and that receiving a `304` causes the relay to return the cached `200` body to the client with updated rate limit headers.

**Acceptance Scenarios**:
1. **Given** an endpoint was previously cached with an `ETag`, **When** another request arrives after TTL expiry, **Then** the relay issues an upstream conditional request with `If-None-Match`.
2. **Given** upstream responds with `304 Not Modified`, **Then** the relay serves the cached body with status `200 OK` to the client.

---

### User Story 5 - Health & Rate Limit Telemetry (Priority: P5)

Operators and monitoring tools need to check if the relay is healthy and see how much upstream GitHub quota remains.

**Why this priority**: Essential for observability.

**Independent Test**: Call `GET /_relay/status` and assert that JSON output contains uptime, cache size, and last observed `X-RateLimit-*` numbers.

**Acceptance Scenarios**:
1. **Given** the relay has handled requests, **When** a client requests `GET /_relay/status`, **Then** the relay returns `200 OK` with JSON containing `status: "ok"`, `rate_limit_limit`, `rate_limit_remaining`, and `rate_limit_reset`.

---

## Edge Cases

- **Upstream Rate Limit Exceeded (429 / 403)**: When GitHub returns `403 Forbidden` or `429 Too Many Requests` due to quota exhaustion, the relay must propagate the `Retry-After` or `X-RateLimit-Reset` header and return a clear JSON payload indicating upstream exhaustion.
- **Client Disconnect**: If the client aborts the connection mid-flight, context cancellation must cancel the upstream request without crashing the server.
- **Header Sanitization**: Relay must never forward the internal `GITHUB_TOKEN` downstream to the client or log it in stdout.
- **Query Parameter Preservation**: Relay must preserve all URL query parameters (`?per_page=100`, `?page=2`, etc.) when forwarding.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Relay MUST proxy HTTP requests targeting `/repos/...`, `/orgs/...`, `/users/...`, etc. to `https://api.github.com`.
- **FR-002**: Relay MUST inject `Authorization: Bearer <GITHUB_TOKEN>` into all upstream requests.
- **FR-003**: Relay MUST strip or overwrite any client-supplied `Authorization` header destined for GitHub.
- **FR-004**: If `RELAY_AUTH_KEY` is configured, relay MUST enforce authentication via `Authorization: Bearer <KEY>` or `X-Relay-Key: <KEY>`. Unauthenticated requests MUST receive `401 Unauthorized`.
- **FR-005**: If `RELAY_AUTH_KEY` is not set, relay MUST allow public access without authentication.
- **FR-006**: Relay MUST provide HTTP `302 Found` redirects for release asset download paths targeting `https://github.com/{owner}/{repo}/releases/latest/download/{asset}`.
- **FR-007**: Relay MUST maintain an in-memory cache for `GET` responses containing `ETag` headers and send `If-None-Match` upstream on cache revalidation.
- **FR-008**: Relay MUST serve `304 Not Modified` upstream responses as `200 OK` from cache.
- **FR-009**: Relay MUST expose `GET /_relay/status` with current rate-limit telemetry and health.
- **FR-010**: Relay MUST be implemented strictly using the Go standard library (`net/http`, `net/http/httputil`, `sync`, etc.) with zero external package dependencies.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of forwarded requests successfully include the configured `GITHUB_TOKEN` without exposing it to the client.
- **SC-002**: Cached revalidations that result in `304 Not Modified` consume 0 GitHub rate limit quota.
- **SC-003**: Asset downloads via `/releases/latest/download/...` return a `302 Found` redirect in under 5ms without proxying binary payloads through the relay.
- **SC-004**: Strict TDD compliance: All unit tests run via `go test -v ./...` with zero external dependencies and pass 100%.
- **SC-005**: Zero memory leaks during concurrent request benchmarking.
