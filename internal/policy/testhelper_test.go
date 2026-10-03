package policy_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// MockPolicyServer provides an HTTP test server simulating remote policy endpoints with ETag support.
type MockPolicyServer struct {
	*httptest.Server
	mu          sync.Mutex
	content     []byte
	etag        string
	statusCode  int
	callCount   int
	lastReqETag string
}

// NewMockPolicyServer starts an in-memory HTTP server serving the given initial YAML content.
func NewMockPolicyServer(initialContent []byte) *MockPolicyServer {
	ms := &MockPolicyServer{
		content:    initialContent,
		statusCode: http.StatusOK,
	}
	ms.etag = fmt.Sprintf("\"%x\"", sha256.Sum256(initialContent))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		defer ms.mu.Unlock()

		ms.callCount++
		ms.lastReqETag = r.Header.Get("If-None-Match")

		if ms.statusCode != http.StatusOK {
			w.WriteHeader(ms.statusCode)
			return
		}

		if ms.etag != "" && ms.lastReqETag == ms.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if ms.etag != "" {
			w.Header().Set("ETag", ms.etag)
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ms.content)
	})

	ms.Server = httptest.NewServer(handler)
	return ms
}

// SetContent dynamically updates the served content and generates a new ETag.
func (ms *MockPolicyServer) SetContent(newContent []byte, newETag string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.content = newContent
	if newETag == "" {
		ms.etag = fmt.Sprintf("\"%x\"", sha256.Sum256(newContent))
	} else {
		ms.etag = newETag
	}
}

// SetStatusCode allows simulating upstream HTTP errors (e.g. 500, 404).
func (ms *MockPolicyServer) SetStatusCode(code int) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.statusCode = code
}

// CallCount returns total number of requests received by the mock server.
func (ms *MockPolicyServer) CallCount() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.callCount
}
