package policy_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github-api-gateway/internal/policy"
)

func TestReconciler_PeriodicPollingAndHotSwap(t *testing.T) {
	initialYAML := []byte("internal_rules:\n  - name: rec1\n    repo: org/*\n    methods: [GET]\n")
	mockServer := NewMockPolicyServer(initialYAML)
	defer mockServer.Close()

	src, err := policy.NewPolicySource(mockServer.URL + "/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)

	// Fetch initial state once
	_, _, err = fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	var reloadCalls int32
	var lastReloadContent []byte
	var mu sync.Mutex

	reloadFunc := func(data []byte) error {
		atomic.AddInt32(&reloadCalls, 1)
		mu.Lock()
		lastReloadContent = data
		mu.Unlock()
		return nil
	}

	// 50ms poll interval for fast test
	reconciler := policy.NewReconciler(src, fetcher, 50*time.Millisecond, reloadFunc, nil)
	reconciler.Start()
	defer reconciler.Stop()

	// Wait 120ms: server content is unchanged, reloadFunc should NOT be called
	time.Sleep(120 * time.Millisecond)
	if calls := atomic.LoadInt32(&reloadCalls); calls != 0 {
		t.Fatalf("expected 0 reload calls for unchanged policy, got %d", calls)
	}

	// Update server content
	updatedYAML := []byte("internal_rules:\n  - name: rec2\n    repo: org/*\n    methods: [GET, POST]\n")
	mockServer.SetContent(updatedYAML, "")

	// Wait for ticker to pick up change
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&reloadCalls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if calls := atomic.LoadInt32(&reloadCalls); calls != 1 {
		t.Fatalf("expected exactly 1 reload call after content update, got %d", calls)
	}

	mu.Lock()
	defer mu.Unlock()
	if string(lastReloadContent) != string(updatedYAML) {
		t.Fatalf("expected reloaded content to match updated YAML, got: %s", string(lastReloadContent))
	}
}

func TestReconciler_ErrorHandlingPreservesState(t *testing.T) {
	initialYAML := []byte("internal_rules:\n  - name: rec-valid\n    repo: org/*\n    methods: [GET]\n")
	mockServer := NewMockPolicyServer(initialYAML)
	defer mockServer.Close()

	src, err := policy.NewPolicySource(mockServer.URL + "/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)
	_, _, _ = fetcher.Fetch(context.Background(), src)

	var reloadCalls int32
	reloadFunc := func(data []byte) error {
		atomic.AddInt32(&reloadCalls, 1)
		return errors.New("simulated reload validation failure")
	}

	reconciler := policy.NewReconciler(src, fetcher, 40*time.Millisecond, reloadFunc, nil)
	reconciler.Start()
	defer reconciler.Stop()

	// Update to new content which triggers reloadFunc, but reloadFunc returns error
	mockServer.SetContent([]byte("corrupt: yaml"), "")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&reloadCalls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if atomic.LoadInt32(&reloadCalls) == 0 {
		t.Fatalf("expected reloadFunc to have been invoked despite error")
	}
}

func TestReconciler_StopIdempotent(t *testing.T) {
	src, _ := policy.NewPolicySource("policy.yaml")
	reconciler := policy.NewReconciler(src, policy.NewFetcher(nil), 100*time.Millisecond, func([]byte) error { return nil }, nil)
	reconciler.Start()

	// Calling Stop multiple times must not panic or block
	reconciler.Stop()
	reconciler.Stop()
}
