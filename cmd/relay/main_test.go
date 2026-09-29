package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github-api-relay/internal/config"
	"github-api-relay/internal/token"
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

	cfg := &config.Config{
		Port:                    "8080",
		GitHubTokens:            []string{"ghp_testToken1234567890"},
		RelayAuthKeys:           []string{"key-ci", "key-team-a"},
		PolicyPath:              policyPath,
		TokenMinRemaining:       3000,
		FallbackUnauthenticated: true,
		CacheTTLSeconds:         300,
	}

	app, err := NewApp(cfg, mockGitHub.URL)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	relayServer := httptest.NewServer(app.Handler)
	defer relayServer.Close()

	client := relayServer.Client()

	// -------------------------------------------------------------------------
	// Scenario 1: Telemetry endpoint GET /_relay/status
	// -------------------------------------------------------------------------
	t.Run("Telemetry Status Endpoint", func(t *testing.T) {
		res, err := client.Get(relayServer.URL + "/_relay/status")
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
	// Scenario 2: Relay Key Authentication (User Story 3)
	// -------------------------------------------------------------------------
	t.Run("Relay Key Authentication", func(t *testing.T) {
		// Missing key -> 401 Unauthorized
		req, _ := http.NewRequest("GET", relayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for missing key, got %d", res.StatusCode)
		}

		// Wrong key -> 401 Unauthorized
		req, _ = http.NewRequest("GET", relayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
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
		// Hardcoded denylist: /user must return 403 Forbidden even with valid relay key
		req, _ := http.NewRequest("GET", relayServer.URL+"/user", nil)
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
		req, _ = http.NewRequest("GET", relayServer.URL+"/repos/unauthorized-org/secret/releases", nil)
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
		req1, _ := http.NewRequest("GET", relayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
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

		// Call 2: Revalidate -> upstream returns 304, relay serves 200 OK with cached body
		req2, _ := http.NewRequest("GET", relayServer.URL+"/repos/my-company/frontend/releases/latest", nil)
		req2.Header.Set("X-Relay-Key", "key-team-a") // verify alternative header works
		res2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("req2 failed: %v", err)
		}
		body2, _ := io.ReadAll(res2.Body)
		res2.Body.Close()

		if res2.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from relay on 304 revalidation, got %d", res2.StatusCode)
		}
		if string(body2) != `{"tag_name": "v1.2.3"}` {
			t.Errorf("expected cached body2, got: %s", string(body2))
		}
	})

	// -------------------------------------------------------------------------
	// Scenario 5: S3 Binary Redirect Preservation (User Story 6)
	// -------------------------------------------------------------------------
	t.Run("S3 Binary Redirect Preservation", func(t *testing.T) {
		// Disable following redirects in client to assert the 302 response directly
		noRedirectClient := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		req, _ := http.NewRequest("GET", relayServer.URL+"/repos/my-company/frontend/releases/download/v1.0.0/asset.tar.gz", nil)
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
}
