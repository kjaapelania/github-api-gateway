package ratelimit_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github-api-gateway/internal/ratelimit"
)

func TestLimiter_Disabled(t *testing.T) {
	// maxRPM <= 0 means disabled
	limiter := ratelimit.NewLimiter(0, 0)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK when limiter is disabled, got %d on request %d", rec.Code, i)
		}
	}
}

func TestLimiter_RateLimitingPerKey(t *testing.T) {
	// 60 RPM with burst of 2
	limiter := ratelimit.NewLimiter(60, 2)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Client 1 sends 2 quick requests (burst allows 2)
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer client-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for request %d, got %d", i, rec.Code)
		}
	}

	// Client 1 sends a 3rd immediate request -> should be throttled (429)
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.Header.Set("Authorization", "Bearer client-1")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for client-1 exceeding rate limit, got %d", rec3.Code)
	}

	if rec3.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header in 429 response")
	}

	var errPayload map[string]any
	if err := json.NewDecoder(rec3.Body).Decode(&errPayload); err != nil {
		t.Fatalf("failed to decode JSON error: %v", err)
	}
	if errPayload["error"] != "client_rate_limited" {
		t.Errorf("expected error=client_rate_limited, got: %v", errPayload["error"])
	}

	// Client 2 with different key should NOT be blocked (independent bucket!)
	reqClient2 := httptest.NewRequest("GET", "/test", nil)
	reqClient2.Header.Set("Authorization", "Bearer client-2")
	recClient2 := httptest.NewRecorder()
	handler.ServeHTTP(recClient2, reqClient2)

	if recClient2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for independent client-2, got %d", recClient2.Code)
	}
}

func TestLimiter_XRelayKeyHeader(t *testing.T) {
	limiter := ratelimit.NewLimiter(60, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.Header.Set("X-Relay-Key", "key-custom")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for first request, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Relay-Key", "key-custom")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for second request, got %d", rec2.Code)
	}
}

func TestLimiter_RefillOverTime(t *testing.T) {
	// High rate: 600 RPM (10 tokens/sec), burst 1
	limiter := ratelimit.NewLimiter(600, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1st request uses the 1 burst token
	req1 := httptest.NewRequest("GET", "/test", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	// 2nd immediate request gets 429
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req1)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec2.Code)
	}

	// Wait 150ms (refills > 1 token at 10 tokens/sec)
	time.Sleep(150 * time.Millisecond)

	// 3rd request should now succeed
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req1)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 after token refill, got %d", rec3.Code)
	}
}

func TestLimiter_XGatewayKeyHeader(t *testing.T) {
	limiter := ratelimit.NewLimiter(60, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.Header.Set("X-Gateway-Key", "gateway-client-1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for first request, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Gateway-Key", "gateway-client-1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for second request, got %d", rec2.Code)
	}
}

func TestLimiter_UpdateLimits(t *testing.T) {
	limiter := ratelimit.NewLimiter(60, 1)
	rpm, burst := limiter.Limits()
	if rpm != 60 || burst != 1 {
		t.Fatalf("expected 60, 1; got %d, %d", rpm, burst)
	}

	limiter.UpdateLimits(120, 20)
	rpm, burst = limiter.Limits()
	if rpm != 120 || burst != 20 {
		t.Fatalf("expected 120, 20; got %d, %d", rpm, burst)
	}
}

