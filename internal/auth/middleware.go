package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Middleware wraps an http.Handler to enforce pre-shared relay authentication keys.
type Middleware func(http.Handler) http.Handler

// NewMiddleware creates an authentication middleware. If keys is empty, it operates in open mode.
func NewMiddleware(keys []string) Middleware {
	keySet := make(map[string]bool)
	for _, k := range keys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			keySet[trimmed] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If no keys configured, open access mode
			if len(keySet) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			providedKey := extractKey(r)
			if providedKey == "" || !keySet[providedKey] {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":   "unauthorized",
					"message": "missing or invalid relay authentication key",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
