package telemetry

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github-api-gateway/internal/token"
)

// DefaultHistogramBuckets defines standard HTTP request duration buckets in seconds.
var DefaultHistogramBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// GaugeSources holds callbacks to query dynamic subsystem state at scrape time.
type GaugeSources struct {
	ActiveAuthKeys func() int
	PolicyRules    func() int
	CacheEntries   func() int
	ActiveBuckets  func() int
	TokenStatuses  func() []token.TokenInfo
}

type reqKey struct {
	method string
	status string
}

// Registry maintains thread-safe Prometheus counters, histograms, and dynamic gauge sources.
type Registry struct {
	startTime time.Time
	sources   GaugeSources

	reqMu       sync.RWMutex
	reqCounters map[reqKey]*atomic.Uint64

	histMu         sync.Mutex
	histBuckets    []uint64 // non-cumulative counts per bucket
	histCount      uint64
	histSumSeconds float64

	authAllow  atomic.Uint64
	authReject atomic.Uint64

	ratelimitAllow    atomic.Uint64
	ratelimitThrottle atomic.Uint64

	cacheRevalidated atomic.Uint64
	cacheMiss        atomic.Uint64
	cacheBypass      atomic.Uint64

	policyAllow atomic.Uint64
	policyDeny  atomic.Uint64
}

// NewRegistry creates a new thread-safe Prometheus metrics registry.
func NewRegistry(startTime time.Time, sources GaugeSources) *Registry {
	return &Registry{
		startTime:   startTime,
		sources:     sources,
		reqCounters: make(map[reqKey]*atomic.Uint64),
		histBuckets: make([]uint64, len(DefaultHistogramBuckets)),
	}
}

// ObserveRequest records an HTTP request's method, status code, and duration.
func (r *Registry) ObserveRequest(method string, statusCode int, duration time.Duration) {
	if method == "" {
		method = "UNKNOWN"
	}
	statusStr := strconv.Itoa(statusCode)
	key := reqKey{method: method, status: statusStr}

	r.reqMu.RLock()
	counter, ok := r.reqCounters[key]
	r.reqMu.RUnlock()

	if !ok {
		r.reqMu.Lock()
		counter, ok = r.reqCounters[key]
		if !ok {
			counter = &atomic.Uint64{}
			r.reqCounters[key] = counter
		}
		r.reqMu.Unlock()
	}
	counter.Add(1)

	secs := duration.Seconds()
	if secs < 0 {
		secs = 0
	}

	r.histMu.Lock()
	r.histCount++
	r.histSumSeconds += secs
	for i, bound := range DefaultHistogramBuckets {
		if secs <= bound {
			r.histBuckets[i]++
			break
		}
	}
	r.histMu.Unlock()
}

// RecordAuthDecision increments the gateway authentication decision counter.
func (r *Registry) RecordAuthDecision(allowed bool) {
	if allowed {
		r.authAllow.Add(1)
	} else {
		r.authReject.Add(1)
	}
}

// RecordRateLimitDecision increments the per-client rate limiter decision counter.
func (r *Registry) RecordRateLimitDecision(allowed bool) {
	if allowed {
		r.ratelimitAllow.Add(1)
	} else {
		r.ratelimitThrottle.Add(1)
	}
}

// RecordCacheOutcome increments the ETag cache outcome counter ("revalidated", "miss", "bypass").
func (r *Registry) RecordCacheOutcome(result string) {
	switch result {
	case "revalidated":
		r.cacheRevalidated.Add(1)
	case "miss":
		r.cacheMiss.Add(1)
	case "bypass":
		r.cacheBypass.Add(1)
	}
}

// RecordPolicyDecision increments the enterprise policy decision counter.
func (r *Registry) RecordPolicyDecision(allowed bool) {
	if allowed {
		r.policyAllow.Add(1)
	} else {
		r.policyDeny.Add(1)
	}
}

// NewMetricsHandler returns an http.Handler serving Prometheus text exposition format 0.0.4.
func NewMetricsHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var sb strings.Builder

		// 1. Uptime gauge
		uptime := time.Since(reg.startTime).Seconds()
		if uptime < 0 {
			uptime = 0
		}
		sb.WriteString("# HELP github_api_gateway_uptime_seconds Gateway uptime in seconds.\n")
		sb.WriteString("# TYPE github_api_gateway_uptime_seconds gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_uptime_seconds %.3f\n", uptime)

		// 2. Requests total counter
		sb.WriteString("# HELP github_api_gateway_requests_total Total HTTP requests processed by the gateway.\n")
		sb.WriteString("# TYPE github_api_gateway_requests_total counter\n")
		type reqSample struct {
			key reqKey
			val uint64
		}
		reg.reqMu.RLock()
		samples := make([]reqSample, 0, len(reg.reqCounters))
		for k, c := range reg.reqCounters {
			samples = append(samples, reqSample{key: k, val: c.Load()})
		}
		reg.reqMu.RUnlock()
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].key.method != samples[j].key.method {
				return samples[i].key.method < samples[j].key.method
			}
			return samples[i].key.status < samples[j].key.status
		})
		for _, s := range samples {
			fmt.Fprintf(&sb, "github_api_gateway_requests_total{method=%q,status=%q} %d\n", s.key.method, s.key.status, s.val)
		}

		// 3. Request duration histogram
		sb.WriteString("# HELP github_api_gateway_request_duration_seconds HTTP request latency distributions in seconds.\n")
		sb.WriteString("# TYPE github_api_gateway_request_duration_seconds histogram\n")
		reg.histMu.Lock()
		bucketCopy := make([]uint64, len(reg.histBuckets))
		copy(bucketCopy, reg.histBuckets)
		totalCount := reg.histCount
		totalSum := reg.histSumSeconds
		reg.histMu.Unlock()

		var cumulative uint64
		for i, bound := range DefaultHistogramBuckets {
			cumulative += bucketCopy[i]
			fmt.Fprintf(&sb, "github_api_gateway_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(bound, 'f', -1, 64), cumulative)
		}
		fmt.Fprintf(&sb, "github_api_gateway_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", totalCount)
		fmt.Fprintf(&sb, "github_api_gateway_request_duration_seconds_sum %.6f\n", totalSum)
		fmt.Fprintf(&sb, "github_api_gateway_request_duration_seconds_count %d\n", totalCount)

		// 4. Auth decisions & active keys
		sb.WriteString("# HELP github_api_gateway_auth_decisions_total Total client authentication decisions.\n")
		sb.WriteString("# TYPE github_api_gateway_auth_decisions_total counter\n")
		fmt.Fprintf(&sb, "github_api_gateway_auth_decisions_total{decision=\"allow\"} %d\n", reg.authAllow.Load())
		fmt.Fprintf(&sb, "github_api_gateway_auth_decisions_total{decision=\"reject\"} %d\n", reg.authReject.Load())

		activeKeys := 0
		if reg.sources.ActiveAuthKeys != nil {
			activeKeys = reg.sources.ActiveAuthKeys()
		}
		sb.WriteString("# HELP github_api_gateway_active_auth_keys Number of active pre-shared client authentication keys.\n")
		sb.WriteString("# TYPE github_api_gateway_active_auth_keys gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_active_auth_keys %d\n", activeKeys)

		// 5. Rate limit decisions & active buckets
		sb.WriteString("# HELP github_api_gateway_ratelimit_decisions_total Total per-client rate limiter decisions.\n")
		sb.WriteString("# TYPE github_api_gateway_ratelimit_decisions_total counter\n")
		fmt.Fprintf(&sb, "github_api_gateway_ratelimit_decisions_total{decision=\"allow\"} %d\n", reg.ratelimitAllow.Load())
		fmt.Fprintf(&sb, "github_api_gateway_ratelimit_decisions_total{decision=\"throttle\"} %d\n", reg.ratelimitThrottle.Load())

		activeBuckets := 0
		if reg.sources.ActiveBuckets != nil {
			activeBuckets = reg.sources.ActiveBuckets()
		}
		sb.WriteString("# HELP github_api_gateway_ratelimit_active_buckets Current number of active client token buckets.\n")
		sb.WriteString("# TYPE github_api_gateway_ratelimit_active_buckets gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_ratelimit_active_buckets %d\n", activeBuckets)

		// 6. Cache outcomes & entries
		sb.WriteString("# HELP github_api_gateway_cache_events_total Total ETag response cache outcomes.\n")
		sb.WriteString("# TYPE github_api_gateway_cache_events_total counter\n")
		fmt.Fprintf(&sb, "github_api_gateway_cache_events_total{result=\"revalidated\"} %d\n", reg.cacheRevalidated.Load())
		fmt.Fprintf(&sb, "github_api_gateway_cache_events_total{result=\"miss\"} %d\n", reg.cacheMiss.Load())
		fmt.Fprintf(&sb, "github_api_gateway_cache_events_total{result=\"bypass\"} %d\n", reg.cacheBypass.Load())

		cacheEntries := 0
		if reg.sources.CacheEntries != nil {
			cacheEntries = reg.sources.CacheEntries()
		}
		sb.WriteString("# HELP github_api_gateway_cache_entries Current number of unexpired ETag cache entries.\n")
		sb.WriteString("# TYPE github_api_gateway_cache_entries gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_cache_entries %d\n", cacheEntries)

		// 7. Policy decisions & rules
		sb.WriteString("# HELP github_api_gateway_policy_decisions_total Total enterprise access policy decisions.\n")
		sb.WriteString("# TYPE github_api_gateway_policy_decisions_total counter\n")
		fmt.Fprintf(&sb, "github_api_gateway_policy_decisions_total{decision=\"allow\"} %d\n", reg.policyAllow.Load())
		fmt.Fprintf(&sb, "github_api_gateway_policy_decisions_total{decision=\"deny\"} %d\n", reg.policyDeny.Load())

		policyRules := 0
		if reg.sources.PolicyRules != nil {
			policyRules = reg.sources.PolicyRules()
		}
		sb.WriteString("# HELP github_api_gateway_policy_rules Number of active enterprise policy rules.\n")
		sb.WriteString("# TYPE github_api_gateway_policy_rules gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_policy_rules %d\n", policyRules)

		// 8. Token pool gauges (strictly masked token IDs)
		var tokens []token.TokenInfo
		if reg.sources.TokenStatuses != nil {
			tokens = reg.sources.TokenStatuses()
		}
		sb.WriteString("# HELP github_api_gateway_tokens_total Total configured GitHub Personal Access Tokens in pool.\n")
		sb.WriteString("# TYPE github_api_gateway_tokens_total gauge\n")
		fmt.Fprintf(&sb, "github_api_gateway_tokens_total %d\n", len(tokens))

		sb.WriteString("# HELP github_api_gateway_token_remaining Remaining GitHub API rate-limit quota per masked token.\n")
		sb.WriteString("# TYPE github_api_gateway_token_remaining gauge\n")
		for _, tok := range tokens {
			fmt.Fprintf(&sb, "github_api_gateway_token_remaining{token_id=%q} %d\n", tok.MaskedToken, tok.Remaining)
		}

		sb.WriteString("# HELP github_api_gateway_token_in_cooldown Whether the masked token is currently in cooldown (1 = cooldown, 0 = active).\n")
		sb.WriteString("# TYPE github_api_gateway_token_in_cooldown gauge\n")
		for _, tok := range tokens {
			cooldownVal := 0
			if !tok.Eligible {
				cooldownVal = 1
			}
			fmt.Fprintf(&sb, "github_api_gateway_token_in_cooldown{token_id=%q} %d\n", tok.MaskedToken, cooldownVal)
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sb.String()))
	})
}
