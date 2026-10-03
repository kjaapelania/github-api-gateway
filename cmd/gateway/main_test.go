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
