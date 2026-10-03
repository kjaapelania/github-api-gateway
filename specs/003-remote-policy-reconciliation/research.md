# Technical Research: Remote Policy Loading and Declarative Reconciliation

**Feature**: Remote Policy Loading and Declarative Reconciliation  
**Branch**: `003-remote-policy-reconciliation` | **Date**: 2026-10-04  
**Status**: Completed

---

## 1. Remote HTTP/HTTPS Policy Fetching

### Decision
Implement `policy.Fetcher` using Go's standard library `net/http.Client` configured with:
- Strict 10-second request timeout (`context.WithTimeout`).
- Size cap of 10MB using `io.LimitReader(resp.Body, 10*1024*1024)` to protect against memory exhaustion or decompression bombs.
- Support for both `http://` and `https://` schemas; local paths continue using `os.ReadFile`.
- Content verification via `crypto/sha256` checksumming.

### Rationale
- Complies strictly with Constitution Principle I (Go Standard Library First).
- Zero external dependencies required.
- Protects the gateway from hanging indefinitely on dead or unresponsive remote policy servers.

### Alternatives Considered
- **`os/exec` calling `curl`**: Rejected because it introduces external OS binary dependencies, behaves inconsistently across platforms (Windows vs Linux), and introduces command injection risks.
- **Third-party libraries (e.g., `resty`, `go-getter`)**: Rejected because Constitution Principle I strictly forbids non-standard HTTP dependencies.

---

## 2. Declarative Pull-Based Reconciliation Engine

### Decision
Implement a background goroutine in `internal/policy` or `cmd/gateway` driven by `time.NewTicker`:
- Controlled by `GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL` (e.g. `30s`, `1m`). If unset or $\le 0$, the ticker is disabled.
- **Two-tier cache validation**:
  1. **HTTP Conditional Revalidation**: Sends `If-None-Match: <stored_etag>` on polling requests. If upstream returns `304 Not Modified`, the engine immediately skips parsing and leaves active policy untouched.
  2. **SHA-256 Hash Verification**: If the upstream server does not support ETags (returns `200 OK`), the engine computes `sha256.Sum256(rawBytes)` and compares it to `lastHash`. If identical, it skips YAML parsing.
- **Graceful Lifecycle**: Managed via `context.Context` tied to server shutdown for clean teardown without goroutine leaks.

### Rationale
- Completely autonomous GitOps pattern. When policy commits merge to Git, the gateway pulls and reconciles desired state within `POLL_INTERVAL`.
- Consumes zero CPU and zero heap allocation when the remote policy is unchanged.

### Alternatives Considered
- **Filesystem watchers (`fsnotify`)**: Only works on local mounted files; completely useless for remote URLs (GitHub raw, S3, OPA).
- **WebSockets / gRPC Control Plane**: Overly complex for simple declarative YAML reconciliation; adds heavy state management and external protocols.

---

## 3. Administrative Authentication & Loopback Guard

### Decision
Harden administrative routes (`/_gateway/reload`) using standard library HTTP middleware:
- **Perimeter Check**: Inspects `r.RemoteAddr` via `net.SplitHostPort` and `net.ParseIP(host).IsLoopback()`.
  - Loopback IPs (`127.0.0.1/8`, `::1`, `::ffff:127.0.0.1`) are permitted without token **only if** `ADMIN_TOKEN` is unset.
  - If `ADMIN_TOKEN` is configured, **all callers** (including loopback) must provide the Bearer token (mitigating SSRF).
- **Constant-Time Comparison**: Compares incoming Bearer token to `GITHUB_API_GATEWAY_ADMIN_TOKEN` using `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.
- **Fail-Closed Startup Check**: During `config.Load()`, if `AllowRemoteAdmin == true` and `AdminToken == ""`, configuration validation fails fast with an explicit error before any socket is bound.

### Rationale
- Protects against Server-Side Request Forgery (SSRF) and loopback reverse-proxy bypass.
- Guarantees that an unauthenticated remote reload endpoint cannot exist under any circumstances.

### Alternatives Considered
- **HTTP Basic Authentication**: Rejected because modern CI/CD webhooks and automated tools standardize on Bearer tokens.
- **IP Allowlisting**: Fragile in cloud environments where NAT gateways, Kubernetes ingresses, and dynamic IPs change frequently. Token authentication provides reliable identity verification.

---

## 4. Concurrent Reload Safety

### Decision
Serialize all reload invocations (from `SIGHUP`, background reconciliation ticker, and `POST /_gateway/reload`) through a dedicated `sync.Mutex` on `App`.
- If an update fails (e.g. invalid YAML syntax, 404 from upstream), the mutex ensures the previous valid policy engine remains active and undisturbed.
- In-flight client proxy requests read the policy pointer atomically via `sync/atomic.Pointer[policy.Policy]` or read-write locks (`sync.RWMutex`), guaranteeing zero packet drops.
