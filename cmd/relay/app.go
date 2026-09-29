package main

import (
	"fmt"
	"net/http"
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
	Handler    http.Handler
	StartTime  time.Time
	TargetHost string
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

	// 5. Pre-shared keys authentication middleware
	authMiddleware := auth.NewMiddleware(cfg.RelayAuthKeys)

	// 6. Telemetry and health status handler
	startTime := time.Now()
	statusHandler := telemetry.NewStatusHandler(startTime, pool, c, len(cfg.RelayAuthKeys), pol)

	// 7. Route and middleware stack:
	// Status endpoint is accessible locally for health/telemetry
	// API requests pass through Auth -> Cache -> Proxy
	apiHandler := authMiddleware(c.Middleware(proxyHandler))

	mux := http.NewServeMux()
	mux.Handle("/_relay/status", statusHandler)
	mux.Handle("/", apiHandler)

	return &App{
		Config:     cfg,
		Policy:     pol,
		Pool:       pool,
		Cache:      c,
		Handler:    mux,
		StartTime:  startTime,
		TargetHost: targetURL,
	}, nil
}
