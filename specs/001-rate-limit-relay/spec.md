# Feature Specification: GitHub API Rate Limit Relay

**Feature Branch**: `001-rate-limit-relay`
**Created**: 2026-09-29
**Status**: Draft
**Input**: "GitHub API Rate Limit Relay with token pool budgeting, unauthenticated fallback, enterprise YAML granular access policy with default deny, Option 2 regex/glob separation, String-or-Array flexibility, strict method validation, zero-quota caching, and latest release redirect optimization"

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

### User Story 2 - Enterprise Granular Access Policy & Default Deny (Priority: P2)

In an enterprise team environment, the relay must not act as an open proxy or an accidental data exfiltration channel. The operator configures a YAML policy (`policy.yaml`). The relay enforces **fail-closed Default Deny**: every request is blocked with `403 Forbidden` unless an explicit rule matches the repository, path, and HTTP method.

**Policy Specification**:
- **String-or-Array Flexibility**: `repo`, `repo_regex`, `path`, and `path_regex` can each be defined either as a single string or a YAML list of strings to avoid rule duplication.
- **Option 2 (Explicit Separation)**:
  - `repo` (glob, e.g. `my-company/*`) OR `repo_regex` (Go regex, e.g. `^my-company/(api|web)$`), mutually exclusive.
  - `path` (glob, e.g. `/repos/my-company/*/releases/**`) OR `path_regex` (Go regex), mutually exclusive.
- **Mandatory `methods` & Startup Validation**:
  - `methods` is mandatory in every rule.
  - The relay validates all methods on startup against `GET`, `HEAD`, `POST`, `PUT`, `DELETE`, `PATCH`, `OPTIONS`. Typos (e.g. `GTE`) cause immediate startup failure.
- **Flexible Scoping**:
  - `repo` only: Matches any path within that repository.
  - `path` only: Matches that path across all repositories.
  - `methods` only: Global method allowlist.
- **Hardcoded Denylist**: `/user`, `/user/**`, `/admin/**`, and `/authorizations/**` are permanently blocked regardless of rules.

**Why this priority**: Essential security barrier. Prevents unauthorized access to internal private code, blocks mutating methods (`POST`/`DELETE`), and prevents exfiltrating company secrets to external public repos.

**Independent Test**: 
1. Send a request that matches no rule $\rightarrow$ assert `403 Forbidden` (Default Deny).
2. Send a `POST` request to an allowed `GET`-only path $\rightarrow$ assert `403 Forbidden`.
3. Send a request to `/user` $\rightarrow$ assert `403 Forbidden`.
4. Send a request matching an allowed rule $\rightarrow$ assert `200 OK` forwarded upstream.
5. Provide a policy with typo `methods: ["GTE"]` $\rightarrow$ assert startup fails with clear validation error.

---

### User Story 3 - Optional Relay Authentication Key (Priority: P3)

The operator running the relay wants to protect their internal relay endpoint so only authorized team members or CI runners with an internal secret can query the relay.

**Acceptance Scenarios**:
1. **Given** `RELAY_AUTH_KEY=secret123`, **When** a client sends a request without `Authorization: Bearer secret123` or `X-Relay-Key: secret123`, **Then** the relay immediately returns `401 Unauthorized`.
2. **Given** `RELAY_AUTH_KEY` is empty/unset, **Then** relay protection is disabled (delegating access control strictly to the granular policy).

---

### User Story 4 - Token Pool Management & Quota Threshold Budgeting (Priority: P4)

The operator provides one or more GitHub tokens (`GITHUB_TOKENS=t1,t2`). The relay dynamically tracks remaining quota from GitHub's authoritative `X-RateLimit-Remaining` and `X-RateLimit-Reset` headers. When a token's remaining quota drops below `TOKEN_MIN_REMAINING` (default 10) or hits 0, the relay automatically rotates to the next available token until the exhausted token's reset window arrives.

**Acceptance Scenarios**:
1. **Given** multiple tokens configured, **When** requests arrive, **Then** the relay selects tokens with remaining quota above `TOKEN_MIN_REMAINING`.
2. **Given** the relay server restarts, **When** it initializes, **Then** it is stateless: it syncs token quota status directly from GitHub's zero-quota `GET /rate_limit` endpoint or first response headers.

---

### User Story 5 - Graceful Unauthenticated Fallback (Priority: P5)

When all configured tokens in the pool are exhausted (or if the relay is run without any tokens configured), the relay can fall back to unauthenticated requests if `FALLBACK_UNAUTHENTICATED=true`.

**Acceptance Scenarios**:
1. **Given** all tokens have remaining = 0 and `FALLBACK_UNAUTHENTICATED=true`, **When** a request arrives, **Then** the relay strips the `Authorization` header and proxies unauthenticated.
2. **Given** `FALLBACK_UNAUTHENTICATED=false` and all tokens are exhausted, **Then** the relay returns `429 Too Many Requests`.

---

### User Story 6 - Latest Release Redirect Optimization (Priority: P6)

GitHub's web frontend (`https://github.com/{owner}/{repo}/releases/latest` and `.../releases/latest/download/{asset}`) returns an HTTP `302 Found` redirect without consuming REST API quota and offloads large binary downloads to GitHub's CDN.

**Acceptance Scenarios**:
1. **Given** a client requests a latest release asset download at `GET /repos/{owner}/{repo}/releases/latest/download/{asset}`, **Then** the relay responds with an HTTP `302 Found` directing client to `https://github.com/{owner}/{repo}/releases/latest/download/{asset}`.

---

### User Story 7 - Zero-Quota ETag In-Memory Caching (Priority: P7)

Clients frequently query the same API endpoints. The relay caches responses and sends `If-None-Match: <ETag>` when revalidating. When GitHub returns `304 Not Modified`, GitHub charges **zero rate limit**, and the relay serves the response from memory.

---

### User Story 8 - Health & Rate Limit Telemetry (Priority: P8)

Operators need to monitor the health of the relay and see the live status of each token and policy rule matches.

**Acceptance Scenarios**:
1. Call `GET /_relay/status` and assert JSON contains uptime, active token pool status, cache count, and policy status.

---

## Requirements *(mandatory)*

- **FR-001**: Relay MUST proxy HTTP requests targeting GitHub API to `https://api.github.com`.
- **FR-002**: Relay MUST support any valid GitHub token format (Classic `ghp_`, Fine-Grained `github_pat_`, OAuth `gho_`, GitHub App `ghs_`).
- **FR-003**: Relay MUST enforce a Fail-Closed Default Deny access policy defined in a YAML configuration file.
- **FR-004**: Policy attributes `repo`, `repo_regex`, `path`, and `path_regex` MUST accept either a single string or an array of strings.
- **FR-005**: Relay policy rules MUST support either `repo` (glob) OR `repo_regex` (Go regex), but NEVER both in the same rule.
- **FR-006**: Relay policy rules MUST support either `path` (glob) OR `path_regex` (Go regex), but NEVER both in the same rule.
- **FR-007**: Relay policy rules MUST require `methods`. An unlisted or misspelled method MUST cause startup validation failure.
- **FR-008**: Relay MUST hard-block requests to sensitive user and account endpoints (`/user`, `/user/**`, `/admin/**`, `/authorizations/**`).
- **FR-009**: Relay MUST rotate tokens when remaining quota drops below `TOKEN_MIN_REMAINING`.
- **FR-010**: If `FALLBACK_UNAUTHENTICATED=true`, relay MUST fall back to unauthenticated requests when all tokens are exhausted or no tokens are provided.
- **FR-011**: Relay MUST issue `302 Found` redirects for release asset download requests to GitHub's direct web download URLs.
- **FR-012**: Relay MUST cache `GET` responses with `ETag` and send `If-None-Match` upstream to leverage zero-quota `304 Not Modified` responses.
- **FR-013**: Relay MUST expose `GET /_relay/status` with per-token quota telemetry and health.
