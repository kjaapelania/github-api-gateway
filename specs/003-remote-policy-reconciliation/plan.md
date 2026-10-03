# Implementation Plan: Remote Policy Loading and Declarative Reconciliation

**Branch**: `003-remote-policy-reconciliation` | **Date**: 2026-10-04 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from [`specs/003-remote-policy-reconciliation/spec.md`](spec.md)

---

## Summary

Enable `github-api-gateway` to function as a declarative, GitOps-ready data plane:
1. **Remote Policy Loading**: Generalize `GITHUB_API_GATEWAY_POLICY_PATH` to transparently load policy YAML from `http://` and `https://` URLs using the Go standard library with strict 10s timeouts and 10MB bounds, while retaining 100% backward compatibility for local files.
2. **Autonomous Pull-Based Reconciliation**: Implement an event-driven background reconciler (`GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL`) that conditionally checks remote policy via `If-None-Match` (ETag) and SHA-256 hashes, performing zero heap allocation on unchanged policies and atomic in-memory hot-swaps on updates.
3. **Authenticated Push-Based Control Plane**: Secure `POST /_gateway/reload` with defense-in-depth: loopback-only by default; when `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true`, enforce Bearer token authentication with constant-time comparison and fail-closed startup validation.

---

## Technical Context

**Language/Version**: Go `1.25.x`  
**Primary Dependencies**: Go standard library (`net/http`, `net/url`, `crypto/sha256`, `crypto/subtle`, `sync`, `time`, `log/slog`) and `gopkg.in/yaml.v3` (Constitution-authorized exemption for YAML parsing). **Zero new third-party dependencies**.  
**Storage**: Ephemeral in-memory atomic pointer (`sync.RWMutex` / atomic swap) with no external database.  
**Testing**: Go standard library `testing` package with `net/http/httptest` and race detector (`-race`).  
**Target Platform**: Linux (Debian, Alpine, Distroless containers), macOS (Intel, Apple Silicon), Windows (64-bit).  
**Project Type**: Cloud-native reverse proxy daemon / API gateway.  
**Performance Goals**: <50ms hot-swap latency, 0 dropped in-flight requests, 0 allocations on unchanged poll checks.  
**Security Constraints**: Fail-closed on missing tokens, zero plaintext secret exposure in logs/errors, `crypto/subtle.ConstantTimeCompare` for Bearer token validation.

---

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-evaluated post-design.*

| Principle | Requirement | Compliance Status | Rationale |
| :--- | :--- | :---: | :--- |
| **I. Go Standard Library First** | Zero non-standard dependencies except `gopkg.in/yaml.v3` | **PASS** | Uses standard `net/http.Client`, `crypto/sha256`, `crypto/subtle`, and `time.Ticker`. No external libraries added. |
| **II. Transparent Reverse Proxy** | GitHub API proxying and quota preservation | **PASS** | Reconciled policies immediately govern the proxy engine without disrupting active GitHub rate-limit relief. |
| **III. Strict TDD Enforcement** | Unit & integration tests written before code (Red-Green-Refactor) | **PASS** | Every component (`Fetcher`, `Reconciler`, `AdminGuard`) begins with failing `httptest` suites. |
| **IV. Observability & Secrets** | Never log raw secrets; structured `log/slog` logging | **PASS** | Admin token is strictly verified with constant-time comparison and never printed in logs or errors. |
| **V. Fail-Closed Governance** | Default deny, startup refusal on invalid security configs | **PASS** | Gateway refuses to boot if `ALLOW_REMOTE_ADMIN=true` without an admin token. Non-loopback traffic rejected by default. |

---

## Project Structure & Architecture

### Design Artifacts
```text
specs/003-remote-policy-reconciliation/
├── spec.md                   # Feature specification
├── plan.md                   # This implementation plan
├── research.md               # Technical decisions and alternatives
├── data-model.md             # Entities, validation rules, state transitions
├── quickstart.md             # Validation workflows and testing scenarios
├── contracts/
│   ├── reload-api.json       # JSON schema for POST /_gateway/reload
│   └── config.env.schema.json# JSON schema for new environment variables
└── tasks.md                  # Implementation tasks (created in next step)
```

### Source Code Modifications
```text
internal/
├── config/
│   ├── config.go             # Add PolicyPollInterval, AllowRemoteAdmin, AdminToken + validation
│   └── config_test.go        # Unit tests for new config parsing and fail-closed validation
├── policy/
│   ├── engine.go             # Update to use PolicySource and atomic reload
│   ├── fetcher.go            # New: Remote HTTP/HTTPS fetcher with ETag, SHA-256, and local file fallback
│   ├── fetcher_test.go       # New: TDD unit tests for remote policy fetching and caching
│   ├── reconciler.go         # New: Background ticker engine checking for upstream changes
│   └── reconciler_test.go    # New: TDD unit tests for background reconciliation loop
cmd/gateway/
├── app.go                    # Wire fetcher, reconciler, and Bearer token check into App lifecycle
└── main_test.go              # Integration tests for URL loading, reconciliation, and remote auth
```

---

## Detailed Component Architecture

### 1. `internal/policy/fetcher.go`
- Implements `Fetch(ctx context.Context, source *PolicySource) ([]byte, bool, error)`
- If `source.IsRemote`:
  - Builds `http.NewRequestWithContext` with `If-None-Match: source.LastETag`.
  - Sets 10-second timeout.
  - On `304 Not Modified`: returns `nil, false, nil` (unchanged).
  - On `200 OK`: reads body with `io.LimitReader(resp.Body, 10<<20)`. Computes SHA-256. If matching `source.LastHash`, returns `nil, false, nil`.
  - Otherwise returns `rawContent, true, nil` with updated ETag and Hash.
- If not remote:
  - Reads from local disk via `os.ReadFile`. Computes SHA-256. If identical to `source.LastHash`, returns `nil, false, nil`. Otherwise returns new bytes.

### 2. `internal/policy/reconciler.go`
- `Reconciler` struct holding `fetcher`, `source`, `pollInterval`, `reloadFunc`, and `logger`.
- `Start(ctx context.Context)`: launches a goroutine with `time.NewTicker(pollInterval)`.
- On each tick: calls `fetcher.Fetch`. If modified, invokes `reloadFunc(content)`. If error occurs, logs structured warning with `slog.Error` and preserves active state.

### 3. `cmd/gateway/app.go` (Admin Security Guard)
- Updates `reloadHandler`:
  1. Checks loopback: `net.ParseIP(host).IsLoopback()`.
  2. If not loopback:
     - If `!cfg.AllowRemoteAdmin`: returns `403 Forbidden`.
     - Checks `Authorization: Bearer <token>`.
     - Validates token with `subtle.ConstantTimeCompare([]byte(token), []byte(cfg.AdminToken)) == 1`. If mismatch, returns `401 Unauthorized`.
  3. If loopback:
     - If `cfg.AdminToken != ""`: requires Bearer token (SSRF protection).
     - If `cfg.AdminToken == ""`: permits without token (local dev ease).

---

## Phased Implementation Roadmap

1. **Phase 1: Configuration & Fail-Closed Guardrails**
   - Add new fields to `config.Config`.
   - Implement fail-closed startup validation: error when `AllowRemoteAdmin && AdminToken == ""`.
   - Unit tests in `config_test.go`.

2. **Phase 2: Universal Policy Fetcher (Local & Remote HTTP/HTTPS)**
   - Implement `PolicySource` and `Fetcher` with 10s timeout and 10MB limit.
   - Implement ETag conditional requests and SHA-256 content hashing.
   - Comprehensive unit tests with `httptest.Server` in `fetcher_test.go`.

3. **Phase 3: Background Reconciliation Engine**
   - Implement `Reconciler` background worker.
   - Handle 304 Not Modified, hash equivalence, network errors, and corrupt YAML rollback.
   - Unit tests with virtual clocks / short tickers in `reconciler_test.go`.

4. **Phase 4: Administrative Authentication & Handler Hardening**
   - Implement Bearer token extraction and `subtle.ConstantTimeCompare`.
   - Update `reloadHandler` in `cmd/gateway/app.go` with loopback/remote matrix.
   - End-to-end integration tests in `main_test.go`.

5. **Phase 5: Operational Wiring, Documentation & Verification**
   - Wire reconciler lifecycle into `cmd/gateway/main.go` and `app.go`.
   - Update `README.md` and `.env.example`.
   - Run quality gate `mise run check-all` with zero warnings and `-race` clean.
