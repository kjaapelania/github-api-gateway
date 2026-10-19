package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github-api-gateway/internal/config"
)

// version can be overridden at link time via -ldflags="-X main.version=v1.0.0"
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	versionFlag := flag.Bool("version", false, "Print version and exit")
	healthcheckFlag := flag.Bool("healthcheck", false, "Perform an HTTP health check against the local gateway and exit (for distroless containers)")
	portFlag := flag.String("port", "", "Port to bind locally (overrides GITHUB_API_GATEWAY_PORT)")
	policyFlag := flag.String("policy", "", "Path to policy.yaml (overrides GITHUB_API_GATEWAY_POLICY_PATH)")
	authKeysFlag := flag.String("auth-keys", "", "Comma-separated gateway auth keys (overrides GITHUB_API_GATEWAY_AUTH_KEYS)")
	tokensFlag := flag.String("tokens", "", "Comma-separated GitHub tokens (overrides GITHUB_API_GATEWAY_TOKENS)")
	minRemainingFlag := flag.Int("min-remaining", -1, "Minimum remaining quota buffer (overrides GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING)")
	fallbackFlag := flag.String("fallback", "", "Fallback to unauthenticated (true/false, overrides GITHUB_API_GATEWAY_FALLBACK_UNAUTHENTICATED)")
	cacheTTLFlag := flag.Int("cache-ttl", -1, "Cache TTL seconds (overrides GITHUB_API_GATEWAY_CACHE_TTL_SECONDS)")
	targetURLFlag := flag.String("target-url", "", "Upstream GitHub API URL (overrides GITHUB_API_GATEWAY_TARGET_URL)")
	clientMaxRPMFlag := flag.Int("client-max-rpm", -1, "Per-client maximum requests per minute (overrides GITHUB_API_GATEWAY_CLIENT_MAX_RPM)")
	clientBurstFlag := flag.Int("client-burst", -1, "Per-client token bucket burst capacity (overrides GITHUB_API_GATEWAY_CLIENT_BURST)")
	flag.Parse()

	if *versionFlag {
		os.Stdout.WriteString("github-api-gateway " + version + "\n")
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// CLI flags override environment variables and .env across initial load and hot-reloads
	applyFlags := func(c *config.Config) {
		if *portFlag != "" {
			c.Port = *portFlag
		}
		if *policyFlag != "" {
			c.PolicyPath = *policyFlag
		}
		if *authKeysFlag != "" {
			c.AuthKeys = splitCSV(*authKeysFlag)
		}
		if *tokensFlag != "" {
			c.GitHubTokens = splitCSV(*tokensFlag)
		}
		if *minRemainingFlag >= 0 {
			c.TokenMinRemaining = *minRemainingFlag
		}
		if *fallbackFlag != "" {
			c.FallbackUnauthenticated = (*fallbackFlag == "true" || *fallbackFlag == "1")
		}
		if *cacheTTLFlag >= 0 {
			c.CacheTTLSeconds = *cacheTTLFlag
		}
		if *targetURLFlag != "" {
			c.TargetURL = *targetURLFlag
		}
		if *clientMaxRPMFlag >= 0 {
			c.ClientMaxRPM = *clientMaxRPMFlag
		}
		if *clientBurstFlag >= 0 {
			c.ClientBurst = *clientBurstFlag
		}
	}
	applyFlags(cfg)

	if *healthcheckFlag {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, hcErr := client.Get("http://127.0.0.1:" + cfg.Port + "/_gateway/status")
		if hcErr != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		_ = resp.Body.Close()
		os.Exit(0)
	}

	slog.Info("starting github-api-gateway", "version", version)

	targetURL := cfg.TargetURL
	if targetURL == "" {
		targetURL = "https://api.github.com"
	}

	app, err := NewApp(cfg, targetURL)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}
	app.ApplyOverrides = applyFlags

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverCtx, serverStop := context.WithCancel(context.Background())

	// Listen for SIGHUP signal to trigger zero-downtime hot-reload
	hupChan := make(chan os.Signal, 1)
	signal.Notify(hupChan, syscall.SIGHUP)
	go func() {
		for range hupChan {
			slog.Info("SIGHUP received: reloading configuration and policy...")
			resp, reloadErr := app.Reload()
			if reloadErr != nil {
				slog.Error("hot reload failed, previous configuration preserved", "error", reloadErr)
			} else {
				slog.Info("hot reload succeeded", "active_keys", resp.ActiveAuthKeys, "rules", resp.PolicyRules)
			}
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		slog.Info("shutdown signal received", "signal", sig.String())

		shutdownCtx, shutdownCancel := context.WithTimeout(serverCtx, 15*time.Second)
		defer shutdownCancel()

		go func() {
			<-shutdownCtx.Done()
			if shutdownCtx.Err() == context.DeadlineExceeded {
				slog.Error("graceful shutdown timed out, forcing exit")
				os.Exit(1)
			}
		}()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
		app.Close()
		serverStop()
	}()

	slog.Info("server listening", "port", cfg.Port, "target", targetURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed to start", "error", err)
		os.Exit(1)
	}

	<-serverCtx.Done()
	slog.Info("github-api-gateway stopped cleanly")
}

func splitCSV(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
