package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github-api-relay/internal/auth"
)

func TestAuthMiddleware_OpenMode(t *testing.T) {
	// Empty keys: open mode, allows all
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	middleware := auth.NewMiddleware(nil)
	handler := middleware(next)

	req := httptest.NewRequest("GET", "/repos/foo/bar", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK in open mode, got: %d", rec.Code)
	}
	if !nextCalled {
		t.Error("expected next handler to be called")
	}
}

func TestAuthMiddleware_MultiKeyValidation(t *testing.T) {
	validKeys := []string{"key-ci", "key-team-a"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := auth.NewMiddleware(validKeys)
	handler := middleware(next)

	tests := []struct {
		name       string
		authHeader string
		relayKey   string
		wantCode   int
	}{
		{
			name:       "valid via Authorization Bearer key-ci",
			authHeader: "Bearer key-ci",
			wantCode:   http.StatusOK,
		},
		{
			name:       "valid via Authorization Bearer key-team-a",
			authHeader: "Bearer key-team-a",
			wantCode:   http.StatusOK,
		},
		{
			name:     "valid via X-Relay-Key header",
			relayKey: "key-ci",
			wantCode: http.StatusOK,
		},
		{
			name:       "invalid key",
			authHeader: "Bearer wrong-key",
			wantCode:   http.StatusUnauthorized,
		},
		{
			name:     "missing key",
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/repos/foo/bar", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.relayKey != "" {
				req.Header.Set("X-Relay-Key", tt.relayKey)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("expected status %d, got: %d", tt.wantCode, rec.Code)
			}
		})
	}
}
