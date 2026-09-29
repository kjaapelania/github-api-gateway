# GitHub API Relay Constitution

## Core Principles

### I. Go Standard Library First (Controlled Dependency Scope)
- Use the Go standard library (`net/http`, `net/http/httputil`, `net/http/httptest`, `sync`, `regexp`, `time`, `log/slog`) as the primary foundation.
- **Explicit Exemption**: `gopkg.in/yaml.v3` is authorized exclusively for parsing human-friendly YAML policy and configuration files.
- No external HTTP routing frameworks (no Gin, Fiber, Chi) or external database/caching engines.

### II. Transparent Reverse Proxy & Rate Limit Relief
- The relay acts as an HTTP reverse proxy targeting `https://api.github.com`.
- **Token Injection**: Transparently inject configured GitHub Personal Access Token(s) to elevate rate limits from 60 req/hr (unauthenticated) to 5,000+ req/hr.
- **Smart Caching & Conditional Requests**: Leverage `ETag` / `If-None-Match`. Responses returning `304 Not Modified` preserve upstream quota.
- **Upstream Rate Limit Propagation**: Faithfully forward or expose GitHub rate limit headers (`X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`).

### III. Strict Test-Driven Development (NON-NEGOTIABLE)
- TDD cycle is strictly enforced:
  1. Write unit/integration test using `net/http/httptest`.
  2. Run the test and confirm failure (Red).
  3. Implement the minimal standard library code to pass (Green).
  4. Refactor while keeping tests green.
- No code change or feature PR is accepted without accompanying table-driven unit tests.

### IV. Observability & Graceful Failure
- Use Go's standard `log/slog` for structured, leveled logging.
- Never log raw authorization tokens or secrets.
- Handle upstream GitHub network timeouts and 429 (Rate Limited) errors gracefully.

### V. Fail-Closed Enterprise Access Governance
- **Default Deny**: All requests are denied by default unless an explicit rule matches and authorizes them.
- **Symmetric Protection**: Both internal company repositories and external open-source repositories must be guarded by explicit rules to prevent data exfiltration.
- **Hardcoded Denylist**: Endpoints that expose account credentials, personal data, or admin settings (`/user`, `/user/**`, `/admin/**`, `/authorizations/**`) are unconditionally blocked.
- **Unambiguous Matching**: Rules support either glob (`repo`, `path`) or regex (`repo_regex`, `path_regex`), which are strictly mutually exclusive per rule.

## Governance

- This Constitution supersedes all ad-hoc instructions.
- Any architectural change or added dependency must explicitly amend this file first.
- Every task generated via Spec Kit must cite compliance with these Principles.

**Version**: 1.1.0 | **Ratified**: 2026-09-29 | **Amended**: 2026-09-29 (YAML exemption & Fail-Closed Governance)
