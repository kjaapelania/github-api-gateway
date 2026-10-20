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

// Bucket represents a thread-safe token bucket for a single client.
type Bucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	burst      int
	ratePerS   float64
}

// NewBucket creates a new token bucket starting at full burst capacity.
func NewBucket(ratePerS float64, burst int) *Bucket {
	return &Bucket{
		tokens:     float64(burst),
		lastRefill: time.Now(),
		burst:      burst,
		ratePerS:   ratePerS,
	}
}

// Allow attempts to consume 1 token at the current time.
func (b *Bucket) Allow() (bool, int) {
	return b.AllowAt(time.Now())
}

// AllowAt attempts to consume 1 token at a specific time (enabling deterministic virtual time tests).
func (b *Bucket) AllowAt(now time.Time) (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.lastRefill.IsZero() || now.Before(b.lastRefill) {
		b.lastRefill = now
	} else {
		elapsed := now.Sub(b.lastRefill).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(float64(b.burst), b.tokens+elapsed*b.ratePerS)
			b.lastRefill = now
		}
	}

	const epsilon = 1e-9
	if b.tokens >= 1.0-epsilon {
		b.tokens -= 1.0
		if b.tokens < 0 {
			b.tokens = 0
		}
		return true, 0
	}

	if b.ratePerS <= 0 {
		return false, 60
	}

	needed := 1.0 - b.tokens
	retryAfter := int(math.Ceil(needed / b.ratePerS))
	if retryAfter < 1 {
		retryAfter = 1
	}

	return false, retryAfter
}

// updateParameters updates the burst and ratePerS of this bucket.
func (b *Bucket) updateParameters(ratePerS float64, burst int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ratePerS = ratePerS
	b.burst = burst
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
}

// DefaultMaxBuckets is the default maximum number of active client buckets kept in memory.
const DefaultMaxBuckets = 10000

// Limiter provides per-client token-bucket rate limiting to prevent noisy-neighbor quota exhaustion.
type Limiter struct {
	mu         sync.RWMutex
	buckets    map[string]*Bucket
	maxBuckets int
	maxRPM     int
	burst      int
	ratePerS   float64
	onDecision func(allowed bool)
}

// SetOnDecision registers an optional observer callback invoked on each rate-limiting decision.
func (l *Limiter) SetOnDecision(fn func(allowed bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onDecision = fn
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
		buckets:    make(map[string]*Bucket),
		maxBuckets: DefaultMaxBuckets,
		maxRPM:     maxRPM,
		burst:      burst,
		ratePerS:   ratePerS,
	}
}

// SetMaxBuckets sets the maximum number of client buckets stored in memory.
func (l *Limiter) SetMaxBuckets(max int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if max > 0 {
		l.maxBuckets = max
	}
}

// BucketCount returns the current number of active client buckets.
func (l *Limiter) BucketCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.buckets)
}

// UpdateLimits dynamically updates the maxRPM and burst limits across the limiter and active buckets.
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

	// Dynamically update existing client buckets
	for _, b := range l.buckets {
		b.updateParameters(ratePerS, burst)
	}
}

// Limits returns the current maxRPM and burst.
func (l *Limiter) Limits() (int, int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.maxRPM, l.burst
}

// GetBucket returns the Bucket for the specified clientID, creating one if it doesn't exist.
func (l *Limiter) GetBucket(clientID string) *Bucket {
	l.mu.RLock()
	b, exists := l.buckets[clientID]
	l.mu.RUnlock()
	if exists {
		return b
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// Double-checked locking
	b, exists = l.buckets[clientID]
	if !exists {
		if l.maxBuckets > 0 && len(l.buckets) >= l.maxBuckets {
			l.evictOldestBucketLocked()
		}
		b = &Bucket{
			tokens:     float64(l.burst),
			lastRefill: time.Now(),
			burst:      l.burst,
			ratePerS:   l.ratePerS,
		}
		l.buckets[clientID] = b
	}
	return b
}

func (l *Limiter) evictOldestBucketLocked() {
	var oldestKey string
	var oldestTime time.Time
	for k, b := range l.buckets {
		b.mu.Lock()
		t := b.lastRefill
		b.mu.Unlock()
		if oldestKey == "" || t.Before(oldestTime) {
			oldestKey = k
			oldestTime = t
		}
	}
	if oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}

// Allow checks if the given client is allowed to proceed at the current time.
func (l *Limiter) Allow(clientID string) (bool, int) {
	l.mu.RLock()
	maxRPM := l.maxRPM
	l.mu.RUnlock()

	if maxRPM <= 0 {
		return true, 0
	}

	return l.GetBucket(clientID).Allow()
}

// AllowAt checks if the given client is allowed to proceed at a specific time (useful for deterministic tests).
func (l *Limiter) AllowAt(clientID string, now time.Time) (bool, int) {
	l.mu.RLock()
	maxRPM := l.maxRPM
	l.mu.RUnlock()

	if maxRPM <= 0 {
		return true, 0
	}

	return l.GetBucket(clientID).AllowAt(now)
}

// Middleware wraps an http.Handler with per-client rate limiting.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.RLock()
		maxRPM := l.maxRPM
		onDecision := l.onDecision
		l.mu.RUnlock()

		if maxRPM <= 0 {
			if onDecision != nil {
				onDecision(true)
			}
			next.ServeHTTP(w, r)
			return
		}

		clientID := extractClientID(r)
		allowed, retryAfter := l.GetBucket(clientID).Allow()
		if onDecision != nil {
			onDecision(allowed)
		}
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

	// 3. Fallback strictly to direct TCP peer IP (never trust spoofable X-Forwarded-For)
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && ip != "" {
		return ip
	}

	return r.RemoteAddr
}
