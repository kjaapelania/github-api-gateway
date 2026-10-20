package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github-api-gateway/internal/auth"
	"github-api-gateway/internal/cache"
	"github-api-gateway/internal/config"
	"github-api-gateway/internal/policy"
	"github-api-gateway/internal/proxy"
	"github-api-gateway/internal/ratelimit"
	"github-api-gateway/internal/telemetry"
	"github-api-gateway/internal/token"
)

// App manages the lifecycle of the GitHub API Gateway components.
type App struct {
	Config         *config.Config
	ApplyOverrides func(*config.Config)
	Policy         *policy.Policy
	PolicySource   *policy.PolicySource
	Fetcher        *policy.Fetcher
	Reconciler     *policy.Reconciler
	Pool           *token.Pool
	Cache          *cache.Cache
	AuthMgr        *auth.Manager
	Limiter        *ratelimit.Limiter
	Proxy          *proxy.Handler
	Metrics        *telemetry.Registry
	Handler        http.Handler
	StartTime      time.Time
	TargetHost     string
	mu             sync.RWMutex
	reloadMu       sync.Mutex
}

// ReloadResponse represents the outcome of a successful hot-reload.
type ReloadResponse struct {
	Status         string `json:"status"`
	ActiveAuthKeys int    `json:"active_auth_keys"`
	PolicyRules    int    `json:"policy_rules"`
	ActiveTokens   int    `json:"active_tokens,omitempty"`
	Source         string `json:"source,omitempty"`
	ETag           string `json:"etag,omitempty"`
}

// NewApp instantiates and wires together all gateway modules into a cohesive HTTP handler.
func NewApp(cfg *config.Config, targetURL string) (*App, error) {
	// 1. Load fail-closed enterprise access policy (local file or remote URL)
	policySource, err := policy.NewPolicySource(cfg.PolicyPath)
	if err != nil {
		return nil, fmt.Errorf("invalid policy path %s: %w", cfg.PolicyPath, err)
	}

	fetcher := policy.NewFetcher(nil)
	data, _, err := fetcher.Fetch(context.Background(), policySource)
	if err != nil {
		return nil, fmt.Errorf("failed to load policy from %s: %w", cfg.PolicyPath, err)
	}

	pol, err := policy.LoadFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse policy from %s: %w", cfg.PolicyPath, err)
	}

	// 2. Token pool with Largest-Remaining selection & 10s cooldown
	pool := token.NewPool(cfg.GitHubTokens, cfg.TokenMinRemaining, cfg.FallbackUnauthenticated)

	// 3. Thread-safe in-memory ETag response cache
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	c := cache.New(ttl)

	// 4. Reverse proxy handler
	proxyHandler := proxy.NewHandler(targetURL, pol, pool)

	// 5. Pre-shared keys authentication manager
	authMgr := auth.NewManager(cfg.AuthKeys)

	// 6. Per-client token-bucket rate limiter to prevent noisy-neighbor quota exhaustion
	limiter := ratelimit.NewLimiter(cfg.ClientMaxRPM, cfg.ClientBurst)

	// 7. Telemetry, Prometheus metrics, and health status handlers
	startTime := time.Now()
	policyRulesFn := func() int {
		if p := proxyHandler.GetPolicy(); p != nil {
			return p.RuleCount()
		}
		return 0
	}

	statusHandler := telemetry.NewDynamicStatusHandler(
		startTime,
		pool,
		c,
		authMgr.KeyCount,
		policyRulesFn,
	)

	metricsRegistry := telemetry.NewRegistry(startTime, telemetry.GaugeSources{
		ActiveAuthKeys: authMgr.KeyCount,
		PolicyRules:    policyRulesFn,
		CacheEntries:   c.Count,
		ActiveBuckets:  limiter.BucketCount,
		TokenStatuses:  pool.Status,
	})
	authMgr.SetOnDecision(metricsRegistry.RecordAuthDecision)
	limiter.SetOnDecision(metricsRegistry.RecordRateLimitDecision)
	c.SetOnOutcome(metricsRegistry.RecordCacheOutcome)
	proxyHandler.SetOnPolicyDecision(metricsRegistry.RecordPolicyDecision)

	metricsHandler := telemetry.NewMetricsHandler(metricsRegistry)
	obsMiddleware := telemetry.NewObservabilityMiddleware(metricsRegistry, slog.Default())

	// 8. Route and middleware stack:
	// Status, Metrics & Reload endpoints are accessible for administration
	// API requests pass through Observability -> Auth -> RateLimiter -> Cache -> Proxy
	apiHandler := obsMiddleware(authMgr.Middleware(limiter.Middleware(c.Middleware(proxyHandler))))

	mux := http.NewServeMux()
	mux.Handle("/_gateway/status", statusHandler)
	mux.Handle("/_relay/status", statusHandler)
	mux.Handle("/_gateway/metrics", metricsHandler)
	mux.Handle("/_relay/metrics", metricsHandler)
	mux.Handle("/", apiHandler)

	app := &App{
		Config:       cfg,
		Policy:       pol,
		PolicySource: policySource,
		Fetcher:      fetcher,
		Pool:         pool,
		Cache:        c,
		AuthMgr:      authMgr,
		Limiter:      limiter,
		Proxy:        proxyHandler,
		Metrics:      metricsRegistry,
		Handler:      mux,
		StartTime:    startTime,
		TargetHost:   targetURL,
	}

	if cfg.PolicyPollInterval > 0 && policySource.IsRemote {
		reloadCallback := func(data []byte) error {
			newPolicy, err := policy.LoadFromBytes(data)
			if err != nil {
				return err
			}
			app.mu.Lock()
			defer app.mu.Unlock()
			app.Policy = newPolicy
			app.Proxy.SetPolicy(newPolicy)
			return nil
		}
		reconciler := policy.NewReconciler(policySource, fetcher, cfg.PolicyPollInterval, reloadCallback, nil)
		reconciler.Start()
		app.Reconciler = reconciler
	}

	reloadHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "method not allowed, use POST to reload",
			})
			return
		}

		// Security: Restrict reload endpoint to loopback (127.0.0.1 / ::1) unless remote admin is enabled
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		isLoopback := ip != nil && ip.IsLoopback()

		app.mu.RLock()
		allowRemote := app.Config.AllowRemoteAdmin
		adminToken := app.Config.AdminToken
		app.mu.RUnlock()

		if !isLoopback && !allowRemote {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "forbidden: remote admin is disabled",
			})
			return
		}

		// If adminToken is configured, or if caller is remote, authenticate Bearer token
		if adminToken != "" || !isLoopback {
			authHeader := r.Header.Get("Authorization")
			token := ""
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "unauthorized",
				})
				return
			}
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
	}

	mux.HandleFunc("/_gateway/reload", reloadHandler)
	mux.HandleFunc("/_relay/reload", reloadHandler)

	return app, nil
}

// Reload atomically reloads configuration, policy, tokens, and gateway keys in-memory.
// If any error occurs (e.g. invalid YAML syntax), the previous valid state is preserved.
func (a *App) Reload() (*ReloadResponse, error) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	// 1. Reload configuration from .env / env vars
	newCfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to reload config: %w", err)
	}

	a.mu.RLock()
	prevCfg := a.Config
	fetcher := a.Fetcher
	applyOverrides := a.ApplyOverrides
	a.mu.RUnlock()

	// Preserve configured options if new configuration has defaults or env vars are unset
	if prevCfg != nil {
		if prevCfg.PolicyPath != "" && newCfg.PolicyPath == "policy.yaml" {
			newCfg.PolicyPath = prevCfg.PolicyPath
		}
		if os.Getenv("GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN") == "" {
			newCfg.AllowRemoteAdmin = prevCfg.AllowRemoteAdmin
		}
		if os.Getenv("GITHUB_API_GATEWAY_ADMIN_TOKEN") == "" {
			newCfg.AdminToken = prevCfg.AdminToken
		}
		if os.Getenv("GITHUB_API_GATEWAY_AUTH_KEYS") == "" && len(newCfg.AuthKeys) == 0 && len(prevCfg.AuthKeys) > 0 {
			newCfg.AuthKeys = prevCfg.AuthKeys
		}
		if os.Getenv("GITHUB_API_GATEWAY_TOKENS") == "" && len(newCfg.GitHubTokens) == 0 && len(prevCfg.GitHubTokens) > 0 {
			newCfg.GitHubTokens = prevCfg.GitHubTokens
		}
	}

	if applyOverrides != nil {
		applyOverrides(newCfg)
	}

	// 2. Validate and compile new policy
	newPolicySource, err := policy.NewPolicySource(newCfg.PolicyPath)
	if err != nil {
		return nil, fmt.Errorf("invalid policy path %s: %w", newCfg.PolicyPath, err)
	}

	if fetcher == nil {
		fetcher = policy.NewFetcher(nil)
	}

	data, _, err := fetcher.Fetch(context.Background(), newPolicySource)
	if err != nil {
		return nil, fmt.Errorf("failed to reload policy from %s: %w", newCfg.PolicyPath, err)
	}

	newPolicy, err := policy.LoadFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to reload policy from %s: %w", newCfg.PolicyPath, err)
	}

	// 3. Stop previous reconciler outside a.mu.Lock() to prevent ABBA deadlock with reloadCallback
	a.mu.Lock()
	oldReconciler := a.Reconciler
	a.Reconciler = nil
	a.mu.Unlock()

	if oldReconciler != nil {
		oldReconciler.Stop()
	}

	// 4. Atomically apply changes without dropping connections
	a.mu.Lock()
	defer a.mu.Unlock()

	if newCfg.PolicyPollInterval > 0 && newPolicySource.IsRemote {
		reloadCallback := func(data []byte) error {
			reloadedPolicy, err := policy.LoadFromBytes(data)
			if err != nil {
				return err
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			a.Policy = reloadedPolicy
			a.Proxy.SetPolicy(reloadedPolicy)
			return nil
		}
		reconciler := policy.NewReconciler(newPolicySource, fetcher, newCfg.PolicyPollInterval, reloadCallback, nil)
		reconciler.Start()
		a.Reconciler = reconciler
	}

	a.Policy = newPolicy
	a.PolicySource = newPolicySource
	a.Config = newCfg
	a.Proxy.SetPolicy(newPolicy)
	a.AuthMgr.SetKeys(newCfg.AuthKeys)
	a.Pool.UpdateTokens(newCfg.GitHubTokens)
	if a.Limiter != nil {
		a.Limiter.UpdateLimits(newCfg.ClientMaxRPM, newCfg.ClientBurst)
	}

	var etag string
	var sourceURI string
	if a.PolicySource != nil {
		sourceURI = a.PolicySource.URI
		etag = a.PolicySource.Snapshot().LastETag
	}

	return &ReloadResponse{
		Status:         "reloaded",
		ActiveAuthKeys: a.AuthMgr.KeyCount(),
		PolicyRules:    newPolicy.RuleCount(),
		ActiveTokens:   len(newCfg.GitHubTokens),
		Source:         sourceURI,
		ETag:           etag,
	}, nil
}

// Close gracefully terminates background tasks like the policy reconciler.
func (a *App) Close() {
	a.mu.Lock()
	oldReconciler := a.Reconciler
	a.Reconciler = nil
	a.mu.Unlock()

	if oldReconciler != nil {
		oldReconciler.Stop()
	}
}
