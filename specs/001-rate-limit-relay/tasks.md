# Tasks: GitHub API Rate Limit Relay

**Input**: Design documents from `specs/001-rate-limit-relay/`
**Prerequisites**: [plan.md](plan.md) (required), [spec.md](spec.md) (required)
**Constitution Compliance**: Strict TDD (tests written and confirmed red before code), Go standard library only (zero external dependencies).

## Phase 1: Setup & Foundational Configuration

- [ ] **T001**: Initialize Go module `github-api-relay` (`go mod init github-api-relay`).
- [ ] **T002**: [TDD] Write unit tests in `internal/config/config_test.go` asserting defaults and environment variable parsing (`PORT`, `GITHUB_TOKENS`, `RELAY_AUTH_KEY`, `TOKEN_MIN_REMAINING`, `FALLBACK_UNAUTHENTICATED`, `CACHE_TTL_SECONDS`).
- [ ] **T003**: Implement `internal/config/config.go` using only standard library `os`, `strings`, and `strconv` to make `config_test.go` pass (Green).

---

## Phase 2: Token Pool, Quota Tracking & Fallback Management

- [ ] **T004 [TokenPool]**: [TDD] Write unit tests in `internal/token/pool_test.go`:
  - Token selection rotates between available tokens.
  - Updating token quota via `RecordUsage(token, remaining, resetTime)` marks token exhausted if `remaining < minRemaining`.
  - When all tokens are exhausted, `Acquire()` returns fallback to unauthenticated if `fallbackUnauthenticated=true`, or returns an error if `false`.
  - Works transparently with any token prefix (`ghp_`, `github_pat_`, `gho_`, `ghs_`).
  - Works with empty token list (unauthenticated-only mode).
- [ ] **T005 [TokenPool]**: Implement `internal/token/pool.go` using `sync.RWMutex` to make `pool_test.go` pass (Green).

---

## Phase 3: User Story 1 & 4 - Core Reverse Proxy with Token Injection & Fallback

- [ ] **T006 [US1,US4]**: [TDD] Write unit tests in `internal/proxy/handler_test.go` with an `httptest.Server`:
  - Requests have `Authorization: Bearer <TOKEN>` injected from `TokenPool`.
  - Client-supplied `Authorization` headers are stripped/overwritten.
  - When pool returns unauthenticated fallback, request is proxied with NO `Authorization` header.
  - Response rate-limit headers (`X-RateLimit-Remaining`, `X-RateLimit-Reset`) are parsed and fed back to `TokenPool.RecordUsage()`.
  - Upstream headers and status codes are propagated back to the client.
- [ ] **T007 [US1,US4]**: Implement `internal/proxy/handler.go` using `net/http/httputil.ReverseProxy` with custom `Director` and `ModifyResponse` to make `handler_test.go` pass (Green).

---

## Phase 4: User Story 2 - Optional Relay Authentication Protection

- [ ] **T008 [US2]**: [TDD] Write tests in `internal/auth/middleware_test.go`:
  - When `RELAY_AUTH_KEY` is set, requests without matching `Authorization: Bearer <KEY>` or `X-Relay-Key: <KEY>` receive `401 Unauthorized` with JSON error.
  - When matching key is provided, request continues to next handler.
  - When `RELAY_AUTH_KEY` is empty, all requests pass through (open mode).
- [ ] **T009 [US2]**: Implement `internal/auth/middleware.go` using standard `http.Handler` wrapper to make `middleware_test.go` pass (Green).

---

## Phase 5: User Story 5 - Latest Release Redirect Optimization

- [ ] **T010 [US5]**: [TDD] Write tests in `internal/redirect/release_test.go`:
  - Request to `/repos/{owner}/{repo}/releases/latest/download/{asset}` returns `302 Found` with `Location: https://github.com/{owner}/{repo}/releases/latest/download/{asset}`.
  - Requests not matching this pattern pass to the next handler.
- [ ] **T011 [US5]**: Implement `internal/redirect/release.go` using standard `http.Redirect` to make `release_test.go` pass (Green).

---

## Phase 6: User Story 6 - Zero-Quota ETag In-Memory Caching

- [ ] **T012 [US6]**: [TDD] Write tests in `internal/cache/memory_test.go`:
  - Cache response body and `ETag` on `200 OK` `GET` requests.
  - On subsequent request, verify `If-None-Match: <ETag>` is sent to upstream.
  - When upstream responds with `304 Not Modified`, verify relay serves cached body with status `200 OK` and updated rate limit headers.
- [ ] **T013 [US6]**: Implement `internal/cache/memory.go` using `sync.RWMutex` and an in-memory map to make `memory_test.go` pass (Green).

---

## Phase 7: User Story 7 - Status & Telemetry Endpoint

- [ ] **T014 [US7]**: [TDD] Write tests in `internal/telemetry/status_test.go` verifying `GET /_relay/status` returns JSON with uptime, token pool status (each token's masked ID, remaining quota, reset time), and cache count.
- [ ] **T015 [US7]**: Implement `internal/telemetry/status.go` to make `status_test.go` pass (Green).

---

## Phase 8: Application Integration & End-to-End Verification

- [ ] **T016**: Implement `cmd/relay/main.go` wiring all middlewares (Auth $\rightarrow$ Redirect $\rightarrow$ Cache $\rightarrow$ Proxy $\rightarrow$ Status) and setting up graceful shutdown on SIGINT/SIGTERM.
- [ ] **T017**: [TDD] Write end-to-end integration test in `cmd/relay/main_test.go` running the complete relay against a mock GitHub API and verifying all user stories end-to-end.
