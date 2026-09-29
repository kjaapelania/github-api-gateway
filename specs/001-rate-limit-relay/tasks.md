# Tasks: GitHub API Rate Limit Relay

**Input**: Design documents from `specs/001-rate-limit-relay/`
**Prerequisites**: [plan.md](plan.md) (required), [spec.md](spec.md) (required)
**Constitution Compliance**: Strict TDD (tests written and confirmed red before code), Go standard library only (zero external dependencies).

## Phase 1: Setup & Foundational Configuration

- [ ] **T001**: Initialize Go module `github-api-relay` (`go mod init github-api-relay`).
- [ ] **T002**: [TDD] Write unit tests in `internal/config/config_test.go` asserting defaults and environment variable parsing (`PORT`, `GITHUB_TOKEN`, `RELAY_AUTH_KEY`, `CACHE_TTL_SECONDS`).
- [ ] **T003**: Implement `internal/config/config.go` using only standard library `os` and `strconv` to make `config_test.go` pass (Green).

---

## Phase 2: User Story 1 (P1) - Core Token Injection Reverse Proxy

- [ ] **T004 [US1]**: [TDD] Write unit tests in `internal/proxy/handler_test.go` with an `httptest.Server` simulating GitHub API:
  - Assert that client requests have `Authorization: Bearer <GITHUB_TOKEN>` injected.
  - Assert that client-supplied `Authorization` headers are stripped/overwritten.
  - Assert that upstream `X-RateLimit-*` headers and status codes are propagated back to the client.
- [ ] **T005 [US1]**: Implement `internal/proxy/handler.go` using `net/http/httputil.ReverseProxy` with custom `Director` and `ModifyResponse` to make `handler_test.go` pass (Green).

---

## Phase 3: User Story 2 (P2) - Optional Relay Authentication Protection

- [ ] **T006 [US2]**: [TDD] Write tests in `internal/auth/middleware_test.go`:
  - When `RELAY_AUTH_KEY` is set, requests without matching `Authorization: Bearer <KEY>` or `X-Relay-Key: <KEY>` receive `401 Unauthorized` with JSON error.
  - When matching key is provided, request continues to next handler.
  - When `RELAY_AUTH_KEY` is empty, all requests pass through (open mode).
- [ ] **T007 [US2]**: Implement `internal/auth/middleware.go` using standard `http.Handler` wrapper to make `middleware_test.go` pass (Green).

---

## Phase 4: User Story 3 (P3) - Latest Release Redirect Optimization

- [ ] **T008 [US3]**: [TDD] Write tests in `internal/redirect/release_test.go`:
  - Request to `/repos/{owner}/{repo}/releases/latest/download/{asset}` returns `302 Found` with `Location: https://github.com/{owner}/{repo}/releases/latest/download/{asset}`.
  - Requests not matching this pattern pass to the next handler.
- [ ] **T009 [US3]**: Implement `internal/redirect/release.go` using standard `http.Redirect` to make `release_test.go` pass (Green).

---

## Phase 5: User Story 4 (P4) - Zero-Quota ETag In-Memory Caching

- [ ] **T010 [US4]**: [TDD] Write tests in `internal/cache/memory_test.go`:
  - Cache response body and `ETag` on `200 OK` `GET` requests.
  - On subsequent request, verify `If-None-Match: <ETag>` is sent to upstream.
  - When upstream responds with `304 Not Modified`, verify relay serves cached body with status `200 OK` and updated rate limit headers.
- [ ] **T011 [US4]**: Implement `internal/cache/memory.go` using `sync.RWMutex` and an in-memory map to make `memory_test.go` pass (Green).

---

## Phase 6: User Story 5 (P5) - Status & Telemetry Endpoint

- [ ] **T012 [US5]**: [TDD] Write tests in `internal/telemetry/status_test.go` verifying `GET /_relay/status` returns JSON with uptime, cache entry count, and last observed rate-limit quotas.
- [ ] **T013 [US5]**: Implement `internal/telemetry/status.go` to make `status_test.go` pass (Green).

---

## Phase 7: Application Integration & End-to-End Verification

- [ ] **T014**: Implement `cmd/relay/main.go` wiring all middlewares (Auth $\rightarrow$ Redirect $\rightarrow$ Cache $\rightarrow$ Proxy $\rightarrow$ Status) and setting up graceful shutdown on SIGINT/SIGTERM.
- [ ] **T015**: [TDD] Write end-to-end integration test in `cmd/relay/main_test.go` running the complete relay against a mock GitHub API and verifying all user stories end-to-end.
