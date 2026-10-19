package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github-api-gateway/internal/policy"
	"github-api-gateway/internal/token"
)

// Handler proxies authorized HTTP requests to upstream GitHub while injecting tokens and recording usage.
type Handler struct {
	target   *url.URL
	policyMu sync.RWMutex
	policy   *policy.Policy
	pool     *token.Pool
}

// NewHandler creates a new reverse proxy handler.
func NewHandler(targetURL string, pol *policy.Policy, pool *token.Pool) *Handler {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		panic("invalid target URL: " + targetURL)
	}

	return &Handler{
		target: parsed,
		policy: pol,
		pool:   pool,
	}
}

// SetPolicy dynamically updates the active enterprise policy.
func (h *Handler) SetPolicy(pol *policy.Policy) {
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	h.policy = pol
}

// GetPolicy retrieves the active enterprise policy.
func (h *Handler) GetPolicy() *policy.Policy {
	h.policyMu.RLock()
	defer h.policyMu.RUnlock()
	return h.policy
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Policy enforcement (Fail-Closed Default Deny)
	pol := h.GetPolicy()
	if pol != nil {
		allowed, reason := pol.Evaluate(r.Method, r.URL.Path)
		if !allowed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "forbidden",
				"reason":  reason,
				"path":    r.URL.Path,
				"message": "request blocked by relay enterprise access policy",
			})
			return
		}
	}

	// 2. Acquire a token from pool
	var tok string
	if h.pool != nil {
		var err error
		tok, err = h.pool.Acquire()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "rate_limited",
				"message": err.Error(),
			})
			return
		}
	}

	// 3. Configure reverse proxy
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = h.target.Scheme
			req.URL.Host = h.target.Host
			req.Host = h.target.Host

			// Ensure prefix path is preserved
			if h.target.Path != "" && h.target.Path != "/" {
				req.URL.Path = singleJoiningSlash(h.target.Path, req.URL.Path)
				if req.URL.RawPath != "" {
					req.URL.RawPath = singleJoiningSlash(h.target.Path, req.URL.RawPath)
				}
			}

			// Strip internal gateway authentication headers so they never leak upstream
			req.Header.Del("X-Gateway-Key")
			req.Header.Del("X-Relay-Key")

			// Token injection & stripping client auth
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			} else {
				req.Header.Del("Authorization")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			// Record rate limit headers or sideline revoked/expired tokens
			if h.pool != nil && tok != "" {
				if resp.StatusCode == http.StatusUnauthorized {
					h.pool.MarkInvalid(tok)
					return nil
				}

				remainingStr := resp.Header.Get("X-RateLimit-Remaining")
				resetStr := resp.Header.Get("X-RateLimit-Reset")

				if remainingStr != "" && resetStr != "" {
					remaining, err1 := strconv.Atoi(remainingStr)
					resetEpoch, err2 := strconv.ParseInt(resetStr, 10, 64)
					if err1 == nil && err2 == nil {
						h.pool.RecordUsage(tok, remaining, time.Unix(resetEpoch, 0))
					}
				}
			}
			return nil
		},
	}

	proxy.ServeHTTP(w, r)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
