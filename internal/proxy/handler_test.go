package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github-api-gateway/internal/policy"
	"github-api-gateway/internal/proxy"
	"github-api-gateway/internal/token"
)

func TestProxyHandler_PolicyBlocking(t *testing.T) {
	// Policy only allows GET /repos/allowed-org/**
	policyYAML := `
internal_rules:
  - name: "allowed-rule"
    repo: "allowed-org/*"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(policyYAML))
	if err != nil {
		t.Fatalf("unexpected error loading policy: %v", err)
	}

	upstreamHit := false
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mockUpstream.Close()

	pool := token.NewPool([]string{"ghp_token"}, 3000, true)
	handler := proxy.NewHandler(mockUpstream.URL, pol, pool)

	// Blocked request: unlisted repo
	req := httptest.NewRequest("GET", "/repos/forbidden-org/secret", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got: %d", rec.Code)
	}
	if upstreamHit {
		t.Error("expected upstream to NOT be contacted for blocked request")
	}

	// Blocked request: sensitive path
	recSensitive := httptest.NewRecorder()
	handler.ServeHTTP(recSensitive, httptest.NewRequest("GET", "/user", nil))
	if recSensitive.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for /user, got: %d", recSensitive.Code)
	}
}

func TestProxyHandler_TokenInjectionAndHeaderStripping(t *testing.T) {
	policyYAML := `
internal_rules:
  - name: "allow-all-get"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(policyYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var capturedAuth string
	var capturedUserAgent string
	var capturedGatewayKey string
	var capturedRelayKey string
	resetTimestamp := strconv.FormatInt(time.Now().Add(1*time.Hour).Unix(), 10)

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedUserAgent = r.Header.Get("User-Agent")
		capturedGatewayKey = r.Header.Get("X-Gateway-Key")
		capturedRelayKey = r.Header.Get("X-Relay-Key")

		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4950")
		w.Header().Set("X-RateLimit-Reset", resetTimestamp)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer mockUpstream.Close()

	pool := token.NewPool([]string{"ghp_secret_token_123"}, 3000, true)
	handler := proxy.NewHandler(mockUpstream.URL, pol, pool)

	// Client sends a fake Authorization header and gateway keys that should be stripped
	req := httptest.NewRequest("GET", "/repos/my-org/app", nil)
	req.Header.Set("Authorization", "Bearer client-fake-token")
	req.Header.Set("X-Gateway-Key", "secret-gateway-key")
	req.Header.Set("X-Relay-Key", "secret-relay-key")
	req.Header.Set("User-Agent", "my-test-agent")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", rec.Code)
	}

	expectedAuth := "Bearer ghp_secret_token_123"
	if capturedAuth != expectedAuth {
		t.Errorf("expected upstream Authorization=%q, got: %q", expectedAuth, capturedAuth)
	}
	if capturedGatewayKey != "" {
		t.Errorf("expected X-Gateway-Key to be stripped before upstream, got: %q", capturedGatewayKey)
	}
	if capturedRelayKey != "" {
		t.Errorf("expected X-Relay-Key to be stripped before upstream, got: %q", capturedRelayKey)
	}

	if capturedUserAgent != "my-test-agent" {
		t.Errorf("expected User-Agent preserved, got: %s", capturedUserAgent)
	}

	// Verify upstream rate-limit headers propagated to client
	if rec.Header().Get("X-RateLimit-Remaining") != "4950" {
		t.Errorf("expected X-RateLimit-Remaining=4950, got: %s", rec.Header().Get("X-RateLimit-Remaining"))
	}

	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"hello":"world"}` {
		t.Errorf("expected body %q, got: %q", `{"hello":"world"}`, string(body))
	}

	// Verify that token pool recorded the usage
	status := pool.Status()
	if len(status) != 1 || status[0].Remaining != 4950 {
		t.Errorf("expected token pool status to reflect remaining=4950, got: %+v", status)
	}
}

func TestProxyHandler_UnauthenticatedFallback(t *testing.T) {
	policyYAML := `
internal_rules:
  - name: "allow-all"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(policyYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var capturedAuth string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("X-RateLimit-Remaining", "55")
		w.WriteHeader(http.StatusOK)
	}))
	defer mockUpstream.Close()

	// Empty token pool with fallback enabled
	pool := token.NewPool(nil, 3000, true)
	handler := proxy.NewHandler(mockUpstream.URL, pol, pool)

	req := httptest.NewRequest("GET", "/repos/public/repo", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", rec.Code)
	}
	if capturedAuth != "" {
		t.Errorf("expected no Authorization header for unauthenticated fallback, got: %s", capturedAuth)
	}
}

func TestProxyHandler_DynamicPolicyUpdate(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockUpstream.Close()

	pol1, _ := policy.LoadFromBytes([]byte(`
internal_rules:
  - repo: "org/a"
    methods: ["GET"]
`))
	pol2, _ := policy.LoadFromBytes([]byte(`
internal_rules:
  - repo: "org/b"
    methods: ["GET"]
`))

	h := proxy.NewHandler(mockUpstream.URL, pol1, nil)

	// Under pol1: org/a allowed, org/b blocked
	reqA := httptest.NewRequest("GET", "/repos/org/a/releases", nil)
	recA := httptest.NewRecorder()
	h.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("expected 200 for org/a under pol1, got %d", recA.Code)
	}

	reqB := httptest.NewRequest("GET", "/repos/org/b/releases", nil)
	recB := httptest.NewRecorder()
	h.ServeHTTP(recB, reqB)
	if recB.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for org/b under pol1, got %d", recB.Code)
	}

	// Update to pol2
	h.SetPolicy(pol2)

	// Under pol2: org/a blocked, org/b allowed
	recA2 := httptest.NewRecorder()
	h.ServeHTTP(recA2, reqA)
	if recA2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for org/a under pol2, got %d", recA2.Code)
	}

	recB2 := httptest.NewRecorder()
	h.ServeHTTP(recB2, reqB)
	if recB2.Code != http.StatusOK {
		t.Fatalf("expected 200 for org/b under pol2, got %d", recB2.Code)
	}
}

func TestProxyHandler_RevokedTokenSidelinesToHealthyToken(t *testing.T) {
	pol, _ := policy.LoadFromBytes([]byte(`
internal_rules:
  - methods: ["GET"]
`))

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer ghp_revoked_token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4900")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(1*time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockUpstream.Close()

	pool := token.NewPool([]string{"ghp_revoked_token", "ghp_healthy_token"}, 3000, false)
	// Give ghp_revoked_token slightly higher initial quota so it gets picked first
	pool.RecordUsage("ghp_healthy_token", 4950, time.Now().Add(1*time.Hour))

	h := proxy.NewHandler(mockUpstream.URL, pol, pool)

	// Request 1 hits upstream with ghp_revoked_token -> 401 Unauthorized, sidelines ghp_revoked_token
	req1 := httptest.NewRequest("GET", "/repos/org/repo", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on first request with revoked token, got %d", rec1.Code)
	}

	// Request 2 must automatically select ghp_healthy_token and succeed with 200 OK
	req2 := httptest.NewRequest("GET", "/repos/org/repo", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on second request using healthy token, got %d", rec2.Code)
	}
}

func TestProxyHandler_OnPolicyDecisionHook(t *testing.T) {
	pol, _ := policy.LoadFromBytes([]byte(`
internal_rules:
  - repo: "org/allowed"
    methods: ["GET"]
`))
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockUpstream.Close()

	h := proxy.NewHandler(mockUpstream.URL, pol, nil)
	var allowCount, denyCount int
	h.SetOnPolicyDecision(func(allowed bool) {
		if allowed {
			allowCount++
		} else {
			denyCount++
		}
	})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/repos/org/allowed/releases", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/repos/org/blocked/releases", nil))

	if allowCount != 1 || denyCount != 1 {
		t.Fatalf("expected allow=1, deny=1; got allow=%d, deny=%d", allowCount, denyCount)
	}
}
