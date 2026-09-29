package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github-api-relay/internal/auth"
	"github-api-relay/internal/cache"
	"github-api-relay/internal/config"
	"github-api-relay/internal/policy"
	"github-api-relay/internal/proxy"
	"github-api-relay/internal/telemetry"
	"github-api-relay/internal/token"
)

// App manages the lifecycle of the GitHub API Rate Limit Relay components.
type App struct {
	Config     *config.Config
	Policy     *policy.Policy
	Pool       *token.Pool
	Cache      *cache.Cache
	AuthMgr    *auth.Manager
	Proxy      *proxy.Handler
	Handler    http.Handler
	StartTime  time.Time
	TargetHost string
	mu         sync.RWMutex
}

// ReloadResponse represents the outcome of a successful hot-reload.
type ReloadResponse struct {
	Status          string `json:"status"`
	ActiveRelayKeys int    `json:"active_relay_keys"`
	PolicyRules     int    `json:"policy_rules"`
	ActiveTokens    int    `json:"active_tokens"`
}

// NewApp instantiates and wires together all relay modules into a cohesive HTTP handler.
func NewApp(cfg *config.Config, targetURL string) (*App, error) {
	// 1. Load fail-closed enterprise access policy
	pol, err := policy.LoadFromFile(cfg.PolicyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load policy from %s: %w", cfg.PolicyPath, err)
	}

	// 2. Token pool with Largest-Remaining selection & 10s cooldown
	pool := token.NewPool(cfg.GitHubTokens, cfg.TokenMinRemaining, cfg.FallbackUnauthenticated)

	// 3. Thread-safe in-memory ETag response cache
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	c := cache.New(ttl)

	// 4. Reverse proxy handler
	proxyHandler := proxy.NewHandler(targetURL, pol, pool)

	// 5. Pre-shared keys authentication manager
	authMgr := auth.NewManager(cfg.RelayAuthKeys)

	// 6. Telemetry and health status handler
	startTime := time.Now()
	statusHandler := telemetry.NewStatusHandler(startTime, pool, c, len(cfg.RelayAuthKeys), pol)

	// 7. Route and middleware stack:
	// Status & Reload endpoints are accessible for administration
	// API requests pass through Auth -> Cache -> Proxy
	apiHandler := authMgr.Middleware(c.Middleware(proxyHandler))

	mux := http.NewServeMux()
	mux.Handle("/_relay/status", statusHandler)
	mux.Handle("/", apiHandler)

	app := &App{
		Config:     cfg,
		Policy:     pol,
		Pool:       pool,
		Cache:      c,
		AuthMgr:    authMgr,
		Proxy:      proxyHandler,
		Handler:    mux,
		StartTime:  startTime,
		TargetHost: targetURL,
	}

	mux.HandleFunc("/_relay/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "method not allowed, use POST to reload",
			})
			return
		}

		resp, reloadErr := app.Reload()
		w.Header().Set("Content-Type", "application/json")
		if reloadErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  reloadErr.Error(),
				"config": "previous configuration preserved",
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})

	return app, nil
}

// Reload atomically reloads configuration, policy, tokens, and relay keys in-memory.
// If any error occurs (e.g. invalid YAML syntax), the previous valid state is preserved.
func (a *App) Reload() (*ReloadResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. Reload configuration from .env / env vars
	newCfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to reload config: %w", err)
	}
	// Preserve configured policy path if new configuration has default value
	if a.Config != nil && a.Config.PolicyPath != "" && newCfg.PolicyPath == "policy.yaml" {
		newCfg.PolicyPath = a.Config.PolicyPath
	}

	// 2. Validate and compile new policy
	newPolicy, err := policy.LoadFromFile(newCfg.PolicyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to reload policy from %s: %w", newCfg.PolicyPath, err)
	}

	// 3. Atomically apply changes without dropping connections
	a.Policy = newPolicy
	a.Config = newCfg
	a.Proxy.SetPolicy(newPolicy)
	a.AuthMgr.SetKeys(newCfg.RelayAuthKeys)
	a.Pool.UpdateTokens(newCfg.GitHubTokens)

	return &ReloadResponse{
		Status:          "reloaded",
		ActiveRelayKeys: a.AuthMgr.KeyCount(),
		PolicyRules:     newPolicy.RuleCount(),
		ActiveTokens:    len(newCfg.GitHubTokens),
	}, nil
}
