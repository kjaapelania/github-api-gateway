package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github-api-gateway/internal/auth"
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

func TestKeyManager_DynamicKeyRotation(t *testing.T) {
	mgr := auth.NewManager([]string{"key-v1"})
	handler := mgr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. key-v1 is accepted
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.Header.Set("Authorization", "Bearer key-v1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with key-v1, got: %d", rec1.Code)
	}

	// 2. Rotate keys to key-v2
	mgr.SetKeys([]string{"key-v2"})

	// 3. key-v1 is now revoked -> 401
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req1)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized after key-v1 revoked, got: %d", rec2.Code)
	}

	// 4. key-v2 is accepted -> 200
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.Header.Set("Authorization", "Bearer key-v2")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with key-v2, got: %d", rec3.Code)
	}
}

func TestKeyManager_OnDecisionHook(t *testing.T) {
	mgr := auth.NewManager([]string{"valid-key"})
	var allowedCount, rejectedCount int
	mgr.SetOnDecision(func(allowed bool) {
		if allowed {
			allowedCount++
		} else {
			rejectedCount++
		}
	})

	handler := mgr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reqOK := httptest.NewRequest("GET", "/test", nil)
	reqOK.Header.Set("X-Gateway-Key", "valid-key")
	handler.ServeHTTP(httptest.NewRecorder(), reqOK)

	reqBad := httptest.NewRequest("GET", "/test", nil)
	reqBad.Header.Set("X-Gateway-Key", "bad-key")
	handler.ServeHTTP(httptest.NewRecorder(), reqBad)

	if allowedCount != 1 || rejectedCount != 1 {
		t.Fatalf("expected allowed=1, rejected=1; got allowed=%d, rejected=%d", allowedCount, rejectedCount)
	}
}
