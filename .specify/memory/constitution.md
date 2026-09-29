# GitHub API Relay Constitution

## Core Principles

### I. Go Standard Library First (Zero External Dependencies)
- Use only the Go standard library (`net/http`, `net/http/httputil`, `net/http/httptest`, `sync`, `time`, etc.).
- No external HTTP frameworks (no Gin, Fiber, Chi) or third-party caching libraries.
- The binary must remain lightweight, fast to compile, and easy to audit.

### II. Transparent Reverse Proxy & Rate Limit Relief
- The relay acts as an HTTP reverse proxy targeting `https://api.github.com`.
- **Token Injection**: Transparently inject configured GitHub Personal Access Token(s) to elevate rate limits from 60 req/hr (unauthenticated) to 5,000+ req/hr.
- **Smart Caching & Conditional Requests**: Leverage `ETag` / `If-None-Match` and `If-Modified-Since` headers. GitHub responses returning `304 Not Modified` preserve upstream quota.
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
- Handle upstream GitHub network timeouts and 429 (Rate Limited) errors gracefully with meaningful error responses.

## Governance

- This Constitution supersedes all ad-hoc instructions.
- Any architectural change or added dependency must explicitly amend this file first.
- Every task generated via Spec Kit must cite compliance with Principles I, II, and III.

**Version**: 1.0.0 | **Ratified**: 2026-09-29
