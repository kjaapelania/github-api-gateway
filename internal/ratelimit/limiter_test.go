package ratelimit_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestLimiter_XForwardedForHeader(t *testing.T) {
	limiter := ratelimit.NewLimiter(60, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for first request, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for second request with same client IP, got %d", rec2.Code)
	}

	// Different forwarded IP should succeed
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.Header.Set("X-Forwarded-For", "198.51.100.1")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for different client IP, got %d", rec3.Code)
	}
}

func TestLimiter_VirtualTimeRefill(t *testing.T) {
	// Rate: 60 RPM (1 token/sec), burst 1
	limiter := ratelimit.NewLimiter(60, 1)
	now := time.Now()

	// 1st request succeeds
	allowed, _ := limiter.AllowAt("client-vt", now)
	if !allowed {
		t.Fatal("expected 1st request to be allowed")
	}

	// Immediate 2nd request is throttled
	allowed, retryAfter := limiter.AllowAt("client-vt", now)
	if allowed {
		t.Fatal("expected 2nd immediate request to be throttled")
	}
	if retryAfter != 1 {
		t.Fatalf("expected retryAfter 1, got %d", retryAfter)
	}

	// Advance virtual time by 1 second instantly without sleeping!
	future := now.Add(1 * time.Second)
	allowed, _ = limiter.AllowAt("client-vt", future)
	if !allowed {
		t.Fatal("expected request to be allowed after 1s virtual time advancement")
	}
}

func TestBucket_DeterministicRefill(t *testing.T) {
	now := time.Now()
	// Rate: 2.0 tokens/sec, burst: 5
	b := ratelimit.NewBucket(2.0, 5)

	// Consume all 5 burst tokens
	for i := 0; i < 5; i++ {
		allowed, _ := b.AllowAt(now)
		if !allowed {
			t.Fatalf("expected burst token %d to be allowed", i)
		}
	}

	// 6th request is throttled
	allowed, retryAfter := b.AllowAt(now)
	if allowed {
		t.Fatal("expected 6th request to be denied")
	}
	if retryAfter != 1 {
		t.Fatalf("expected retryAfter 1, got %d", retryAfter)
	}

	// Advance virtual time by 2.5 seconds (should refill 5 tokens, clamped at burst 5)
	future := now.Add(2500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		allowed, _ := b.AllowAt(future)
		if !allowed {
			t.Fatalf("expected refilled token %d to be allowed", i)
		}
	}
}

func TestLimiter_ConcurrentFineGrainedClients(t *testing.T) {
	limiter := ratelimit.NewLimiter(600, 10)
	var wg sync.WaitGroup

	// 20 distinct clients issuing requests concurrently without deadlocks or contention
	for c := 0; c < 20; c++ {
		wg.Add(1)
		clientID := "client-" + string(rune('A'+c))
		go func(id string) {
			defer wg.Done()
			for r := 0; r < 5; r++ {
				allowed, _ := limiter.Allow(id)
				if !allowed {
					t.Errorf("expected client %s request %d to be allowed", id, r)
				}
			}
		}(clientID)
	}

	wg.Wait()
}

func TestLimiter_UpdateLimits(t *testing.T) {
	limiter := ratelimit.NewLimiter(60, 1)
	rpm, burst := limiter.Limits()
	if rpm != 60 || burst != 1 {
		t.Fatalf("expected 60, 1; got %d, %d", rpm, burst)
	}

	// Populate a bucket
	limiter.Allow("client-pre")

	// Update limits
	limiter.UpdateLimits(120, 20)
	rpm, burst = limiter.Limits()
	if rpm != 120 || burst != 20 {
		t.Fatalf("expected 120, 20; got %d, %d", rpm, burst)
	}

	// Verify the existing bucket also updated its parameters
	b := limiter.GetBucket("client-pre")
	future := time.Now().Add(10 * time.Second)
	// Bucket can now burst up to 20
	allowedCount := 0
	for i := 0; i < 25; i++ {
		allowed, _ := b.AllowAt(future)
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 20 {
		t.Fatalf("expected updated burst capacity 20, got allowed count %d", allowedCount)
	}
}
