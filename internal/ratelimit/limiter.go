package ratelimit

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// Limiter provides per-client token-bucket rate limiting to prevent noisy-neighbor quota exhaustion.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	maxRPM   int
	burst    int
	ratePerS float64
}

// NewLimiter creates a per-client token-bucket rate limiter.
// maxRPM <= 0 disables rate limiting.
func NewLimiter(maxRPM, burst int) *Limiter {
	if burst <= 0 && maxRPM > 0 {
		burst = int(math.Max(10, float64(maxRPM)/6.0))
	}
	var ratePerS float64
	if maxRPM > 0 {
		ratePerS = float64(maxRPM) / 60.0
	}

	return &Limiter{
		buckets:  make(map[string]*bucket),
		maxRPM:   maxRPM,
		burst:    burst,
		ratePerS: ratePerS,
	}
}

// UpdateLimits dynamically updates the maxRPM and burst limits.
func (l *Limiter) UpdateLimits(maxRPM, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if burst <= 0 && maxRPM > 0 {
		burst = int(math.Max(10, float64(maxRPM)/6.0))
	}
	var ratePerS float64
	if maxRPM > 0 {
		ratePerS = float64(maxRPM) / 60.0
	}

	l.maxRPM = maxRPM
	l.burst = burst
	l.ratePerS = ratePerS
}

// Limits returns the current maxRPM and burst.
func (l *Limiter) Limits() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxRPM, l.burst
}

// Middleware wraps an http.Handler with per-client rate limiting.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		maxRPM := l.maxRPM
		l.mu.Unlock()

		if maxRPM <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		clientID := extractClientID(r)
		allowed, retryAfter := l.allow(clientID)
		if !allowed {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":               "client_rate_limited",
				"message":             "client exceeded maximum request rate limit to protect shared pool",
				"retry_after_seconds": retryAfter,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (l *Limiter) allow(clientID string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, exists := l.buckets[clientID]
	if !exists {
		b = &bucket{
			tokens:     float64(l.burst),
			lastRefill: now,
		}
		l.buckets[clientID] = b
	} else {
		// Refill tokens based on elapsed time
		elapsed := now.Sub(b.lastRefill).Seconds()
		b.tokens = math.Min(float64(l.burst), b.tokens+elapsed*l.ratePerS)
		b.lastRefill = now
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	// Calculate wait time until 1 token is available
	needed := 1.0 - b.tokens
	retryAfter := int(math.Ceil(needed / l.ratePerS))
	if retryAfter < 1 {
		retryAfter = 1
	}

	return false, retryAfter
}

func extractClientID(r *http.Request) string {
	// 1. Check X-Gateway-Key or X-Relay-Key header
	if key := r.Header.Get("X-Gateway-Key"); key != "" {
		return strings.TrimSpace(key)
	}
	if key := r.Header.Get("X-Relay-Key"); key != "" {
		return strings.TrimSpace(key)
	}

	// 2. Check Authorization: Bearer <KEY>
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}

	// 3. Fallback to client IP (in open mode)
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && ip != "" {
		return ip
	}

	return r.RemoteAddr
}
