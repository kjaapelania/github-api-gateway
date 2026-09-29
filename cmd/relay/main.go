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

	"github-api-relay/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	portFlag := flag.String("port", "", "Port to bind locally (overrides PORT)")
	policyFlag := flag.String("policy", "", "Path to policy.yaml (overrides POLICY_PATH)")
	authKeysFlag := flag.String("auth-keys", "", "Comma-separated relay auth keys (overrides RELAY_AUTH_KEYS)")
	tokensFlag := flag.String("tokens", "", "Comma-separated GitHub tokens (overrides GITHUB_TOKENS)")
	minRemainingFlag := flag.Int("min-remaining", -1, "Minimum remaining quota buffer (overrides TOKEN_MIN_REMAINING)")
	fallbackFlag := flag.String("fallback", "", "Fallback to unauthenticated (true/false, overrides FALLBACK_UNAUTHENTICATED)")
	cacheTTLFlag := flag.Int("cache-ttl", -1, "Cache TTL seconds (overrides CACHE_TTL_SECONDS)")
	targetURLFlag := flag.String("target-url", "", "Upstream GitHub API URL (overrides GITHUB_API_URL)")
	flag.Parse()

	slog.Info("starting github-api-relay")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// CLI flags override environment variables and .env
	if *portFlag != "" {
		cfg.Port = *portFlag
	}
	if *policyFlag != "" {
		cfg.PolicyPath = *policyFlag
	}
	if *authKeysFlag != "" {
		cfg.RelayAuthKeys = splitCSV(*authKeysFlag)
	}
	if *tokensFlag != "" {
		cfg.GitHubTokens = splitCSV(*tokensFlag)
	}
	if *minRemainingFlag >= 0 {
		cfg.TokenMinRemaining = *minRemainingFlag
	}
	if *fallbackFlag != "" {
		cfg.FallbackUnauthenticated = (*fallbackFlag == "true" || *fallbackFlag == "1")
	}
	if *cacheTTLFlag >= 0 {
		cfg.CacheTTLSeconds = *cacheTTLFlag
	}

	targetURL := os.Getenv("GITHUB_API_URL")
	if *targetURLFlag != "" {
		targetURL = *targetURLFlag
	} else if targetURL == "" {
		targetURL = "https://api.github.com"
	}

	app, err := NewApp(cfg, targetURL)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

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
				slog.Info("hot reload succeeded", "active_keys", resp.ActiveRelayKeys, "rules", resp.PolicyRules)
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
		serverStop()
	}()

	slog.Info("server listening", "port", cfg.Port, "target", targetURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed to start", "error", err)
		os.Exit(1)
	}

	<-serverCtx.Done()
	slog.Info("github-api-relay stopped cleanly")
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
