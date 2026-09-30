# Quickstart: GitHub API Gateway

This guide provides practical curl commands and scenarios to verify all gateway capabilities manually.

---

## 1. Setup & Launch

Ensure your GitHub token is set in your local `.env`:
```bash
GITHUB_API_GATEWAY_TOKENS=ghp_yourTokenHere
```

Build and launch the gateway:
```bash
go build -o bin/gateway ./cmd/gateway
./bin/gateway -port 8080
```

---

## 2. Validation Scenarios

### Scenario 1: Health & Telemetry Status
Query the status endpoint to verify active tokens and policy rules:
```bash
curl -s http://localhost:8080/_gateway/status | jq .
```
**Expected Response**:
```json
{
  "status": "ok",
  "uptime": "12s",
  "active_relay_keys": 0,
  "cache_entries": 0,
  "policy_rules": 4,
  "tokens": [
    {
      "masked_token": "ghp_...XXXX",
      "remaining_quota": 5000,
      "reset_time": 1900000000
    }
  ]
}
```

---

### Scenario 2: Transparent Proxying & ETag Caching
Send a GET request to an allowed repository:
```bash
curl -i http://localhost:8080/repos/my-company/frontend/releases/latest \
  -H "Authorization: Bearer key-ci"
```
- **1st Request**: Upstream receives injected GitHub token; response is cached.
- **2nd Request**: Gateway sends `If-None-Match: <ETag>`; upstream returns `304 Not Modified`; gateway serves body at **0 rate limit cost**.

---

### Scenario 3: Per-Client Rate Limiting & Burst Protection
Send a rapid burst of requests exceeding the burst limit (default burst: 10):
```bash
for i in {1..12}; do
  echo "Request $i:"
  curl -s -o /dev/null -w "Status: %{http_code}\n" http://localhost:8080/repos/my-company/frontend/releases/latest \
    -H "X-Gateway-Key: client-alpha"
done
```
**Expected Output**:
- Requests 1 to 10: `Status: 200`
- Request 11 & 12: `Status: 429`

Inspect the 429 response headers and JSON body:
```bash
curl -i http://localhost:8080/repos/my-company/frontend/releases/latest \
  -H "X-Gateway-Key: client-alpha"
```
**Expected Header & Body**:
```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 1

{
  "error": "client_rate_limited",
  "message": "client exceeded maximum request rate limit to protect shared pool",
  "retry_after_seconds": 1
}
```

---

### Scenario 4: Independent Client Isolation
While `client-alpha` is rate-limited, query immediately with a different client key:
```bash
curl -i http://localhost:8080/repos/my-company/frontend/releases/latest \
  -H "X-Gateway-Key: client-beta"
```
**Expected**: `client-beta` receives `200 OK` immediately (independent token bucket).

---

### Scenario 5: Zero-Restart Dynamic Hot-Reload
Modify `policy.yaml` or change `GITHUB_API_GATEWAY_CLIENT_MAX_RPM` in `.env`, then trigger in-memory reload without dropping connections:
```bash
curl -i -X POST http://localhost:8080/_gateway/reload
```
**Expected Response**:
```json
{
  "status": "reloaded",
  "active_auth_keys": 2,
  "policy_rules": 4,
  "active_tokens": 1
}
```
*(Or send `pkill -HUP gateway`)*.
