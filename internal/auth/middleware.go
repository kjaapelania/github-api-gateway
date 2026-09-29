package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// Middleware wraps an http.Handler to enforce pre-shared relay authentication keys.
type Middleware func(http.Handler) http.Handler

// Manager manages active relay authentication keys dynamically with thread safety.
type Manager struct {
	mu     sync.RWMutex
	keySet map[string]bool
}

// NewManager creates a key manager with the provided keys.
func NewManager(keys []string) *Manager {
	m := &Manager{}
	m.SetKeys(keys)
	return m
}

// SetKeys atomically replaces the set of allowed relay authentication keys.
func (m *Manager) SetKeys(keys []string) {
	newSet := make(map[string]bool)
	for _, k := range keys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			newSet[trimmed] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keySet = newSet
}

// KeyCount returns the number of active relay authentication keys.
func (m *Manager) KeyCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.keySet)
}

// Middleware returns an http.Handler middleware that validates requests against active keys.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		isOpen := len(m.keySet) == 0
		providedKey := extractKey(r)
		allowed := isOpen || m.keySet[providedKey]
		m.mu.RUnlock()

		if allowed {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "unauthorized",
			"message": "missing or invalid relay authentication key",
		})
	})
}

// NewMiddleware creates an authentication middleware. If keys is empty, it operates in open mode.
func NewMiddleware(keys []string) Middleware {
	mgr := NewManager(keys)
	return mgr.Middleware
}

func extractKey(r *http.Request) string {
	// 1. Check X-Relay-Key header
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

	return ""
}
