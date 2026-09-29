package token

import (
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultLimit is the standard GitHub token quota per hour.
	DefaultLimit = 5000
	// ResetGracePeriod is the buffer we wait after reset time before probing.
	ResetGracePeriod = 10 * time.Second
	// ProbeCooldown is how long we throttle between failed probe attempts.
	ProbeCooldown = 10 * time.Second
)

// TokenInfo holds public status for telemetry.
type TokenInfo struct {
	MaskedToken string    `json:"masked_token"`
	Remaining   int       `json:"remaining"`
	ResetTime   time.Time `json:"reset_time"`
	Eligible    bool      `json:"eligible"`
}

// TokenStatus represents a single token's internal state.
type TokenStatus struct {
	token         string
	remaining     int
	resetTime     time.Time
	nextProbeTime time.Time
}

// Pool manages a collection of GitHub tokens with Largest-Remaining selection.
type Pool struct {
	tokens                  []*TokenStatus
	minRemaining            int
	fallbackUnauthenticated bool
	mu                      sync.RWMutex
}

// NewPool creates a new token pool.
func NewPool(tokens []string, minRemaining int, fallbackUnauthenticated bool) *Pool {
	p := &Pool{
		minRemaining:            minRemaining,
		fallbackUnauthenticated: fallbackUnauthenticated,
	}

	for _, t := range tokens {
		p.tokens = append(p.tokens, &TokenStatus{
			token:     t,
			remaining: DefaultLimit,
			resetTime: time.Now().Add(1 * time.Hour),
		})
	}

	return p
}

// UpdateTokens updates the token pool, preserving existing quota states while adding new tokens and removing deleted ones.
func (p *Pool) UpdateTokens(tokens []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	existing := make(map[string]*TokenStatus)
	for _, t := range p.tokens {
		existing[t.token] = t
	}

	var newTokens []*TokenStatus
	for _, tokStr := range tokens {
		tokStr = strings.TrimSpace(tokStr)
		if tokStr == "" {
			continue
		}
		if prev, ok := existing[tokStr]; ok {
			newTokens = append(newTokens, prev)
		} else {
			newTokens = append(newTokens, &TokenStatus{
				token:     tokStr,
				remaining: DefaultLimit,
				resetTime: time.Now().Add(1 * time.Hour),
			})
		}
	}
	p.tokens = newTokens
}

// Acquire selects the token with the largest remaining quota above minRemaining,
// handles 10-second reset probe cooldowns, and falls back to unauthenticated if configured.
func (p *Pool) Acquire() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If no tokens configured, operate in unauthenticated mode
	if len(p.tokens) == 0 {
		return "", nil
	}

	now := time.Now()
	var bestToken *TokenStatus
	bestQuota := -1

	for _, t := range p.tokens {
		effectiveQuota := -1

		// Case 1: Healthy token above minimum buffer
		if t.remaining >= p.minRemaining {
			effectiveQuota = t.remaining
		} else if now.After(t.resetTime.Add(ResetGracePeriod)) && now.After(t.nextProbeTime) {
			// Case 2: Past reset time + 10s buffer, and past probe cooldown
			effectiveQuota = DefaultLimit
		}

		if effectiveQuota != -1 && (bestToken == nil || effectiveQuota > bestQuota) {
			bestToken = t
			bestQuota = effectiveQuota
		}
	}

	if bestToken != nil {
		return bestToken.token, nil
	}

	// All tokens exhausted
	if p.fallbackUnauthenticated {
		return "", nil // empty string indicates unauthenticated proxying
	}

	return "", errors.New("all GitHub tokens in pool are exhausted below threshold")
}

// RecordUsage updates a token's live quota and reset timestamp from GitHub response headers.
func (p *Pool) RecordUsage(tokenStr string, remaining int, resetTime time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.tokens {
		if t.token == tokenStr {
			wasPastReset := !t.resetTime.IsZero() && time.Now().After(t.resetTime.Add(ResetGracePeriod))
			t.remaining = remaining
			t.resetTime = resetTime

			// If this update arrived AFTER the reset grace period had already passed,
			// and GitHub still reported remaining < minRemaining,
			// then GitHub's rollover is lagging. Throttle next probe.
			now := time.Now()
			if wasPastReset && remaining < p.minRemaining && now.After(resetTime) {
				t.nextProbeTime = now.Add(ProbeCooldown)
			}
			return
		}
	}
}

// Status returns telemetry info for all tokens in the pool without leaking full secrets.
func (p *Pool) Status() []TokenInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	var infos []TokenInfo

	for _, t := range p.tokens {
		eligible := t.remaining >= p.minRemaining || (now.After(t.resetTime.Add(ResetGracePeriod)) && now.After(t.nextProbeTime))
		infos = append(infos, TokenInfo{
			MaskedToken: maskToken(t.token),
			Remaining:   t.remaining,
			ResetTime:   t.resetTime,
			Eligible:    eligible,
		})
	}

	return infos
}

func maskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "..." + token[len(token)-4:]
}
