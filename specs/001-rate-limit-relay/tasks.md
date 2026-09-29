# Tasks: GitHub API Rate Limit Relay

**Input**: Design documents from `specs/001-rate-limit-relay/`
**Prerequisites**: [plan.md](plan.md) (required), [spec.md](spec.md) (required), [constitution.md](../../.specify/memory/constitution.md)
**Constitution Compliance**: Strict TDD, Controlled dependencies (`gopkg.in/yaml.v3` for policy only), Fail-Closed Default Deny.

## Phase 1: Setup & Foundational Configuration

- [x] **T000**: Configure container and developer runtime harness (`mise.toml`, `Dockerfile`, `docker-compose.yml`, `.env.example`, starter `policy.yaml`, root `.gitignore`).
- [x] **T001**: Initialize Go module `github-api-relay` (`go mod init github-api-relay`) and add `gopkg.in/yaml.v3`.
- [x] **T002**: [TDD] Write unit tests in `internal/config/config_test.go` asserting defaults (`TOKEN_MIN_REMAINING=3000`, `PORT=8080`, `FALLBACK_UNAUTHENTICATED=true`, `CACHE_TTL_SECONDS=300`) and environment variable parsing (`PORT`, `GITHUB_TOKENS`, `RELAY_AUTH_KEYS`, `POLICY_PATH`, `TOKEN_MIN_REMAINING`, `FALLBACK_UNAUTHENTICATED`, `CACHE_TTL_SECONDS`).
- [x] **T003**: Implement `internal/config/config.go` to make `config_test.go` pass (Green).

---

## Phase 2: Enterprise Granular Access Policy (Fail-Closed Default Deny)

- [x] **T004 [Policy]**: [TDD] Write unit tests in `internal/policy/policy_test.go`:
  - Assert String-or-Array deserialization: `repo: "org/a"` and `repo: ["org/a", "org/b"]` both parse into slice.
  - Assert policy validation fails on startup if a rule specifies *both* `repo` and `repo_regex`, or *both* `path` and `path_regex` (Mutual Exclusivity).
  - Assert policy validation fails on startup if `methods` is missing or contains invalid methods like `GTE`.
  - Assert hardcoded denylist rejects `/user`, `/user/keys`, `/admin/*`, `/authorizations/*` regardless of policy rules.
  - Assert Default Deny: any request not explicitly matching an allowed rule returns `403 Forbidden`.
  - Assert glob matching works for `repo: "my-org/*"` and `path: "/repos/my-org/*/releases/**"`.
  - Assert regex matching works for `repo_regex: "^my-org/(api|worker)$"` and `path_regex: "^/repos/[^/]+/[^/]+/releases/.*$"`.
  - Assert scoping: rule with only `repo` matches any path; rule with only `path` matches any repo; rule with only `methods` matches globally.
  - Assert method filtering: requesting `POST` on a rule allowing only `["GET", "HEAD"]` returns `403 Forbidden`.
- [x] **T005 [Policy]**: Implement `internal/policy/model.go` (with `StringOrSlice` unmarshaler), `internal/policy/parser.go`, and `internal/policy/evaluator.go` to make `policy_test.go` pass (Green).

---

## Phase 3: Largest-Remaining Token Pool & 10s Cooldown Management

- [x] **T006 [TokenPool]**: [TDD] Write unit tests in `internal/token/pool_test.go`:
  - Token selection chooses the token with the Largest Remaining Quota among tokens with remaining >= minRemaining (default: 3000).
  - When all tokens are below minRemaining, selection waits until `Now > ResetTime + 10s`.
  - When probed after reset time and GitHub still reports `remaining < minRemaining` (delayed rollover), assert next probe is throttled for 10 seconds.
  - When all tokens are exhausted and past cooldown, `Acquire()` returns fallback to unauthenticated if `fallbackUnauthenticated=true`, or returns an error if `false`.
  - Works transparently with any token prefix (`ghp_`, `github_pat_`, `gho_`, `ghs_`).
  - Works with empty token list (unauthenticated-only mode).
- [x] **T007 [TokenPool]**: Implement `internal/token/pool.go` using `sync.RWMutex` to make `pool_test.go` pass (Green).

---

## Phase 4: Core Reverse Proxy with Policy Enforcement & Token Injection

- [x] **T008 [Proxy]**: [TDD] Write unit tests in `internal/proxy/handler_test.go` with an `httptest.Server`:
  - Requests blocked by `PolicyEvaluator` are rejected immediately with `403 Forbidden` without contacting upstream.
  - Allowed requests have `Authorization: Bearer <TOKEN>` injected from `TokenPool`.
  - Client-supplied `Authorization` headers are stripped/overwritten.
  - Unauthenticated fallback requests are forwarded without an `Authorization` header.
  - Upstream rate-limit headers (`X-RateLimit-Remaining`, `X-RateLimit-Reset`) are parsed and fed to `TokenPool.RecordUsage()`.
  - Upstream headers and status codes are propagated back to the client.
- [x] **T009 [Proxy]**: Implement `internal/proxy/handler.go` using `net/http/httputil.ReverseProxy` with custom `Director` and `ModifyResponse` to make `handler_test.go` pass (Green).

---

## Phase 5: Multiple Relay Authentication Keys (Zero-Downtime Rotation)

- [x] **T010 [Auth]**: [TDD] Write tests in `internal/auth/middleware_test.go`:
  - When `RELAY_AUTH_KEYS` contains multiple keys (`key1,key2`), requests with either `key1` or `key2` in `Authorization: Bearer <KEY>` or `X-Relay-Key: <KEY>` are accepted.
  - Unknown keys or missing keys receive `401 Unauthorized` with JSON error.
  - When `RELAY_AUTH_KEYS` is empty, all requests pass through (open mode).
- [x] **T011 [Auth]**: Implement `internal/auth/middleware.go` with set-based key lookup to make `middleware_test.go` pass (Green).

---

## Phase 6: Zero-Quota ETag In-Memory Caching & S3 Redirect Preservation

- [ ] **T012 [Cache]**: [TDD] Write tests in `internal/cache/memory_test.go`:
  - Cache response body and `ETag` on `200 OK` `GET` requests.
  - On subsequent request, verify `If-None-Match: <ETag>` is sent to upstream.
  - When upstream responds with `304 Not Modified`, verify relay serves cached body with status `200 OK` and updated rate limit headers (0 quota consumed).
  - Assert that upstream `302 Found` responses (e.g. GitHub release binary redirects to S3) are preserved and forwarded directly to the client without buffering binary bodies.
- [ ] **T013 [Cache]**: Implement `internal/cache/memory.go` using `sync.RWMutex` and an in-memory map to make `memory_test.go` pass (Green).

---

## Phase 7: Status & Telemetry Endpoint

- [ ] **T014 [Telemetry]**: [TDD] Write tests in `internal/telemetry/status_test.go` verifying `GET /_relay/status` returns JSON with uptime, token pool status (each token's masked ID, remaining quota, reset time), active relay key count, and cache count.
- [ ] **T015 [Telemetry]**: Implement `internal/telemetry/status.go` to make `status_test.go` pass (Green).

---

## Phase 8: Application Integration & End-to-End Verification

- [ ] **T016**: Implement `cmd/relay/main.go` wiring all middlewares (Auth $\rightarrow$ Policy $\rightarrow$ Cache $\rightarrow$ Proxy $\rightarrow$ Status) and setting up graceful shutdown.
- [ ] **T017**: [TDD] Write end-to-end integration test in `cmd/relay/main_test.go` running the complete relay against a mock GitHub API and sample `policy.yaml`, verifying all user stories end-to-end.
