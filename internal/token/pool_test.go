package token_test

import (
	"testing"
	"time"

	"github-api-relay/internal/token"
)

func TestTokenPool_LargestRemainingSelection(t *testing.T) {
	tokens := []string{"token-a", "token-b", "token-c"}
	pool := token.NewPool(tokens, 3000, true)

	// Simulate initial remaining counts
	pool.RecordUsage("token-a", 3500, time.Now().Add(1*time.Hour))
	pool.RecordUsage("token-b", 4800, time.Now().Add(1*time.Hour))
	pool.RecordUsage("token-c", 4100, time.Now().Add(1*time.Hour))

	// Should pick token-b because 4800 is the largest
	tok, err := pool.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "token-b" {
		t.Errorf("expected token-b (4800), got: %s", tok)
	}

	// Update token-b so it drops to 4000
	pool.RecordUsage("token-b", 4000, time.Now().Add(1*time.Hour))

	// Now token-c (4100) is the largest
	tok, err = pool.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "token-c" {
		t.Errorf("expected token-c (4100), got: %s", tok)
	}
}

func TestTokenPool_ExclusionBelowThreshold(t *testing.T) {
	tokens := []string{"token-a", "token-b"}
	pool := token.NewPool(tokens, 3000, true)

	// token-a drops to 2999 (< 3000 threshold)
	pool.RecordUsage("token-a", 2999, time.Now().Add(1*time.Hour))
	// token-b has 3200 (>= 3000 threshold)
	pool.RecordUsage("token-b", 3200, time.Now().Add(1*time.Hour))

	// Should select token-b
	tok, err := pool.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "token-b" {
		t.Errorf("expected token-b, got %s", tok)
	}
}

func TestTokenPool_TenSecondResetAndCooldown(t *testing.T) {
	tokens := []string{"token-a"}
	pool := token.NewPool(tokens, 3000, false)

	pastReset := time.Now().Add(-15 * time.Second) // 15 seconds ago (> 10s buffer)
	pool.RecordUsage("token-a", 2900, pastReset)

	// Since > 10s has passed, token-a is eligible for an inline probe
	tok, err := pool.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "token-a" {
		t.Errorf("expected token-a to be probed, got %s", tok)
	}

	// Suppose GitHub was delayed and still returns 2900 with old reset timestamp
	pool.RecordUsage("token-a", 2900, pastReset)

	// Now it must be on 10-second cooldown, and since fallback is false, Acquire must fail
	_, err = pool.Acquire()
	if err == nil {
		t.Error("expected error during cooldown when all tokens exhausted, got nil")
	}
}

func TestTokenPool_UnauthenticatedFallback(t *testing.T) {
	tokens := []string{"token-a"}
	// With fallback enabled
	poolWithFallback := token.NewPool(tokens, 3000, true)
	poolWithFallback.RecordUsage("token-a", 1000, time.Now().Add(1*time.Hour))

	tok, err := poolWithFallback.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty string for unauthenticated fallback, got %q", tok)
	}

	// With fallback disabled
	poolNoFallback := token.NewPool(tokens, 3000, false)
	poolNoFallback.RecordUsage("token-a", 1000, time.Now().Add(1*time.Hour))

	_, err = poolNoFallback.Acquire()
	if err == nil {
		t.Error("expected error when fallback is disabled and tokens exhausted, got nil")
	}
}

func TestTokenPool_EmptyTokens(t *testing.T) {
	pool := token.NewPool(nil, 3000, true)
	tok, err := pool.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty string for empty token list, got %q", tok)
	}
}

func TestTokenPool_UpdateTokens(t *testing.T) {
	pool := token.NewPool([]string{"token-1"}, 3000, true)
	pool.RecordUsage("token-1", 4500, time.Now().Add(1*time.Hour))

	// Update with token-1 kept, token-2 added
	pool.UpdateTokens([]string{"token-1", "token-2"})

	status := pool.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 tokens, got: %d", len(status))
	}
	// token-1 should have preserved its remaining quota (4500)
	if status[0].Remaining != 4500 {
		t.Errorf("expected token-1 remaining=4500, got: %d", status[0].Remaining)
	}
	// token-2 should be newly initialized at 5000
	if status[1].Remaining != 5000 {
		t.Errorf("expected token-2 remaining=5000, got: %d", status[1].Remaining)
	}
}
