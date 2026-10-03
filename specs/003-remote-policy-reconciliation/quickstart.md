# Quickstart: Remote Policy Loading and Declarative Reconciliation

This guide provides end-to-end validation scenarios for the remote policy, declarative reconciliation, and administrative security capabilities.

---

## Prerequisites

- Go `1.25.x` installed (via `mise install`).
- Docker / Docker Compose running locally.

---

## Scenario 1: Remote Policy Loading via URL

Start the gateway pointing to an HTTP/HTTPS policy source:

```bash
# 1. Start a temporary mock server serving policy.yaml
python3 -m http.server 29090 --directory . &
SERVER_PID=$!

# 2. Start the gateway pointing to the remote policy URL
GITHUB_API_GATEWAY_POLICY_PATH="http://127.0.0.1:29090/policy.yaml" \
GITHUB_API_GATEWAY_PORT=28280 \
./bin/gateway &
GATEWAY_PID=$!

# 3. Verify status endpoint reports the remote policy rules
curl -s http://127.0.0.1:28280/_gateway/status | jq .policy_rules

# Cleanup
kill $GATEWAY_PID $SERVER_PID
```

---

## Scenario 2: Declarative Background Reconciliation Loop

Verify that the gateway automatically hot-swaps updated remote policies:

```bash
# 1. Run gateway with a 5-second polling interval
GITHUB_API_GATEWAY_POLICY_PATH="http://127.0.0.1:29090/policy.yaml" \
GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL=5s \
./bin/gateway

# 2. Update policy.yaml in the upstream server directory:
# The gateway logs:
# {"level":"INFO","msg":"reconciliation: remote policy updated","active_keys":0,"rules":5}
```

---

## Scenario 3: Authenticated Remote Push Reload

Verify that remote clients can trigger instant reloads using a Bearer token:

```bash
# 1. Start gateway with remote admin enabled and a secret token
GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true \
GITHUB_API_GATEWAY_ADMIN_TOKEN="deploy-key-secret-999" \
./bin/gateway &

# 2. Attempt reload without token -> 401 Unauthorized
curl -i -X POST http://127.0.0.1:28280/_gateway/reload
# Returns: HTTP/1.1 401 Unauthorized {"error":"unauthorized"}

# 3. Reload with valid Bearer token -> 200 OK
curl -i -X POST http://127.0.0.1:28280/_gateway/reload \
  -H "Authorization: Bearer deploy-key-secret-999"
# Returns: HTTP/1.1 200 OK {"status":"reloaded","active_auth_keys":0,"policy_rules":4}
```

---

## Scenario 4: Fail-Closed Startup Guardrail

Verify that the gateway refuses to boot if remote admin is enabled without a token:

```bash
GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN=true \
GITHUB_API_GATEWAY_ADMIN_TOKEN="" \
./bin/gateway
# Exits with status 1:
# {"level":"ERROR","msg":"configuration error","error":"remote admin cannot be enabled without GITHUB_API_GATEWAY_ADMIN_TOKEN"}
```
