# Feature Specification: GitHub API Rate Limit Relay

**Feature Branch**: `001-rate-limit-relay`
**Created**: 2026-09-29
**Status**: Draft
**Input**: "GitHub API Rate Limit Relay with token pool budgeting, unauthenticated fallback, optional relay auth protection, zero-quota caching, and latest release redirect optimization"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Transparent Token Injection Reverse Proxy (Priority: P1)

An unauthenticated developer or CI worker sends standard GitHub REST API requests to the relay instead of `api.github.com`. The relay transparently forwards the request upstream, injecting a configured GitHub token (Classic `ghp_`, Fine-Grained `github_pat_`, OAuth `gho_`, or App `ghs_`) so that requests operate under the authenticated 5,000 req/hr quota instead of the unauthenticated 60 req/hr limit.

**Why this priority**: Core MVP functionality. Delivers immediate value by bypassing unauthenticated IP rate limits.

**Independent Test**: Start relay with a mock upstream GitHub server, send `GET /repos/golang/go`, and assert upstream received `Authorization: Bearer <TOKEN>` and client received upstream body and rate-limit headers.

**Acceptance Scenarios**:
1. **Given** the relay is running with a configured token, **When** a client sends `GET /repos/octocat/Hello-World`, **Then** the relay proxies the request to `https://api.github.com/repos/octocat/Hello-World` with header `Authorization: Bearer <TOKEN>`.
2. **Given** upstream GitHub responds with status `200 OK` and headers `X-RateLimit-Limit: 5000`, `X-RateLimit-Remaining: 4999`, **Then** the relay forwards the exact status and headers back to the client.
3. **Given** upstream GitHub is unreachable or times out, **Then** the relay returns `502 Bad Gateway` with a structured JSON error body.

---

### User Story 2 - Optional Relay Authentication Protection (Priority: P2)

The operator running the relay wants to protect their 5,000+ req/hr quota from being abused by unauthorized network clients. When configured, incoming requests must supply a valid relay key.

**Why this priority**: Critical operational security to prevent quota theft or denial-of-service when deployed on a shared network or server.

**Independent Test**: Send requests with no key, invalid key, and valid key, verifying `401 Unauthorized` vs `200 OK`.

**Acceptance Scenarios**:
1. **Given** `RELAY_AUTH_KEY=my-internal-secret` is set in the relay environment, **When** a client sends a request without `Authorization: Bearer my-internal-secret` or `X-Relay-Key: my-internal-secret`, **Then** the relay immediately returns `401 Unauthorized` without calling upstream GitHub.
2. **Given** `RELAY_AUTH_KEY=my-internal-secret`, **When** a client provides a valid key via `X-Relay-Key` or `Authorization`, **Then** the request is authorized and forwarded upstream (with the client's auth header replaced by the upstream GitHub token).
3. **Given** `RELAY_AUTH_KEY` is empty or unset, **Then** the relay operates in open/public mode and accepts all incoming requests without authentication.

---

### User Story 3 - Token Pool Management & Quota Threshold Budgeting (Priority: P3)

The operator provides one or more GitHub tokens (`GITHUB_TOKENS=token1,token2`). The relay dynamically tracks remaining quota from GitHub's authoritative `X-RateLimit-Remaining` and `X-RateLimit-Reset` headers. When a token's remaining quota drops below a configurable threshold (`TOKEN_MIN_REMAINING`, default 10) or hits 0, the relay automatically rotates to the next available token until the exhausted token's reset window arrives.

**Why this priority**: Aggregates quota across accounts and prevents sudden 403 quota lockout.

**Independent Test**: Mock two tokens. Set threshold to 10. When Token 1 reports remaining = 9, the relay automatically switches subsequent requests to Token 2.

**Acceptance Scenarios**:
1. **Given** multiple tokens configured, **When** requests arrive, **Then** the relay selects tokens with remaining quota above `TOKEN_MIN_REMAINING`.
2. **Given** the relay server restarts, **When** it initializes, **Then** it is stateless: it syncs token quota status directly from GitHub's zero-quota `GET /rate_limit` endpoint or first response headers without requiring a database.

---

### User Story 4 - Graceful Unauthenticated Fallback (Priority: P4)

When all configured tokens in the pool are exhausted (or if the relay is run without any tokens configured at all), the relay can fall back to unauthenticated requests if `FALLBACK_UNAUTHENTICATED=true`.

**Why this priority**: Maximizes availability for public repository queries even when all token quotas are fully depleted.

**Independent Test**: Deplete all tokens in mock upstream. With `FALLBACK_UNAUTHENTICATED=true`, verify the relay strips the `Authorization` header and forwards the request unauthenticated, returning GitHub's unauthenticated response.

**Acceptance Scenarios**:
1. **Given** all tokens have remaining = 0 and `FALLBACK_UNAUTHENTICATED=true`, **When** a request arrives, **Then** the relay strips the `Authorization` header and proxies unauthenticated.
2. **Given** `FALLBACK_UNAUTHENTICATED=false` and all tokens are exhausted, **Then** the relay returns `429 Too Many Requests` with a descriptive JSON error and `Retry-After` header.
3. **Given** no tokens are configured (`GITHUB_TOKENS=""`), **Then** the relay runs purely in unauthenticated caching proxy mode.

---

### User Story 5 - Latest Release Redirect Optimization (Priority: P5)

GitHub's web frontend (`https://github.com/{owner}/{repo}/releases/latest` and `.../releases/latest/download/{asset}`) returns an HTTP `302 Found` redirect without consuming REST API quota and offloads large binary downloads to GitHub's CDN.

**Why this priority**: Avoids burning API quota and proxying multi-megabyte binary assets through the relay.

**Independent Test**: Send `GET /repos/{owner}/{repo}/releases/latest/download/{asset}` and verify the relay returns `302 Found` directing client to GitHub's web download URL directly.

**Acceptance Scenarios**:
1. **Given** a client requests a latest release asset download at `GET /repos/{owner}/{repo}/releases/latest/download/{asset}`, **Then** the relay responds with an HTTP `302 Found` to `https://github.com/{owner}/{repo}/releases/latest/download/{asset}`.

---

### User Story 6 - Zero-Quota ETag In-Memory Caching (Priority: P6)

Clients frequently query the same API endpoints. The relay caches responses and sends `If-None-Match: <ETag>` when revalidating. When GitHub returns `304 Not Modified`, GitHub charges **zero rate limit**, and the relay serves the response from memory.

**Why this priority**: Maximizes rate limit efficiency and minimizes latency for repeated calls.

**Independent Test**: Make two identical `GET` requests against a mock upstream that returns an ETag. Verify the second request sends `If-None-Match` and that receiving a `304` causes the relay to return the cached `200` body to the client.

---

### User Story 7 - Health & Rate Limit Telemetry (Priority: P7)

Operators need to monitor the health of the relay and see the live status of each token in the pool.

**Independent Test**: Call `GET /_relay/status` and assert that JSON output contains uptime, active token count, cache entries, and remaining quota per token.

---

## Requirements *(mandatory)*

- **FR-001**: Relay MUST proxy HTTP requests targeting GitHub API to `https://api.github.com`.
- **FR-002**: Relay MUST support any valid GitHub token format (Classic `ghp_`, Fine-Grained `github_pat_`, OAuth `gho_`, GitHub App `ghs_`).
- **FR-003**: Relay MUST support single or multiple tokens (`GITHUB_TOKENS=t1,t2`) and track remaining quota via `X-RateLimit-Remaining` and `X-RateLimit-Reset`.
- **FR-004**: Relay MUST rotate tokens when remaining quota drops below `TOKEN_MIN_REMAINING`.
- **FR-005**: If `FALLBACK_UNAUTHENTICATED=true`, relay MUST fall back to unauthenticated requests when all tokens are exhausted or no tokens are provided.
- **FR-006**: If `RELAY_AUTH_KEY` is configured, relay MUST require valid bearer or `X-Relay-Key` authentication (returning `401 Unauthorized` if invalid).
- **FR-007**: Relay MUST issue `302 Found` redirects for release asset download requests to GitHub's direct web download URLs.
- **FR-008**: Relay MUST cache `GET` responses with `ETag` and send `If-None-Match` upstream to leverage zero-quota `304 Not Modified` responses.
- **FR-009**: Relay MUST expose `GET /_relay/status` with per-token quota telemetry and health.
- **FR-010**: Relay MUST be implemented strictly using the Go standard library with zero external dependencies.
