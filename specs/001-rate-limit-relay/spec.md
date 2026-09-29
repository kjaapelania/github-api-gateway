# Feature Specification: GitHub API Rate Limit Relay

**Feature Branch**: `001-rate-limit-relay`
**Created**: 2026-09-29
**Status**: Draft
**Input**: "GitHub API Rate Limit Relay with intelligent round-robin token pooling, unauthenticated fallback, multiple relay keys for zero-downtime rotation, enterprise YAML granular access policy with default deny, Option 2 regex/glob separation, String-or-Array flexibility, strict method validation, considerate 3000 default token reserve, and zero-quota ETag caching"

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

### User Story 4 - Intelligent Round-Robin Token Pool & Considerate Quota Budgeting (Priority: P4)

The operator provides multiple GitHub tokens (`GITHUB_TOKENS=t1,t2,t3`). The relay alternates requests evenly across all tokens using an atomic **Round-Robin** strategy to distribute rate-limit consumption evenly across accounts. The relay dynamically tracks remaining quota from GitHub's authoritative `X-RateLimit-Remaining` and `X-RateLimit-Reset` headers. 

**Considerate Quota Reserve**: To protect colleagues whose personal PATs may be used to bootstrap the relay, `TOKEN_MIN_REMAINING` defaults to **`3000`** (out of 5,000). The relay borrows at most 2,000 calls per hour per token, leaving a generous 3,000-call reserve for the colleague's local Git CLI and IDE extensions. When a token's remaining quota drops below `TOKEN_MIN_REMAINING`, the round-robin selector **automatically skips that token** and continues balancing across the remaining healthy tokens until the reset window arrives. Dedicated bot tokens can override this buffer to a lower value (e.g. `50`).

**Why this priority**: Aggregates quota across accounts while guaranteeing that colleagues who contribute tokens never experience personal lockout.

**Acceptance Scenarios**:
1. **Given** two healthy tokens T1 and T2, **When** successive requests arrive, **Then** the relay alternates T1, T2, T1, T2 in round-robin sequence.
2. **Given** T1's remaining quota drops to 2,999 (below the 3,000 default threshold), **When** subsequent requests arrive, **Then** the relay routes 100% of requests to T2, skipping T1 until its `X-RateLimit-Reset` timestamp passes.
3. **Given** the relay server restarts, **When** it initializes, **Then** it is stateless: it syncs token quota status directly from GitHub's zero-quota `GET /rate_limit` endpoint or first response headers.

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
1. Call `GET /_relay/status` and assert JSON contains uptime, active token pool status, cache count, and policy status.

---

## Requirements *(mandatory)*

- **FR-001**: Relay MUST proxy HTTP requests targeting GitHub API to `https://api.github.com`.
- **FR-002**: Relay MUST support any valid GitHub token format (Classic `ghp_`, Fine-Grained `github_pat_`, OAuth `gho_`, GitHub App `ghs_`).
- **FR-003**: Relay MUST support multiple relay authentication keys (`RELAY_AUTH_KEYS` / `client_keys`) for zero-downtime rotation.
- **FR-004**: Relay MUST balance requests across configured tokens using an atomic Round-Robin selector, automatically skipping tokens whose remaining quota is below `TOKEN_MIN_REMAINING` (default: 3000).
- **FR-005**: Relay MUST enforce a Fail-Closed Default Deny access policy defined in a YAML configuration file.
- **FR-006**: Policy attributes `repo`, `repo_regex`, `path`, and `path_regex` MUST accept either a single string or an array of strings.
- **FR-007**: Relay policy rules MUST support either `repo` (glob) OR `repo_regex` (Go regex), but NEVER both in the same rule.
- **FR-008**: Relay policy rules MUST support either `path` (glob) OR `path_regex` (Go regex), but NEVER both in the same rule.
- **FR-009**: Relay policy rules MUST strictly require `methods`. Unlisted or misspelled methods MUST cause startup validation failure.
- **FR-010**: Relay MUST hard-block requests to sensitive user and account endpoints (`/user`, `/user/**`, `/admin/**`, `/authorizations/**`).
- **FR-011**: If `FALLBACK_UNAUTHENTICATED=true`, relay MUST fall back to unauthenticated requests when all tokens are exhausted or no tokens are provided.
- **FR-012**: Relay MUST cache `GET` responses with `ETag` and send `If-None-Match` upstream to leverage zero-quota `304 Not Modified` responses.
- **FR-013**: Relay MUST expose `GET /_relay/status` with per-token quota telemetry and health.
