# Data Model: Remote Policy Loading and Declarative Reconciliation

**Feature**: Remote Policy Loading and Declarative Reconciliation  
**Branch**: `003-remote-policy-reconciliation` | **Date**: 2026-10-04  
**Status**: Completed

---

## 1. Core Entities

### PolicySource
Represents the origin and metadata of the gateway access policy.

```go
type PolicySource struct {
    URI          string        // Path on disk (e.g. "policy.yaml") or URL ("https://...")
    IsRemote     bool          // True if URI has http:// or https:// scheme
    LastETag     string        // Upstream HTTP ETag header (if provided by remote server)
    LastHash     [32]byte      // SHA-256 hash of the active raw policy content
    LastModified time.Time     // Timestamp of the last successful policy load
    RawContent   []byte        // In-memory cache of raw YAML bytes
}
```

#### Validation Rules:
- `URI` MUST NOT be empty.
- If `IsRemote` is true, `URI` MUST parse as a valid `http://` or `https://` URL via `net/url.ParseRequestURI`.
- Response body fetched from remote `URI` MUST NOT exceed 10MB (`10 * 1024 * 1024` bytes).

---

### ReconciliationConfig
Defines the behavior of the background pull-based state synchronizer.

```go
type ReconciliationConfig struct {
    PollInterval time.Duration // Frequency of remote policy checks (e.g. 30s, 1m)
    Enabled      bool          // True if PollInterval > 0 and PolicySource.IsRemote == true
    Timeout      time.Duration // HTTP fetch timeout (default: 10s)
}
```

#### Validation Rules:
- If `PollInterval` < 0, it is treated as disabled (`Enabled = false`).
- If `PolicySource.IsRemote` is false, background polling is disabled.

---

### AdminConfig
Defines administrative endpoint perimeter and authentication rules.

```go
type AdminConfig struct {
    AllowRemoteAdmin bool   // Allows non-loopback IPs to reach /_gateway/reload
    AdminToken       string // Secret Bearer token required for administrative operations
}
```

#### Validation & Fail-Closed Rules:
- If `AllowRemoteAdmin == true && AdminToken == ""`, configuration is invalid and triggers immediate startup failure (`os.Exit(1)`).
- If `AdminToken != ""`, all callers (both loopback and remote) must present `Authorization: Bearer <AdminToken>`.

---

### ReloadResponse
Payload returned by `POST /_gateway/reload`.

```json
{
  "status": "reloaded",
  "active_auth_keys": 2,
  "policy_rules": 4,
  "source": "https://git.internal/policy.yaml",
  "etag": "\"v1.4.2\"",
  "timestamp": "2026-10-04T02:00:00Z"
}
```

---

## 2. State Lifecycle & Transitions

```
[ Gateway Boot ]
       │
       ▼
Fetch Policy (Disk or Remote URL) ──(Network Error / 404 / Parse Error)──► [ Fatal Startup Exit ]
       │
       ▼ (Success)
Active In-Memory Policy Loaded
       │
       ├───────────────────────────────────────┬───────────────────────────────────────┐
       ▼                                       ▼                                       ▼
[ Background Ticker ]                   [ SIGHUP Signal ]                   [ POST /_gateway/reload ]
       │                                       │                                       │
       ▼                                       ▼                                       ▼
Fetch Remote Policy                     Re-fetch Policy                     Check Remote & Bearer Token
(If-None-Match / Hash Check)            (Disk or URL)                                  │
       │                                       │                                       ▼
  Unchanged?                                   │                            Re-fetch Policy (Disk or URL)
 ┌─────┴──────┐                                │                                       │
 │            │                                └───────────────────┬───────────────────┘
 ▼            ▼                                                    │
No-op    New Content                                               ▼
              └────────────────────────────────────────► Atomically Swap Policy Engine
                                                                   │
                                                      (On Error: Preserve Active Policy)
```
