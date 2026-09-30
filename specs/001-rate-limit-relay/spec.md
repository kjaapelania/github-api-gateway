# Feature Specification: GitHub API Rate Limit Relay

**Feature Branch**: `001-rate-limit-relay`
**Created**: 2026-09-29
**Status**: Draft
**Input**: "GitHub API Rate Limit Relay with intelligent largest-remaining token pooling, unified 10s reset buffer, unauthenticated fallback, multiple relay keys for zero-downtime rotation, enterprise YAML granular access policy with default deny, Option 2 regex/glob separation, String-or-Array flexibility, strict method validation, considerate 3000 default token reserve, and zero-quota ETag caching"

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
  - `methods` is strictly mandatory in every rule.
  - The relay validates all methods on startup against `GET`, `HEAD`, `POST`, `PUT`, `DELETE`, `PATCH`, `OPTIONS`. Typos (e.g. `GTE`) cause immediate startup failure.
- **Flexible Scoping**:
  - `repo` only (with `methods`): Matches any path within that repository.
  - `path` only (with `methods`): Matches that path across all repositories.
  - `methods` only: Global method allowlist.
- **Hardcoded Denylist**: `/user`, `/user/**`, `/admin/**`, and `/authorizations/**` are permanently blocked regardless of rules.

**Why this priority**: Essential security barrier. Prevents unauthorized access to internal private code, blocks mutating methods (`POST`/`DELETE`), and prevents exfiltrating company secrets to external public repos.

---

### User Story 3 - Multiple Relay Authentication Keys & Zero-Downtime Rotation (Priority: P3)

The operator running the relay wants to protect their internal relay endpoint so only authorized team members or CI runners with an authorized key can query the relay. To support zero-downtime key rotation and multi-team isolation, the relay supports a list of valid keys (`RELAY_AUTH_KEYS="key-ci,key-team-a"` or `client_keys: [...]` in YAML).

**Why this priority**: Enables rotating compromised or expired secrets without breaking all running clients, and allows revoking individual team keys independently.

**Acceptance Scenarios**:
1. **Given** `RELAY_AUTH_KEYS="key-1,key-2"`, **When** a client provides either `key-1` or `key-2` via `Authorization: Bearer <KEY>` or `X-Relay-Key: <KEY>`, **Then** the request is accepted.
2. **Given** a client provides an unknown key or no key, **Then** the relay immediately returns `401 Unauthorized` with JSON error.
3. **Given** no relay keys are configured, **Then** relay protection is disabled (delegating access control strictly to the granular policy).

---

### User Story 4 - Largest Remaining Quota Selection & Considerate Budgeting (Priority: P4)

The operator provides multiple GitHub tokens (`GITHUB_TOKENS=t1,t2,t3`). Instead of blind round-robin, the relay dynamically selects the token with the **Largest Remaining Quota** above `TOKEN_MIN_REMAINING`. This automatically protects colleagues whose tokens have been used locally and balances consumption.

**Considerate Quota Reserve & Clock Drift Safety**:
- `TOKEN_MIN_REMAINING` defaults to **`3000`** (out of 5,000) to guarantee a generous reserve for the token owner's personal work.
- **Unified 10-Second Reset & Cooldown**: When a low-quota token's `X-RateLimit-Reset` timestamp passes, the relay waits a **10-second buffer** (`ResetTime + 10s`) before sending an inline probe request. If GitHub has not yet rolled over the quota window, the relay enforces a **10-second probe cooldown** (`NextProbeTime = Now + 10s`), preventing tight loops or quota hammering while self-healing within seconds.

**Acceptance Scenarios**:
1. **Given** T1 has remaining = 4,500 and T2 has remaining = 3,200, **When** a request arrives, **Then** the relay selects T1 because it has the largest remaining quota.
2. **Given** T1's remaining quota drops below 3,000, **When** subsequent requests arrive, **Then** the relay sidelines T1 and routes to other eligible tokens.
3. **Given** T1 reaches its reset time, **When** `Now > ResetTime + 10s`, **Then** the relay probes T1 with a client request. If GitHub rolled over, T1 returns to 4,999; if delayed, T1 is throttled from further probes for 10 seconds.

---

### User Story 5 - Graceful Unauthenticated Fallback (Priority: P5)

When all configured tokens in the pool are exhausted (or if the relay is run without any tokens configured), the relay can fall back to unauthenticated requests if `FALLBACK_UNAUTHENTICATED=true`.

**Acceptance Scenarios**:
1. **Given** all tokens have remaining <= `TOKEN_MIN_REMAINING` and `FALLBACK_UNAUTHENTICATED=true`, **When** a request arrives, **Then** the relay strips the `Authorization` header and proxies unauthenticated.
2. **Given** `FALLBACK_UNAUTHENTICATED=false` and all tokens are exhausted, **Then** the relay returns `429 Too Many Requests`.

---

### User Story 6 - Zero-Quota ETag In-Memory Caching (Priority: P6)

Clients frequently query the same API endpoints (releases, commits, tags). The relay caches responses in memory and sends `If-None-Match: <ETag>` when revalidating. When GitHub returns `304 Not Modified`, GitHub charges **zero rate limit**, and the relay serves the response from memory. Furthermore, when clients download binary release assets, GitHub's API natively responds with a `302 Found` redirect to its AWS S3 CDN (`objects.githubusercontent.com`), which the relay transparently forwards, offloading large binary payload bandwidth completely.

---

### User Story 7 - Health & Rate Limit Telemetry (Priority: P7)

Operators need to monitor the health of the relay and see the live status of each token, cache stats, and policy matches.

**Acceptance Scenarios**:
1. Call `GET /_gateway/status` (or `GET /_relay/status`) and assert JSON contains uptime, active token pool status, cache count, and policy status.

---

### User Story 8 - Zero-Restart Hot-Reloading (Priority: P8)

Operators need to dynamically update the YAML access policy, rotate gateway client keys, update token pools, or adjust rate limit configs without restarting the gateway process or dropping active HTTP connections.

**Acceptance Scenarios**:
1. **Given** a running gateway, **When** an operator issues `POST /_gateway/reload` (or sends `SIGHUP`), **Then** the gateway reloads `.env`, `policy.yaml`, and internal state in-memory, returning `200 OK` with updated stats.
2. **Given** an invalid YAML syntax or corrupt config during reload, **When** `POST /_gateway/reload` is called, **Then** the gateway atomically rolls back, returns `400 Bad Request`, and preserves the previous valid state.

---

### User Story 9 - Per-Client Token-Bucket Rate Limiter (Priority: P9)

A single rogue client or flooded CI loop must not issue excessive traffic to consume all available GitHub token quota from the shared pool ("noisy neighbor" protection).

**Acceptance Scenarios**:
1. **Given** `GITHUB_API_GATEWAY_CLIENT_MAX_RPM=60` and `GITHUB_API_GATEWAY_CLIENT_BURST=10`, **When** an authorized client exceeds their rate limit, **Then** the gateway responds with `429 Too Many Requests`, sets a `Retry-After: <seconds>` header, and returns a JSON diagnostic error without forwarding requests upstream.
2. **Given** multiple clients with distinct keys (`X-Gateway-Key`, `X-Relay-Key`, or `Authorization: Bearer`), **When** client A is throttled, **Then** client B continues to execute normally with an independent token bucket.
3. **Given** unauthenticated open mode (no keys configured), **When** requests arrive, **Then** the limiter buckets requests by client IP.

---

## Requirements *(mandatory)*

- **FR-001**: Gateway MUST proxy HTTP requests targeting GitHub API to `https://api.github.com`.
- **FR-002**: Gateway MUST support any valid GitHub token format (Classic `ghp_`, Fine-Grained `github_pat_`, OAuth `gho_`, GitHub App `ghs_`).
- **FR-003**: Gateway MUST support multiple gateway authentication keys (`GITHUB_API_GATEWAY_AUTH_KEYS`) for zero-downtime rotation.
- **FR-004**: Gateway MUST select tokens using a Largest Remaining Quota First strategy, automatically skipping tokens whose remaining quota is below `GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING` (default: 3000).
- **FR-005**: Gateway MUST enforce a unified 10-second post-reset buffer and 10-second probe cooldown for reset detection without background threads.
- **FR-006**: Gateway MUST enforce a Fail-Closed Default Deny access policy defined in a YAML configuration file.
- **FR-007**: Policy attributes `repo`, `repo_regex`, `path`, and `path_regex` MUST accept either a single string or an array of strings.
- **FR-008**: Gateway policy rules MUST support either `repo` (glob) OR `repo_regex` (Go regex), but NEVER both in the same rule.
- **FR-009**: Gateway policy rules MUST support either `path` (glob) OR `path_regex` (Go regex), but NEVER both in the same rule.
- **FR-010**: Gateway policy rules MUST strictly require `methods`. Unlisted or misspelled methods MUST cause startup validation failure.
- **FR-011**: Gateway MUST hard-block requests to sensitive user and account endpoints (`/user`, `/user/**`, `/admin/**`, `/authorizations/**`).
- **FR-012**: If `GITHUB_API_GATEWAY_FALLBACK_UNAUTHENTICATED=true`, gateway MUST fall back to unauthenticated requests when all tokens are exhausted or no tokens are provided.
- **FR-013**: Gateway MUST cache `GET` responses with `ETag` and send `If-None-Match` upstream to leverage zero-quota `304 Not Modified` responses.
- **FR-014**: Gateway MUST expose `GET /_gateway/status` with per-token quota telemetry and health.
- **FR-015**: Gateway MUST support zero-restart hot-reloading via `POST /_gateway/reload` and `SIGHUP` with atomic rollback on invalid configuration or syntax errors.
- **FR-016**: Gateway MUST enforce per-client token-bucket rate limiting (`GITHUB_API_GATEWAY_CLIENT_MAX_RPM`, default: 60 req/min, burst: 10) to protect the shared token pool from noisy neighbors.
- **FR-017**: Throttled clients MUST receive `429 Too Many Requests` with a `Retry-After: <seconds>` header and a structured JSON diagnostic body.
- **FR-018**: All environment variables MUST use the canonical prefix `GITHUB_API_GATEWAY_*`, and runtime parameters MUST be overridable via CLI flags.

