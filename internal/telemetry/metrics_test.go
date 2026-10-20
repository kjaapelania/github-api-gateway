package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github-api-gateway/internal/telemetry"
	"github-api-gateway/internal/token"
)

func TestMetricsHandler_MethodNotAllowed(t *testing.T) {
	reg := telemetry.NewRegistry(time.Now(), telemetry.GaugeSources{})
	h := telemetry.NewMetricsHandler(reg)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/_gateway/metrics", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405 Method Not Allowed, got %d", method, rec.Code)
		}
	}
}

func TestMetricsHandler_PrometheusExpositionAndZeroSecretLeakage(t *testing.T) {
	rawToken1 := "ghp_111122223333444455556666777788889999"
	rawToken2 := "ghp_AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHH18WX"
	pool := token.NewPool([]string{rawToken1, rawToken2}, 3000, false)
	pool.RecordUsage(rawToken1, 4800, time.Now().Add(30*time.Minute))
	// Trigger cooldown on rawToken2
	pool.RecordUsage(rawToken2, 100, time.Now().Add(30*time.Minute))

	reg := telemetry.NewRegistry(time.Now().Add(-15*time.Second), telemetry.GaugeSources{
		ActiveAuthKeys: func() int { return 3 },
		PolicyRules:    func() int { return 7 },
		CacheEntries:   func() int { return 12 },
		ActiveBuckets:  func() int { return 4 },
		TokenStatuses:  pool.Status,
	})

	reg.ObserveRequest("GET", 200, 12*time.Millisecond)
	reg.ObserveRequest("GET", 200, 45*time.Millisecond)
	reg.ObserveRequest("GET", 403, 2*time.Millisecond)
	reg.RecordAuthDecision(true)
	reg.RecordAuthDecision(true)
	reg.RecordAuthDecision(false)
	reg.RecordRateLimitDecision(true)
	reg.RecordRateLimitDecision(false)
	reg.RecordCacheOutcome("revalidated")
	reg.RecordCacheOutcome("miss")
	reg.RecordCacheOutcome("bypass")
	reg.RecordPolicyDecision(true)
	reg.RecordPolicyDecision(false)

	h := telemetry.NewMetricsHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/_gateway/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("expected Content-Type text/plain; version=0.0.4; charset=utf-8, got %q", ct)
	}

	body := rec.Body.String()

	// Security check: raw tokens must NEVER appear in metrics output
	if strings.Contains(body, rawToken1) || strings.Contains(body, rawToken2) {
		t.Fatalf("SECURITY VIOLATION: raw token leaked in metrics output:\n%s", body)
	}

	expectedLines := []string{
		`# TYPE github_api_gateway_uptime_seconds gauge`,
		`# TYPE github_api_gateway_requests_total counter`,
		`github_api_gateway_requests_total{method="GET",status="200"} 2`,
		`github_api_gateway_requests_total{method="GET",status="403"} 1`,
		`# TYPE github_api_gateway_request_duration_seconds histogram`,
		`github_api_gateway_request_duration_seconds_bucket{le="0.005"} 1`,
		`github_api_gateway_request_duration_seconds_bucket{le="0.025"} 2`,
		`github_api_gateway_request_duration_seconds_bucket{le="0.05"} 3`,
		`github_api_gateway_request_duration_seconds_bucket{le="+Inf"} 3`,
		`github_api_gateway_request_duration_seconds_count 3`,
		`github_api_gateway_auth_decisions_total{decision="allow"} 2`,
		`github_api_gateway_auth_decisions_total{decision="reject"} 1`,
		`github_api_gateway_active_auth_keys 3`,
		`github_api_gateway_ratelimit_decisions_total{decision="allow"} 1`,
		`github_api_gateway_ratelimit_decisions_total{decision="throttle"} 1`,
		`github_api_gateway_ratelimit_active_buckets 4`,
		`github_api_gateway_cache_events_total{result="revalidated"} 1`,
		`github_api_gateway_cache_events_total{result="miss"} 1`,
		`github_api_gateway_cache_events_total{result="bypass"} 1`,
		`github_api_gateway_cache_entries 12`,
		`github_api_gateway_policy_decisions_total{decision="allow"} 1`,
		`github_api_gateway_policy_decisions_total{decision="deny"} 1`,
		`github_api_gateway_policy_rules 7`,
		`github_api_gateway_tokens_total 2`,
		`github_api_gateway_token_remaining{token_id="ghp_...9999"} 4800`,
		`github_api_gateway_token_in_cooldown{token_id="ghp_...9999"} 0`,
		`github_api_gateway_token_remaining{token_id="ghp_...18WX"} 100`,
		`github_api_gateway_token_in_cooldown{token_id="ghp_...18WX"} 1`,
	}

	for _, line := range expectedLines {
		if !strings.Contains(body, line) {
			t.Errorf("expected metrics output to contain %q, got:\n%s", line, body)
		}
	}
}

func TestMetricsHandler_ConcurrentObservationAndScrape(t *testing.T) {
	reg := telemetry.NewRegistry(time.Now(), telemetry.GaugeSources{})
	h := telemetry.NewMetricsHandler(reg)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				reg.ObserveRequest("GET", 200, time.Duration(j)*time.Millisecond)
				reg.RecordAuthDecision(j%2 == 0)
				reg.RecordRateLimitDecision(j%3 != 0)
				reg.RecordCacheOutcome("revalidated")
				reg.RecordPolicyDecision(true)
				if j%10 == 0 {
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_gateway/metrics", nil))
					if rec.Code != http.StatusOK {
						t.Errorf("expected 200 OK during concurrent scrape, got %d", rec.Code)
					}
				}
			}
		}(i)
	}
	wg.Wait()
}
