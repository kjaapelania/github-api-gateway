package policy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github-api-gateway/internal/policy"
)

func TestFetcher_LocalFile(t *testing.T) {
	tmp, err := os.CreateTemp("", "policy-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	content1 := []byte("internal_rules:\n  - name: test\n    repo: org/*\n    methods: [GET]\n")
	if _, err := tmp.Write(content1); err != nil {
		t.Fatal(err)
	}
	_ = tmp.Close()

	src, err := policy.NewPolicySource(tmp.Name())
	if err != nil {
		t.Fatalf("failed to create policy source: %v", err)
	}
	if src.IsRemote {
		t.Fatalf("expected local source, got IsRemote=true")
	}

	fetcher := policy.NewFetcher(nil)
	data, modified, err := fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !modified {
		t.Fatalf("expected modified=true on first fetch")
	}
	if string(data) != string(content1) {
		t.Fatalf("content mismatch: got %s, want %s", string(data), string(content1))
	}

	// Second fetch without file modification should return modified=false
	data2, modified2, err := fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modified2 {
		t.Fatalf("expected modified=false on unchanged local file")
	}
	if data2 != nil {
		t.Fatalf("expected nil data when modified=false, got %v", data2)
	}
}

func TestFetcher_RemoteHTTP(t *testing.T) {
	yamlContent := []byte("internal_rules:\n  - name: remote-test\n    repo: remote/*\n    methods: [GET]\n")
	mockServer := NewMockPolicyServer(yamlContent)
	defer mockServer.Close()

	src, err := policy.NewPolicySource(mockServer.URL + "/policy.yaml")
	if err != nil {
		t.Fatalf("failed to create remote source: %v", err)
	}
	if !src.IsRemote {
		t.Fatalf("expected remote source, got IsRemote=false")
	}

	fetcher := policy.NewFetcher(nil)
	data, modified, err := fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	if !modified {
		t.Fatalf("expected modified=true on first remote fetch")
	}
	if string(data) != string(yamlContent) {
		t.Fatalf("content mismatch: got %s, want %s", string(data), string(yamlContent))
	}
	if src.LastETag == "" {
		t.Fatalf("expected LastETag to be populated from server")
	}
}

func TestFetcher_RemoteHTTPErrors(t *testing.T) {
	mockServer := NewMockPolicyServer([]byte("dummy"))
	mockServer.SetStatusCode(http.StatusNotFound)
	defer mockServer.Close()

	src, err := policy.NewPolicySource(mockServer.URL + "/notfound.yaml")
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)
	_, _, err = fetcher.Fetch(context.Background(), src)
	if err == nil {
		t.Fatalf("expected error for 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected error to mention 404, got: %v", err)
	}
}

func TestFetcher_Exceeds10MBLimit(t *testing.T) {
	// Server serving slightly more than 10MB
	hugeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1024*1024) // 1MB
		for i := 0; i < 11; i++ {        // 11MB total
			_, _ = w.Write(chunk)
		}
	}))
	defer hugeServer.Close()

	src, err := policy.NewPolicySource(hugeServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)
	_, _, err = fetcher.Fetch(context.Background(), src)
	if err == nil {
		t.Fatalf("expected error for payload exceeding 10MB, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Errorf("expected error message to mention 10MB limit, got: %v", err)
	}
}

func TestFetcher_ContextTimeout(t *testing.T) {
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal_rules: []\n"))
	}))
	defer slowServer.Close()

	src, err := policy.NewPolicySource(slowServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err = fetcher.Fetch(ctx, src)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

func TestFetcher_ETagAndHashRevalidation(t *testing.T) {
	initialYAML := []byte("internal_rules:\n  - name: test1\n    repo: org/*\n    methods: [GET]\n")
	mockServer := NewMockPolicyServer(initialYAML)
	defer mockServer.Close()

	src, err := policy.NewPolicySource(mockServer.URL + "/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	fetcher := policy.NewFetcher(nil)

	// Fetch 1: Initial fetch (200 OK)
	data1, mod1, err := fetcher.Fetch(context.Background(), src)
	if err != nil || !mod1 || string(data1) != string(initialYAML) {
		t.Fatalf("fetch 1 failed: mod=%v, err=%v", mod1, err)
	}
	etag1 := src.LastETag
	if etag1 == "" {
		t.Fatal("expected LastETag to be populated")
	}

	// Fetch 2: Revalidation -> server sees matching If-None-Match and returns 304 Not Modified
	data2, mod2, err := fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("fetch 2 failed: %v", err)
	}
	if mod2 {
		t.Fatalf("expected mod=false on 304 Not Modified")
	}
	if data2 != nil {
		t.Fatalf("expected data=nil on 304 Not Modified, got %s", string(data2))
	}

	// Fetch 3: Upstream updates content -> server returns 200 OK with new ETag
	updatedYAML := []byte("internal_rules:\n  - name: test2\n    repo: org/*\n    methods: [GET, POST]\n")
	mockServer.SetContent(updatedYAML, "")

	data3, mod3, err := fetcher.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("fetch 3 failed: %v", err)
	}
	if !mod3 {
		t.Fatalf("expected mod=true when content changed")
	}
	if string(data3) != string(updatedYAML) {
		t.Fatalf("expected updated content, got: %s", string(data3))
	}
	if src.LastETag == etag1 {
		t.Errorf("expected LastETag to update after content change")
	}
}
