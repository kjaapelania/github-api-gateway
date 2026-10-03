# Tasks: Remote Policy Loading and Declarative Reconciliation

**Branch**: `003-remote-policy-reconciliation` | **Date**: 2026-10-04 | **Status**: Ready for Implementation  
**Spec**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize test fixtures and local mock servers for remote policy verification.

- [x] T001 Setup remote policy test fixtures and mock server helpers in `test/fixtures/remote_policy.yaml` and `internal/policy/testhelper_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core configuration extensions and data models that MUST be complete before user stories begin.

**⚠️ CRITICAL**: All user story implementations depend on these foundational structures.

- [x] T002 [P] Define `PolicySource`, `ReconciliationConfig`, and `AdminConfig` data structures in `internal/policy/model.go` with constraints: "`URI` MUST NOT be empty; if `IsRemote` is true, `URI` MUST parse as a valid `http://` or `https://` URL via `net/url.ParseRequestURI`; response body fetched from remote `URI` MUST NOT exceed 10MB (`10 * 1024 * 1024` bytes)"
- [x] T003 [P] Add unit tests for configuration parsing (`PolicyPollInterval`, `AllowRemoteAdmin`, `AdminToken`) in `internal/config/config_test.go`
- [x] T004 Implement configuration fields (`PolicyPollInterval`, `AllowRemoteAdmin`, `AdminToken`) and validation parsing in `internal/config/config.go`

**Checkpoint**: Foundation ready - configuration structures and entities available for user stories.

---

## Phase 3: User Story 1 - Remote Policy Loading via URL (Priority: P1) 🎯 MVP

**Goal**: Enable the gateway to fetch policy YAML directly from HTTP/HTTPS URLs on startup with a 10s timeout and 10MB limit, retaining local file compatibility and failing closed on initial fetch errors.

**Independent Test**: Start the gateway with `GITHUB_API_GATEWAY_POLICY_PATH=http://127.0.0.1:<mock_port>/policy.yaml`. Verify that rules are fetched, parsed, and enforced against incoming proxy requests.

### Tests for User Story 1 (TDD) ⚠️
> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T005 [P] [US1] Unit tests for `PolicySource` and `Fetcher` (HTTP/HTTPS URL detection, 10s timeout, 10MB body limit, local file fallback) in `internal/policy/fetcher_test.go`
- [x] T006 [P] [US1] Integration tests for initial remote policy fetch and fail-closed startup behavior in `cmd/gateway/main_test.go`

### Implementation for User Story 1

- [x] T007 [US1] Implement `Fetcher` and `Fetch` method with standard library `net/http.Client`, 10s timeout, and `io.LimitReader(resp.Body, 10<<20)` in `internal/policy/fetcher.go`
- [x] T008 [US1] Update `NewApp` and `app.Reload` in `cmd/gateway/app.go` to load policy via `Fetcher` supporting both local file paths and `http://`/`https://` URLs

**Checkpoint**: User Story 1 is functional: gateway boots cleanly from both remote URLs and local files.

---

## Phase 4: User Story 4 - Fail-Closed Security & Startup Guardrails (Priority: P1)

**Goal**: Guard administrative endpoints against unauthenticated access, prevent SSRF on loopback when tokens are set, and enforce immediate startup termination when remote admin is enabled without an admin token.

**Independent Test**: Start gateway with `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true` and empty token; confirm fatal exit. Call `POST /_gateway/reload` over loopback and remote IP with/without token; verify 401/403 rejections.

### Tests for User Story 4 (TDD) ⚠️
> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T009 [P] [US4] Unit tests for fail-closed validation ("If `AllowRemoteAdmin == true && AdminToken == ""` configuration is invalid and triggers immediate startup failure") in `internal/config/config_test.go`
- [x] T010 [P] [US4] Integration tests for loopback and remote SSRF Bearer token authentication matrix in `cmd/gateway/main_test.go`

### Implementation for User Story 4

- [x] T011 [US4] Implement fail-closed startup validation refusing startup when `AllowRemoteAdmin && AdminToken == ""` in `internal/config/config.go`
- [x] T012 [US4] Implement constant-time Bearer token verification using `crypto/subtle.ConstantTimeCompare` and SSRF loopback guard ("If `AdminToken != ""` all callers, loopback and remote, must present `Authorization: Bearer <AdminToken>`") in `cmd/gateway/app.go`

**Checkpoint**: Administrative security perimeter is enforced; fail-closed startup guarantees defense-in-depth.

---

## Phase 5: User Story 2 - Declarative Pull-Based Reconciliation Loop (Priority: P2)

**Goal**: Periodically check remote policy in the background, utilizing HTTP `If-None-Match` (ETag) and SHA-256 hashes to achieve zero heap allocations on unchanged policies, atomic hot-swaps on updates, and safe rollback on corrupt YAML.

**Independent Test**: Run gateway with `GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL=50ms` against a mock server. Update remote policy and observe atomic hot-swap. Serve corrupt YAML and verify active policy remains intact.

### Tests for User Story 2 (TDD) ⚠️
> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T013 [P] [US2] Unit tests for ETag conditional requests (`If-None-Match`), SHA-256 hash comparison, and 304 zero-allocation in `internal/policy/fetcher_test.go`
- [x] T014 [P] [US2] Unit tests for `Reconciler` background loop, periodic polling, error rollback, and graceful shutdown in `internal/policy/reconciler_test.go`

### Implementation for User Story 2

- [x] T015 [US2] Implement conditional ETag caching and SHA-256 hash calculation in `internal/policy/fetcher.go`
- [x] T016 [US2] Implement background ticker `Reconciler` with zero-allocation on 304/matching hash and rollback on corrupt YAML in `internal/policy/reconciler.go`
- [x] T017 [US2] Wire `Reconciler` lifecycle into `NewApp`, `main.go`, and graceful shutdown in `cmd/gateway/app.go` and `cmd/gateway/main.go`

**Checkpoint**: Background reconciliation loop autonomously keeps gateway synchronized with remote state.

---

## Phase 6: User Story 3 - Authenticated Remote Push-Based Reload (Priority: P2)

**Goal**: Expose an authenticated `POST /_gateway/reload` endpoint allowing authorized orchestrators to force immediate zero-downtime policy reloads with JSON telemetry.

**Independent Test**: Issue `POST /_gateway/reload` with `Authorization: Bearer <token>` from non-loopback address. Verify `200 OK` response with JSON telemetry matching `contracts/reload-api.json`.

### Tests for User Story 3 (TDD) ⚠️
> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T018 [P] [US3] Contract and integration tests for `POST /_gateway/reload` matching `contracts/reload-api.json` (200 OK, 400 Bad Request, 401 Unauthorized, 403 Forbidden) in `cmd/gateway/main_test.go`

### Implementation for User Story 3

- [x] T019 [US3] Update `reloadHandler` in `cmd/gateway/app.go` to return structured JSON telemetry per `contracts/reload-api.json` and serialize concurrent reloads with mutex

**Checkpoint**: Push-based zero-downtime reload endpoint fully operational and verified against schema.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: End-to-end documentation, operational quickstart verification, and static analysis quality gate.

- [x] T020 [P] Update documentation in `README.md` and `.env.example` with remote policy, reconciler, and admin token settings
- [x] T021 [P] Validate all end-to-end scenarios from `specs/003-remote-policy-reconciliation/quickstart.md`
- [x] T022 Run complete verification suite `mise run check-all` and ensure zero race conditions (`-race`)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately.
- **Foundational (Phase 2)**: Depends on Phase 1 - BLOCKS all user stories.
- **User Story 1 (Phase 3)**: Depends on Phase 2 - Delivers core MVP.
- **User Story 4 (Phase 4)**: Depends on Phase 2 - Delivers critical security guardrails.
- **User Story 2 (Phase 5)**: Depends on Phase 3 (needs `Fetcher`).
- **User Story 3 (Phase 6)**: Depends on Phase 4 (needs admin guard) and Phase 3 (needs `Fetcher`).
- **Polish (Phase 7)**: Depends on Phases 1–6 being complete.

### User Story Dependencies

```
[Phase 1: Setup] ──► [Phase 2: Foundational]
                             │
              ┌──────────────┴──────────────┐
              ▼                             ▼
     [US1: Remote URL Fetch]       [US4: Security Guardrails]
              │                             │
              ▼                             ▼
     [US2: Pull Reconciler]        [US3: Push Remote Reload]
              │                             │
              └──────────────┬──────────────┘
                             ▼
                 [Phase 7: Polish & Gate]
```

### Parallel Opportunities

- Within Phase 2: `T002` and `T003` can run in parallel.
- Within Phase 3: `T005` and `T006` tests can run in parallel.
- Within Phase 4: `T009` and `T010` tests can run in parallel.
- Within Phase 5: `T013` and `T014` tests can run in parallel.
- Within Phase 7: `T020` and `T021` can run in parallel.

---

## Implementation Strategy

### MVP First (User Story 1 & User Story 4)
1. Complete Phase 1 (Setup) and Phase 2 (Foundational).
2. Complete Phase 3 (US1: Remote URL Fetch) → Test independently.
3. Complete Phase 4 (US4: Fail-Closed Security Guardrails) → Test independently.
4. Verify MVP: Gateway boots from remote policy URL and enforces fail-closed admin security.

### Incremental Delivery (Reconciliation & Push Reload)
5. Complete Phase 5 (US2: Declarative Pull Reconciler) → Test independently.
6. Complete Phase 6 (US3: Authenticated Push Reload) → Test independently.
7. Complete Phase 7 (Polish, Docs & `mise run check-all`).
