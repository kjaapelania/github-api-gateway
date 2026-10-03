# Feature Specification: Remote Policy Loading and Declarative Reconciliation

**Feature Branch**: `003-remote-policy-reconciliation`  
**Created**: 2026-10-04  
**Status**: Ready for Planning  
**Input**: "Support remote policy loading from HTTP/HTTPS URLs, declarative pull-based reconciliation loop with ETag/hash checks, and authenticated push-based remote reload with fail-closed security guardrails."

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Remote Policy Loading via URL (Priority: P1)

As an infrastructure operator,  
I want the gateway to fetch its access policy directly from a remote HTTP or HTTPS URL (such as a GitHub raw file, S3 bucket, or internal control plane),  
So that I can manage policy centrally in Git without baking static policy files into container images or mounting local files.

**Why this priority**: Core enabler for cloud-native deployment. Allows the gateway to run in immutable environments (e.g. AWS ECS, Google Cloud Run, Kubernetes) without volume mounts.

**Independent Test**: Configure `GITHUB_API_GATEWAY_POLICY_PATH=https://.../policy.yaml` pointing to a mock HTTP server. Verify that the gateway starts up, parses the remote rules, and enforces them correctly against incoming proxy traffic.

**Acceptance Scenarios**:

1. **Given** `GITHUB_API_GATEWAY_POLICY_PATH` is an `http://` or `https://` URL, **When** the gateway initializes, **Then** it fetches the policy over HTTP/HTTPS with a 10-second timeout, parses the YAML rules, and starts serving requests.
2. **Given** `GITHUB_API_GATEWAY_POLICY_PATH` is a local file path (e.g. `policy.yaml`), **When** the gateway initializes, **Then** it reads the file from the local filesystem (preserving 100% backward compatibility).
3. **Given** the remote policy URL returns an HTTP error (e.g. `404 Not Found` or `500 Internal Server Error`) during initial startup, **When** the gateway attempts to boot, **Then** it logs a fatal error and refuses to start (fail-closed).

---

### User Story 2 - Declarative Pull-Based Reconciliation Loop (Priority: P2)

As a DevOps engineer,  
I want the gateway to periodically check its remote policy source in the background and automatically update its in-memory rules when changes occur,  
So that our GitOps pipeline can push policy changes to Git and have the gateway reconcile desired state automatically without requiring manual restarts or webhook triggers.

**Why this priority**: Eliminates human and webhook coordination for policy updates. Delivers autonomous, Kubernetes-style state reconciliation.

**Independent Test**: Start the gateway with a 1-second reconciliation interval. Modify the served policy content on the mock upstream server. Verify that within the interval, the gateway detects the change, parses the new rules, and applies them with zero dropped traffic.

**Acceptance Scenarios**:

1. **Given** `GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL` is configured (e.g. `30s`), **When** the background reconciliation ticker fires, **Then** the gateway sends a conditional HTTP request with `If-None-Match` (using the stored `ETag`) or compares the SHA-256 hash of the content.
2. **Given** the upstream policy returns `304 Not Modified` or the SHA-256 hash is unchanged, **When** reconciliation checks complete, **Then** the gateway performs zero allocations and leaves existing rules untouched.
3. **Given** the upstream policy has changed with valid new YAML syntax, **When** reconciliation fires, **Then** the gateway parses the new policy and hot-swaps the rules in memory atomically.
4. **Given** the upstream policy changes to an invalid YAML syntax or invalid rule configuration, **When** reconciliation fires, **Then** the gateway logs an error, rejects the change, and safely preserves the previous active policy without interrupting traffic.
5. **Given** `GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL` is empty or 0, **When** the gateway runs, **Then** no background polling goroutine is spawned.

---

### User Story 3 - Authenticated Remote Push-Based Reload (Priority: P2)

As a CI/CD pipeline or cluster orchestrator,  
I want to trigger an immediate, zero-downtime policy reload via `POST /_gateway/reload` over the network,  
So that urgent security changes or deployment webhooks can force an instant hot-reload without waiting for the next polling interval.

**Why this priority**: Provides deterministic, immediate synchronization for time-sensitive security interventions and CI/CD pipelines.

**Independent Test**: Send a `POST` request to `http://<host>:28280/_gateway/reload` with an `Authorization: Bearer <secret>` header. Verify that the gateway re-fetches the policy and returns `200 OK` with JSON telemetry.

**Acceptance Scenarios**:

1. **Given** remote admin is enabled and a valid `Bearer <token>` is provided, **When** a client sends `POST /_gateway/reload` from a non-loopback network IP, **Then** the request is authorized, policy is reloaded, and `200 OK` with status `reloaded` is returned.
2. **Given** remote admin is enabled, **When** a client sends `POST /_gateway/reload` with an invalid or missing token, **Then** the request is rejected with `401 Unauthorized` (`{"error":"unauthorized"}`).
3. **Given** remote admin is disabled (`GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=false`), **When** a client from a non-loopback IP calls `POST /_gateway/reload`, **Then** the request is rejected with `403 Forbidden` (`{"error":"forbidden: remote admin is disabled"}`).

---

### User Story 4 - Fail-Closed Security & Startup Guardrails (Priority: P1)

As a security architect,  
I want the gateway to enforce strict defense-in-depth principles so that administrative endpoints cannot be accidentally exposed without authentication,  
So that we eliminate the risk of remote unauthenticated tampering, SSRF vulnerabilities, and reverse-proxy header spoofing.

**Why this priority**: Prevents critical misconfigurations and adheres to the project constitution's fail-closed governance.

**Independent Test**: Attempt to start the gateway with `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true` but without an admin token. Verify that the process fails immediately on startup.

**Acceptance Scenarios**:

1. **Given** `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true` and `GITHUB_API_GATEWAY_ADMIN_TOKEN` is unset or empty, **When** the gateway starts, **Then** it exits immediately with a fatal error: `fatal: remote admin cannot be enabled without GITHUB_API_GATEWAY_ADMIN_TOKEN`.
2. **Given** `GITHUB_API_GATEWAY_ADMIN_TOKEN` is set, **When** a request arrives at `/_gateway/reload` from local loopback (`127.0.0.1` or `::1`), **Then** it MUST provide the matching Bearer token (protecting against SSRF vulnerabilities on loopback).
3. **Given** `GITHUB_API_GATEWAY_ADMIN_TOKEN` is unset and remote admin is disabled, **When** a request arrives from local loopback (`127.0.0.1` or `::1`), **Then** it is permitted without a token (seamless local developer convenience).

---

### Edge Cases

- **Upstream Network Flake during Reconciliation**: If the remote policy URL times out or returns network errors during periodic reconciliation, the gateway logs a warning and preserves the previous configuration. It does not crash or drop traffic.
- **Large Remote Policy**: Remote policy download enforces a 10MB maximum response body limit (`io.LimitReader`) to prevent memory exhaustion / decompression bomb attacks.
- **Malformed ETag or HTTP Headers**: If upstream returns an invalid or missing ETag, the gateway falls back to comparing the SHA-256 hash of the payload before parsing.
- **Reverse Proxy Header Stripping**: `X-Forwarded-For` is never trusted for loopback determination. Loopback is strictly determined by the direct connection address (`r.RemoteAddr`).
- **Concurrent Reloads**: If multiple reload triggers arrive concurrently (e.g. SIGHUP, webhook POST, and reconciliation ticker simultaneously), an internal mutex serializes them so only one reload executes at a time.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST accept either a local file path or an `http://`/`https://` URL for `GITHUB_API_GATEWAY_POLICY_PATH`.
- **FR-002**: When fetching remote policies, System MUST use an HTTP client with a strict 10-second timeout and 10MB maximum response body size limit.
- **FR-003**: System MUST fail startup immediately if the initial remote policy fetch fails (fail-closed).
- **FR-004**: System MUST support `GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL` accepting Go duration strings (e.g. `10s`, `1m`, `5m`). If 0 or empty, polling is disabled.
- **FR-005**: The reconciliation loop MUST use HTTP `If-None-Match` when an `ETag` is present, or compare SHA-256 content hashes, to avoid unnecessary parsing when the policy is unchanged.
- **FR-006**: When remote policy changes are detected and valid, System MUST atomically swap the in-memory policy engine without dropping in-flight client requests.
- **FR-007**: When remote policy changes fail validation (e.g. YAML parse error, invalid rule, misspelled method), System MUST log the error and retain the previously active valid policy.
- **FR-008**: System MUST support `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN` (boolean, default: `false`).
- **FR-009**: System MUST support `GITHUB_API_GATEWAY_ADMIN_TOKEN` (string, default: empty).
- **FR-010**: System MUST refuse to start if `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true` and `GITHUB_API_GATEWAY_ADMIN_TOKEN` is empty.
- **FR-011**: When `GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true`, `POST /_gateway/reload` MUST require `Authorization: Bearer <GITHUB_API_GATEWAY_ADMIN_TOKEN>`.
- **FR-012**: When `GITHUB_API_GATEWAY_ADMIN_TOKEN` is configured, loopback requests to `POST /_gateway/reload` MUST also require the Bearer token to protect against SSRF.

---

### Key Entities

- **PolicySource**: Abstraction representing where the policy originates (local file path or remote HTTP/HTTPS URL), including caching metadata (`ETag`, `LastModified`, `ContentHash`).
- **ReconciliationEngine**: Background worker that periodically polls the `PolicySource` and orchestrates zero-downtime hot-swaps.
- **AdminAuthGuard**: Security middleware enforcing loopback and Bearer token access on `/_gateway/reload`.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The gateway hot-swaps updated remote policies in memory in under 50 milliseconds with zero dropped connections.
- **SC-002**: When an unchanged remote policy is polled, reconciliation completes with zero YAML parsing overhead and zero heap allocations for policy rules.
- **SC-003**: 100% of unauthorized remote reload requests are blocked with `401 Unauthorized` or `403 Forbidden`.
- **SC-004**: 100% of corrupt or invalid remote policies result in automatic rollback to the previous valid policy.

---

## Assumptions

- Target policy URLs are reachable over IPv4/IPv6 networks and serve standard YAML payloads.
- Local developer workflow continues to use `mise run bin:reload` (SIGHUP) and `mise run compose:reload` (SIGHUP) with zero auth overhead.
- No client CLI subcommands are added to the Go binary; the Go binary remains a pure server daemon.
