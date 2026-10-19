package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github-api-gateway/internal/config"
	"github-api-gateway/internal/token"
)

func TestEndToEnd_Integration(t *testing.T) {
	// 1. Setup Mock GitHub API upstream
	var capturedAuthToken string
	var upstreamCalls int32

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		capturedAuthToken = r.Header.Get("Authorization")

		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1900000000")

		// Revalidation check
		if r.URL.Path == "/repos/my-company/frontend/releases/latest" {
			if r.Header.Get("If-None-Match") == `"v1.2.3-etag"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1.2.3-etag"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name": "v1.2.3"}`))
			return
		}

		// S3 Asset Download Redirect
		if strings.HasPrefix(r.URL.Path, "/repos/my-company/frontend/releases/download/") {
			s3URL := "https://objects.githubusercontent.com/asset-1234.tar.gz"
			http.Redirect(w, r, s3URL, http.StatusFound)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer mockGitHub.Close()

	// 2. Setup App configuration pointing to root policy.yaml
	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatalf("failed to get policy path: %v", err)
	}
	t.Setenv("GITHUB_API_GATEWAY_POLICY_PATH", policyPath)

	cfg := &config.Config{
		Port:                    "28280",
		GitHubTokens:            []string{"ghp_testToken1234567890"},
		AuthKeys:                []string{"key-ci", "key-team-a"},
		PolicyPath:              policyPath,
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
		TargetURL:               mockGitHub.URL,
	}

	app, err := NewApp(cfg, mockGitHub.URL)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	gatewayServer := httptest.NewServer(app.Handler)
	defer gatewayServer.Close()

	client := gatewayServer.Client()

	// -------------------------------------------------------------------------
	// Scenario 1: Telemetry endpoint GET /_gateway/status
	// -------------------------------------------------------------------------
	t.Run("Telemetry Status Endpoint", func(t *testing.T) {
		res, err := client.Get(gatewayServer.URL + "/_gateway/status")
		if err != nil {
			t.Fatalf("status request failed: %v", err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got: %d", res.StatusCode)
		}

		var payload struct {
			Status          string            `json:"status"`
			ActiveRelayKeys int               `json:"active_relay_keys"`
			CacheEntries    int               `json:"cache_entries"`
			PolicyRules     int               `json:"policy_rules"`
			Tokens          []token.TokenInfo `json:"tokens"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode telemetry JSON: %v", err)
		}

		if payload.Status != "ok" {
			t.Errorf("expected status ok, got %s", payload.Status)
		}
		if payload.ActiveRelayKeys != 2 {
			t.Errorf("expected 2 active relay keys, got %d", payload.ActiveRelayKeys)
		}
		if len(payload.Tokens) != 1 || payload.Tokens[0].MaskedToken == "ghp_testToken1234567890" {
			t.Errorf("expected masked token in telemetry, got %+v", payload.Tokens)
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 2: Gateway Key Authentication (User Story 3)
	// -------------------------------------------------------------------------
	t.Run("Gateway Key Authentication", func(t *testing.T) {
		// Missing key -> 401 Unauthorized
		req, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for missing key, got %d", res.StatusCode)
		}

		// Wrong key -> 401 Unauthorized
		req, _ = http.NewRequest("GET", gatewayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		req.Header.Set("Authorization", "Bearer invalid-secret")
		res, err = client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for invalid key, got %d", res.StatusCode)
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 3: Fail-Closed Enterprise Policy & Hardcoded Denylist (User Story 2)
	// -------------------------------------------------------------------------
	t.Run("Policy Hardcoded Denylist and Default Deny", func(t *testing.T) {
		// Hardcoded denylist: /user must return 403 Forbidden even with valid gateway key
		req, _ := http.NewRequest("GET", gatewayServer.URL+"/user", nil)
		req.Header.Set("Authorization", "Bearer key-ci")
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for /user, got %d", res.StatusCode)
		}

		// Default deny: unauthorized repo /repos/unauthorized-org/repo
		req, _ = http.NewRequest("GET", gatewayServer.URL+"/repos/unauthorized-org/secret/releases", nil)
		req.Header.Set("Authorization", "Bearer key-team-a")
		res, err = client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for unapproved repo, got %d", res.StatusCode)
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 4: Successful Token Injection & ETag 304 Revalidation (User Stories 1 & 6)
	// -------------------------------------------------------------------------
	t.Run("Token Injection and ETag Zero Quota Caching", func(t *testing.T) {
		// Call 1: Miss & Cache
		req1, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		req1.Header.Set("Authorization", "Bearer key-ci")
		res1, err := client.Do(req1)
		if err != nil {
			t.Fatalf("req1 failed: %v", err)
		}
		body1, _ := io.ReadAll(res1.Body)
		res1.Body.Close()

		if res1.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK on first fetch, got %d", res1.StatusCode)
		}
		if string(body1) != `{"tag_name": "v1.2.3"}` {
			t.Errorf("unexpected body1: %s", string(body1))
		}
		if capturedAuthToken != "Bearer ghp_testToken1234567890" {
			t.Errorf("expected upstream to receive injected token, got: %s", capturedAuthToken)
		}

		// Call 2: Revalidate -> upstream returns 304, gateway serves 200 OK with cached body
		req2, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		req2.Header.Set("X-Relay-Key", "key-team-a") // verify alternative header works
		res2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("req2 failed: %v", err)
		}
		body2, _ := io.ReadAll(res2.Body)
		res2.Body.Close()

		if res2.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from gateway on 304 revalidation, got %d", res2.StatusCode)
		}
		if string(body2) != `{"tag_name": "v1.2.3"}` {
			t.Errorf("expected cached body2, got: %s", string(body2))
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 5: S3 Binary Redirect Preservation (User Story 6)
	// -------------------------------------------------------------------------
	t.Run("S3 Binary Redirect Preservation", func(t *testing.T) {
		noRedirectClient := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		req, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/my-company/frontend/releases/download/v1.0.0/asset.tar.gz", nil)
		req.Header.Set("Authorization", "Bearer key-ci")
		res, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()

		if res.StatusCode != http.StatusFound {
			t.Fatalf("expected 302 Found, got: %d", res.StatusCode)
		}
		loc := res.Header.Get("Location")
		if loc != "https://objects.githubusercontent.com/asset-1234.tar.gz" {
			t.Errorf("unexpected redirect location: %s", loc)
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 5b: External GitHub-Hosted Tool Release Version Checks (User Story 2)
	// -------------------------------------------------------------------------
	t.Run("External Tool Latest Release Version Check Allowed by Default Policy", func(t *testing.T) {
		for _, allowedPath := range []string{
			"/repos/cli/cli/releases/latest",
			"/repos/jdx/mise/releases/tags/v2026.1.0",
			"/repos/cli/cli/releases/download/v2.60.0/gh_linux_amd64.tar.gz",
		} {
			req, _ := http.NewRequest("GET", gatewayServer.URL+allowedPath, nil)
			req.Header.Set("Authorization", "Bearer key-ci")
			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("req %s failed: %v", allowedPath, err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Errorf("expected 200 OK for external tool release check %s, got %d", allowedPath, res.StatusCode)
			}
		}

		// Non-release endpoints on unapproved external repos must still be blocked (403 Forbidden)
		req, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/cli/cli/issues", nil)
		req.Header.Set("Authorization", "Bearer key-ci")
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for non-release endpoint /repos/cli/cli/issues, got %d", res.StatusCode)
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 6: Zero-Restart Hot-Reloading (POST /_gateway/reload)
	// -------------------------------------------------------------------------
	t.Run("Zero Restart Hot Reloading", func(t *testing.T) {
		// 1. GET /_gateway/reload should return 405 Method Not Allowed
		res, err := client.Get(gatewayServer.URL + "/_gateway/reload")
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 Method Not Allowed for GET /_gateway/reload, got: %d", res.StatusCode)
		}

		// 2. POST /_gateway/reload triggers atomic reload
		res, err = client.Post(gatewayServer.URL+"/_gateway/reload", "application/json", nil)
		if err != nil {
			t.Fatalf("reload request failed: %v", err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("expected 200 OK for POST /_gateway/reload, got %d, body: %s", res.StatusCode, string(body))
		}

		var reloadResp struct {
			Status         string `json:"status"`
			ActiveAuthKeys int    `json:"active_auth_keys"`
			PolicyRules    int    `json:"policy_rules"`
		}
		if err := json.NewDecoder(res.Body).Decode(&reloadResp); err != nil {
			t.Fatalf("failed to decode reload JSON: %v", err)
		}
		if reloadResp.Status != "reloaded" {
			t.Errorf("expected status 'reloaded', got %s", reloadResp.Status)
		}

		// 3. POST /_gateway/reload from non-loopback IP returns 403 Forbidden
		nonLoopbackReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		nonLoopbackReq.RemoteAddr = "192.168.1.100:54321"
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, nonLoopbackReq)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for non-loopback reload, got %d", rec.Code)
		}
	})
}

func TestEndToEnd_ReloadSafetyAndRollback(t *testing.T) {
	// Setup mock upstream
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	}))
	defer mockUpstream.Close()

	// Create temp policy file
	tmpPolicy, err := os.CreateTemp("", "policy-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpPolicy.Name())

	initialPolicyYAML := `
internal_rules:
  - name: "initial"
    repo: "org/initial"
    methods: ["GET"]
`
	_, _ = tmpPolicy.WriteString(initialPolicyYAML)
	_ = tmpPolicy.Close()

	cfg := &config.Config{
		Port:                    "28280",
		PolicyPath:              tmpPolicy.Name(),
		AuthKeys:                []string{"key-v1"},
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
	}

	app, err := NewApp(cfg, mockUpstream.URL)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	server := httptest.NewServer(app.Handler)
	defer server.Close()
	client := server.Client()

	// 1. Initial request under key-v1 and org/initial -> 200 OK
	req1, _ := http.NewRequest("GET", server.URL+"/repos/org/initial/releases", nil)
	req1.Header.Set("Authorization", "Bearer key-v1")
	res1, _ := client.Do(req1)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 under initial config, got %d", res1.StatusCode)
	}
	res1.Body.Close()

	// 2. Corrupt the policy file with invalid syntax
	_ = os.WriteFile(tmpPolicy.Name(), []byte("invalid: yaml: syntax: [unclosed"), 0644)

	// 3. Trigger reload -> must return 400 Bad Request with rollback message
	reloadRes, err := client.Post(server.URL+"/_gateway/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("reload post failed: %v", err)
	}
	defer reloadRes.Body.Close()

	if reloadRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on corrupt reload, got %d", reloadRes.StatusCode)
	}
	var errPayload map[string]string
	_ = json.NewDecoder(reloadRes.Body).Decode(&errPayload)
	if errPayload["config"] != "previous configuration preserved" {
		t.Errorf("expected previous config preserved notice, got: %v", errPayload)
	}

	// 4. Verify server still functions on previous configuration!
	res1Repeat, _ := client.Do(req1)
	if res1Repeat.StatusCode != http.StatusOK {
		t.Fatalf("expected server to safely maintain previous configuration after failed reload, got %d", res1Repeat.StatusCode)
	}
	res1Repeat.Body.Close()

	// 5. Write valid policy with 2 rules and update AUTH_KEYS to 3 keys, then reload and verify /_gateway/status reflects live counts
	updatedPolicyYAML := `
internal_rules:
  - name: "rule-1"
    repo: "org/initial"
    methods: ["GET"]
  - name: "rule-2"
    repo: "org/second"
    methods: ["GET"]
`
	_ = os.WriteFile(tmpPolicy.Name(), []byte(updatedPolicyYAML), 0644)
	t.Setenv("GITHUB_API_GATEWAY_POLICY_PATH", tmpPolicy.Name())
	t.Setenv("GITHUB_API_GATEWAY_AUTH_KEYS", "key-v1,key-v2,key-v3")

	reloadRes2, err := client.Post(server.URL+"/_gateway/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("reload2 post failed: %v", err)
	}
	reloadRes2.Body.Close()
	if reloadRes2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on valid reload, got %d", reloadRes2.StatusCode)
	}

	statusRes, err := client.Get(server.URL + "/_gateway/status")
	if err != nil {
		t.Fatalf("status get failed: %v", err)
	}
	defer statusRes.Body.Close()

	var statusPayload struct {
		ActiveRelayKeys int `json:"active_relay_keys"`
		PolicyRules     int `json:"policy_rules"`
	}
	_ = json.NewDecoder(statusRes.Body).Decode(&statusPayload)
	if statusPayload.ActiveRelayKeys != 3 {
		t.Errorf("expected /_gateway/status active_relay_keys=3 after hot-reload, got %d", statusPayload.ActiveRelayKeys)
	}
	if statusPayload.PolicyRules != 2 {
		t.Errorf("expected /_gateway/status policy_rules=2 after hot-reload, got %d", statusPayload.PolicyRules)
	}
}

func TestEndToEnd_ClientRateLimiting(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"success"}`))
	}))
	defer mockUpstream.Close()

	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatalf("failed to get policy path: %v", err)
	}

	cfg := &config.Config{
		Port:                    "28280",
		GitHubTokens:            []string{"ghp_dummytoken"},
		AuthKeys:                []string{"client-flood", "client-safe"},
		PolicyPath:              policyPath,
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
		TargetURL:               mockUpstream.URL,
		ClientMaxRPM:            60,
		ClientBurst:             2, // Burst capacity of 2 requests
	}

	app, err := NewApp(cfg, mockUpstream.URL)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	server := httptest.NewServer(app.Handler)
	defer server.Close()
	client := server.Client()

	// 1. client-flood sends 1st request -> 200 OK
	req1, _ := http.NewRequest("GET", server.URL+"/repos/my-company/frontend/releases/latest", nil)
	req1.Header.Set("Authorization", "Bearer client-flood")
	res1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK on 1st request, got %d", res1.StatusCode)
	}

	// 2. client-flood sends 2nd request -> 200 OK (burst allowed)
	req2, _ := http.NewRequest("GET", server.URL+"/repos/my-company/frontend/releases/latest", nil)
	req2.Header.Set("Authorization", "Bearer client-flood")
	res2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK on 2nd request, got %d", res2.StatusCode)
	}

	// 3. client-flood sends 3rd rapid request -> 429 Too Many Requests
	req3, _ := http.NewRequest("GET", server.URL+"/repos/my-company/frontend/releases/latest", nil)
	req3.Header.Set("Authorization", "Bearer client-flood")
	res3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()

	if res3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests on 3rd request, got %d", res3.StatusCode)
	}
	if res3.Header.Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429 response")
	}

	var errPayload map[string]any
	if err := json.NewDecoder(res3.Body).Decode(&errPayload); err != nil {
		t.Fatalf("failed to decode 429 JSON response: %v", err)
	}
	if errPayload["error"] != "client_rate_limited" {
		t.Errorf("expected error=client_rate_limited, got: %v", errPayload["error"])
	}

	// 4. client-safe (with different key) must NOT be blocked (independent token bucket)
	reqSafe, _ := http.NewRequest("GET", server.URL+"/repos/my-company/frontend/releases/latest", nil)
	reqSafe.Header.Set("X-Gateway-Key", "client-safe")
	resSafe, err := client.Do(reqSafe)
	if err != nil {
		t.Fatal(err)
	}
	resSafe.Body.Close()
	if resSafe.StatusCode != http.StatusOK {
		t.Errorf("expected independent client-safe to receive 200 OK, got %d", resSafe.StatusCode)
	}
}

func TestEndToEnd_RemotePolicyLoading(t *testing.T) {
	// Remote YAML policy fixture
	remoteYAML := `
internal_rules:
  - name: "remote-org-releases"
    repo: "remote-corp/*"
    path: "/repos/remote-corp/*/releases/**"
    methods: ["GET", "HEAD"]
`
	mockPolicyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/policy.yaml" {
			w.Header().Set("Content-Type", "application/yaml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(remoteYAML))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockPolicyServer.Close()

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"remote_proxy":"success"}`))
	}))
	defer mockGitHub.Close()

	// 1. Success case: NewApp boots from remote policy URL
	cfg := &config.Config{
		Port:                    "28280",
		PolicyPath:              mockPolicyServer.URL + "/policy.yaml",
		AuthKeys:                []string{"remote-key"},
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
		TargetURL:               mockGitHub.URL,
	}

	app, err := NewApp(cfg, mockGitHub.URL)
	if err != nil {
		t.Fatalf("expected NewApp to succeed with remote policy URL, got: %v", err)
	}

	gatewayServer := httptest.NewServer(app.Handler)
	defer gatewayServer.Close()

	// Verify remote rule is enforced:
	// Allowed:
	reqAllowed, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/remote-corp/app/releases/latest", nil)
	reqAllowed.Header.Set("Authorization", "Bearer remote-key")
	resAllowed, err := gatewayServer.Client().Do(reqAllowed)
	if err != nil {
		t.Fatal(err)
	}
	resAllowed.Body.Close()
	if resAllowed.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for remote allowed policy, got %d", resAllowed.StatusCode)
	}

	// Denied (default deny):
	reqDenied, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/other-corp/app/releases", nil)
	reqDenied.Header.Set("Authorization", "Bearer remote-key")
	resDenied, err := gatewayServer.Client().Do(reqDenied)
	if err != nil {
		t.Fatal(err)
	}
	resDenied.Body.Close()
	if resDenied.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for unapproved repo, got %d", resDenied.StatusCode)
	}

	// 2. Fail-closed case: 404 remote policy URL must cause NewApp to fail
	badCfg := &config.Config{
		Port:                    "28280",
		PolicyPath:              mockPolicyServer.URL + "/nonexistent.yaml",
		AuthKeys:                []string{"remote-key"},
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
		TargetURL:               mockGitHub.URL,
	}
	_, err = NewApp(badCfg, mockGitHub.URL)
	if err == nil {
		t.Fatalf("expected NewApp to fail when remote policy returns 404, got nil error")
	}
}

func TestEndToEnd_AdminAuthGuardMatrix(t *testing.T) {
	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// Matrix Case 1: AllowRemoteAdmin=false, AdminToken="" (local dev default)
	t.Run("Default Local Dev", func(t *testing.T) {
		cfg := &config.Config{
			Port:             "28280",
			PolicyPath:       policyPath,
			AllowRemoteAdmin: false,
			AdminToken:       "",
		}
		app, err := NewApp(cfg, "https://api.github.com")
		if err != nil {
			t.Fatal(err)
		}

		// Loopback -> 200 OK
		loopbackReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		loopbackReq.RemoteAddr = "127.0.0.1:45678"
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, loopbackReq)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 OK for loopback reload in local dev mode, got %d", rec.Code)
		}

		// Remote -> 403 Forbidden
		remoteReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		remoteReq.RemoteAddr = "192.168.1.50:45678"
		recRemote := httptest.NewRecorder()
		app.Handler.ServeHTTP(recRemote, remoteReq)
		if recRemote.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for remote reload, got %d", recRemote.Code)
		}
	})

	// Matrix Case 2: AllowRemoteAdmin=false, AdminToken="secret-token" (SSRF loopback protection)
	t.Run("Loopback SSRF Protection with Token", func(t *testing.T) {
		cfg := &config.Config{
			Port:             "28280",
			PolicyPath:       policyPath,
			AllowRemoteAdmin: false,
			AdminToken:       "secret-token",
		}
		app, err := NewApp(cfg, "https://api.github.com")
		if err != nil {
			t.Fatal(err)
		}

		// Loopback without token -> 401 Unauthorized
		loopbackReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		loopbackReq.RemoteAddr = "127.0.0.1:45678"
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, loopbackReq)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for loopback without token when token configured, got %d", rec.Code)
		}

		// Loopback with valid token -> 200 OK
		loopbackAuthReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		loopbackAuthReq.RemoteAddr = "127.0.0.1:45678"
		loopbackAuthReq.Header.Set("Authorization", "Bearer secret-token")
		recAuth := httptest.NewRecorder()
		app.Handler.ServeHTTP(recAuth, loopbackAuthReq)
		if recAuth.Code != http.StatusOK {
			t.Errorf("expected 200 OK for loopback with valid token, got %d", recAuth.Code)
		}

		// Remote even with token -> 403 Forbidden (remote admin disabled)
		remoteReq := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		remoteReq.RemoteAddr = "10.0.0.1:45678"
		remoteReq.Header.Set("Authorization", "Bearer secret-token")
		recRemote := httptest.NewRecorder()
		app.Handler.ServeHTTP(recRemote, remoteReq)
		if recRemote.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for remote request when AllowRemoteAdmin=false, got %d", recRemote.Code)
		}
	})

	// Matrix Case 3: AllowRemoteAdmin=true, AdminToken="secret-token"
	t.Run("Remote Admin Enabled with Token", func(t *testing.T) {
		cfg := &config.Config{
			Port:             "28280",
			PolicyPath:       policyPath,
			AllowRemoteAdmin: true,
			AdminToken:       "secret-token",
		}
		app, err := NewApp(cfg, "https://api.github.com")
		if err != nil {
			t.Fatal(err)
		}

		// Remote without token -> 401 Unauthorized
		reqNoToken := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		reqNoToken.RemoteAddr = "172.16.0.5:12345"
		recNoToken := httptest.NewRecorder()
		app.Handler.ServeHTTP(recNoToken, reqNoToken)
		if recNoToken.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for remote without token, got %d", recNoToken.Code)
		}

		// Remote with wrong token -> 401 Unauthorized
		reqWrong := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		reqWrong.RemoteAddr = "172.16.0.5:12345"
		reqWrong.Header.Set("Authorization", "Bearer wrong-token")
		recWrong := httptest.NewRecorder()
		app.Handler.ServeHTTP(recWrong, reqWrong)
		if recWrong.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for remote with wrong token, got %d", recWrong.Code)
		}

		// Remote with valid token -> 200 OK
		reqValid := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		reqValid.RemoteAddr = "172.16.0.5:12345"
		reqValid.Header.Set("Authorization", "Bearer secret-token")
		recValid := httptest.NewRecorder()
		app.Handler.ServeHTTP(recValid, reqValid)
		if recValid.Code != http.StatusOK {
			t.Errorf("expected 200 OK for remote with valid token, got %d", recValid.Code)
		}
	})
}

func TestEndToEnd_BackgroundReconciliationLoop(t *testing.T) {
	policyV1 := `
internal_rules:
  - name: "alpha-rule"
    repo: "alpha-corp/*"
    path: "/repos/alpha-corp/*/releases/**"
    methods: ["GET"]
`
	policyV2 := `
internal_rules:
  - name: "beta-rule"
    repo: "beta-corp/*"
    path: "/repos/beta-corp/*/releases/**"
    methods: ["GET"]
`
	var currentPolicy atomic.Value
	currentPolicy.Store(policyV1)

	mockPolicyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := currentPolicy.Load().(string)
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(p))
	}))
	defer mockPolicyServer.Close()

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	}))
	defer mockGitHub.Close()

	cfg := &config.Config{
		Port:                    "28280",
		PolicyPath:              mockPolicyServer.URL + "/policy.yaml",
		PolicyPollInterval:      40 * time.Millisecond,
		AuthKeys:                []string{"reconcile-key"},
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
		TargetURL:               mockGitHub.URL,
	}

	app, err := NewApp(cfg, mockGitHub.URL)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}
	defer app.Close()

	gatewayServer := httptest.NewServer(app.Handler)
	defer gatewayServer.Close()
	client := gatewayServer.Client()

	// Initial check: alpha is allowed, beta is forbidden
	reqAlpha, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/alpha-corp/repo/releases/v1", nil)
	reqAlpha.Header.Set("Authorization", "Bearer reconcile-key")
	resAlpha, _ := client.Do(reqAlpha)
	if resAlpha.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for alpha under policyV1, got %d", resAlpha.StatusCode)
	}
	resAlpha.Body.Close()

	reqBeta, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/beta-corp/repo/releases/v1", nil)
	reqBeta.Header.Set("Authorization", "Bearer reconcile-key")
	resBeta, _ := client.Do(reqBeta)
	if resBeta.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for beta under policyV1, got %d", resBeta.StatusCode)
	}
	resBeta.Body.Close()

	// Update policy on mock upstream
	currentPolicy.Store(policyV2)

	// Wait for background reconciler ticker to detect and hot-swap
	deadline := time.Now().Add(1 * time.Second)
	swapped := false
	for time.Now().Before(deadline) {
		reqCheck, _ := http.NewRequest("GET", gatewayServer.URL+"/repos/beta-corp/repo/releases/v1", nil)
		reqCheck.Header.Set("Authorization", "Bearer reconcile-key")
		resCheck, err := client.Do(reqCheck)
		if err == nil {
			status := resCheck.StatusCode
			resCheck.Body.Close()
			if status == http.StatusOK {
				swapped = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !swapped {
		t.Fatalf("background reconciler failed to hot-swap policyV2 within deadline")
	}
}

func TestEndToEnd_ReloadAPIContract(t *testing.T) {
	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Port:             "28280",
		PolicyPath:       policyPath,
		AllowRemoteAdmin: true,
		AdminToken:       "super-secret-reload-token",
		AuthKeys:         []string{"key1"},
	}

	app, err := NewApp(cfg, "https://api.github.com")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Contract: 200 OK
	t.Run("200 OK Contract", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("Authorization", "Bearer super-secret-reload-token")
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		var payload struct {
			Status         string `json:"status"`
			ActiveAuthKeys int    `json:"active_auth_keys"`
			PolicyRules    int    `json:"policy_rules"`
			Source         string `json:"source"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if payload.Status != "reloaded" {
			t.Errorf("expected status 'reloaded', got %q", payload.Status)
		}
		if payload.ActiveAuthKeys != 1 {
			t.Errorf("expected 1 active auth key, got %d", payload.ActiveAuthKeys)
		}
		if payload.PolicyRules <= 0 {
			t.Errorf("expected positive policy rules count, got %d", payload.PolicyRules)
		}
	})

	// 2. Contract: 401 Unauthorized
	t.Run("401 Unauthorized Contract", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("Authorization", "Bearer wrong-token")
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
		}
		var payload map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if payload["error"] != "unauthorized" {
			t.Errorf("expected error 'unauthorized', got %q", payload["error"])
		}
	})

	// 3. Contract: 403 Forbidden
	t.Run("403 Forbidden Contract", func(t *testing.T) {
		disallowedCfg := &config.Config{
			Port:             "28280",
			PolicyPath:       policyPath,
			AllowRemoteAdmin: false,
			AdminToken:       "",
		}
		disallowedApp, _ := NewApp(disallowedCfg, "https://api.github.com")

		req := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		rec := httptest.NewRecorder()
		disallowedApp.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d", rec.Code)
		}
		var payload map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if payload["error"] != "forbidden: remote admin is disabled" {
			t.Errorf("expected error 'forbidden: remote admin is disabled', got %q", payload["error"])
		}
	})

	// 4. Contract: 400 Bad Request on Corrupt Config
	t.Run("400 Bad Request Contract", func(t *testing.T) {
		tmpPolicy, err := os.CreateTemp("", "bad-policy-*.yaml")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpPolicy.Name())
		_, _ = tmpPolicy.WriteString("internal_rules:\n  - name: ok\n    repo: o/*\n    methods: [GET]\n")
		_ = tmpPolicy.Close()

		errCfg := &config.Config{
			Port:             "28280",
			PolicyPath:       tmpPolicy.Name(),
			AllowRemoteAdmin: true,
			AdminToken:       "super-secret-reload-token",
		}
		errApp, _ := NewApp(errCfg, "https://api.github.com")

		// Corrupt file
		_ = os.WriteFile(tmpPolicy.Name(), []byte("invalid: yaml: ["), 0644)

		req := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("Authorization", "Bearer super-secret-reload-token")
		rec := httptest.NewRecorder()
		errApp.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
		}
		var payload map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if payload["status"] != "error" {
			t.Errorf("expected status 'error', got %q", payload["status"])
		}
		if payload["config"] != "previous configuration preserved" {
			t.Errorf("expected config 'previous configuration preserved', got %q", payload["config"])
		}
		if payload["error"] == "" {
			t.Errorf("expected non-empty error message")
		}
	})
}

func TestEndToEnd_ConcurrentReloadSerialization(t *testing.T) {
	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Port:             "28280",
		PolicyPath:       policyPath,
		AllowRemoteAdmin: true,
		AdminToken:       "token-concurrent",
		AuthKeys:         []string{"key1", "key2"},
	}

	app, err := NewApp(cfg, "https://api.github.com")
	if err != nil {
		t.Fatal(err)
	}

	concurrency := 10
	done := make(chan int, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/_gateway/reload", nil)
			req.RemoteAddr = "10.0.0.1:54321"
			req.Header.Set("Authorization", "Bearer token-concurrent")
			rec := httptest.NewRecorder()
			app.Handler.ServeHTTP(rec, req)
			done <- rec.Code
		}()
	}

	for i := 0; i < concurrency; i++ {
		code := <-done
		if code != http.StatusOK {
			t.Errorf("expected concurrent reload to return 200 OK, got %d", code)
		}
	}
}

func TestEndToEnd_ConcurrentReloadAndReconciler_NoDeadlock(t *testing.T) {
	var tick int32
	mockPolicyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&tick, 1)
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		if n%2 == 0 {
			_, _ = w.Write([]byte("internal_rules:\n  - name: r-even\n    repo: even/*\n    methods: [GET]\n"))
		} else {
			_, _ = w.Write([]byte("internal_rules:\n  - name: r-odd\n    repo: odd/*\n    methods: [GET]\n"))
		}
	}))
	defer mockPolicyServer.Close()

	policyURL := mockPolicyServer.URL + "/policy.yaml"
	t.Setenv("GITHUB_API_GATEWAY_POLICY_PATH", policyURL)
	t.Setenv("GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL", "2ms")

	cfg := &config.Config{
		Port:               "28280",
		PolicyPath:         policyURL,
		PolicyPollInterval: 2 * time.Millisecond,
		AuthKeys:           []string{"key1"},
	}

	app, err := NewApp(cfg, "https://api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 15; i++ {
			time.Sleep(2 * time.Millisecond)
			if _, reloadErr := app.Reload(); reloadErr != nil {
				done <- reloadErr
				return
			}
		}
		done <- nil
	}()

	select {
	case reloadErr := <-done:
		if reloadErr != nil {
			t.Fatalf("unexpected error during concurrent reload: %v", reloadErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DEADLOCK detected: concurrent App.Reload() and Reconciler hung for >3s")
	}
}

func TestEndToEnd_ReloadPreservesCLIOverrides(t *testing.T) {
	policyPath, err := filepath.Abs("../../policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Port:              "28280",
		PolicyPath:        policyPath,
		TokenMinRemaining: 1500,
		ClientMaxRPM:      240,
		ClientBurst:       25,
	}

	app, err := NewApp(cfg, "https://api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	app.ApplyOverrides = func(c *config.Config) {
		c.PolicyPath = policyPath
		c.TokenMinRemaining = 1500
		c.ClientMaxRPM = 240
		c.ClientBurst = 25
	}

	if _, err := app.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if app.Config.ClientMaxRPM != 240 || app.Config.ClientBurst != 25 || app.Config.TokenMinRemaining != 1500 {
		t.Errorf("expected CLI overrides preserved after Reload(), got ClientMaxRPM=%d ClientBurst=%d TokenMinRemaining=%d",
			app.Config.ClientMaxRPM, app.Config.ClientBurst, app.Config.TokenMinRemaining)
	}
}
