package telemetry_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github-api-relay/internal/cache"
	"github-api-relay/internal/policy"
	"github-api-relay/internal/telemetry"
	"github-api-relay/internal/token"
)

func TestStatusHandler_Payload(t *testing.T) {
	pool := token.NewPool([]string{"ghp_1234567890abcdef", "ghp_abcdef1234567890"}, 3000, true)
	c := cache.New(5 * time.Minute)
	c.Set("/test", &cache.Entry{ETag: "tag1", Body: []byte("ok"), CachedAt: time.Now()})

	pol, err := policy.LoadFromBytes([]byte(`
internal_rules:
  - name: test-rule
    repo: "org/a"
    methods: ["GET"]
`))
	if err != nil {
		t.Fatalf("failed to parse policy: %v", err)
	}

	startTime := time.Now().Add(-10 * time.Minute)
	activeKeys := 2

	handler := telemetry.NewStatusHandler(startTime, pool, c, activeKeys, pol)

	req := httptest.NewRequest("GET", "/_relay/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got: %d", rec.Code)
	}

	var resp struct {
		Status          string            `json:"status"`
		Uptime          string            `json:"uptime"`
		UptimeSeconds   float64           `json:"uptime_seconds"`
		ActiveRelayKeys int               `json:"active_relay_keys"`
		CacheEntries    int               `json:"cache_entries"`
		PolicyRules     int               `json:"policy_rules"`
		Tokens          []token.TokenInfo `json:"tokens"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal JSON response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got: %s", resp.Status)
	}
	if resp.UptimeSeconds < 500 {
		t.Errorf("expected uptime_seconds >= 500, got: %f", resp.UptimeSeconds)
	}
	if resp.ActiveRelayKeys != 2 {
		t.Errorf("expected 2 active relay keys, got: %d", resp.ActiveRelayKeys)
	}
	if resp.CacheEntries != 1 {
		t.Errorf("expected 1 cache entry, got: %d", resp.CacheEntries)
	}
	if resp.PolicyRules != 1 {
		t.Errorf("expected 1 policy rule, got: %d", resp.PolicyRules)
	}
	if len(resp.Tokens) != 2 {
		t.Fatalf("expected 2 tokens, got: %d", len(resp.Tokens))
	}
	// Verify tokens are masked
	for _, tok := range resp.Tokens {
		if tok.MaskedToken == "ghp_1234567890abcdef" || tok.MaskedToken == "ghp_abcdef1234567890" {
			t.Errorf("raw token must not be leaked in status: %s", tok.MaskedToken)
		}
	}
}

func TestStatusHandler_MethodNotAllowed(t *testing.T) {
	handler := telemetry.NewStatusHandler(time.Now(), nil, nil, 0, nil)

	req := httptest.NewRequest("POST", "/_relay/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST, got: %d", rec.Code)
	}
}
