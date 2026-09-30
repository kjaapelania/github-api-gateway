package config_test

import (
	"os"
	"reflect"
	"testing"

	"github-api-gateway/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all relevant environment variables
	envVars := []string{
		"GITHUB_API_GATEWAY_PORT", "GITHUB_API_GATEWAY_TOKENS", "GITHUB_API_GATEWAY_AUTH_KEYS",
		"GITHUB_API_GATEWAY_POLICY_PATH", "GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING",
		"GITHUB_API_GATEWAY_FALLBACK_UNAUTHENTICATED", "GITHUB_API_GATEWAY_CACHE_TTL_SECONDS",
		"GITHUB_API_GATEWAY_TARGET_URL",
	}
	for _, env := range envVars {
		_ = os.Unsetenv(env)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error loading defaults, got: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("expected Port=8080, got: %s", cfg.Port)
	}
	if len(cfg.GitHubTokens) != 0 {
		t.Errorf("expected empty GitHubTokens, got: %v", cfg.GitHubTokens)
	}
	if len(cfg.AuthKeys) != 0 {
		t.Errorf("expected empty AuthKeys, got: %v", cfg.AuthKeys)
	}
	if cfg.PolicyPath != "policy.yaml" {
		t.Errorf("expected PolicyPath=policy.yaml, got: %s", cfg.PolicyPath)
	}
	if cfg.TokenMinRemaining != 3000 {
		t.Errorf("expected TokenMinRemaining=3000, got: %d", cfg.TokenMinRemaining)
	}
	if !cfg.FallbackUnauthenticated {
		t.Errorf("expected FallbackUnauthenticated=true, got: %v", cfg.FallbackUnauthenticated)
	}
	if cfg.CacheTTLSeconds != 300 {
		t.Errorf("expected CacheTTLSeconds=300, got: %d", cfg.CacheTTLSeconds)
	}
	if cfg.TargetURL != "https://api.github.com" {
		t.Errorf("expected TargetURL=https://api.github.com, got: %s", cfg.TargetURL)
	}
	if cfg.ClientMaxRPM != 60 {
		t.Errorf("expected ClientMaxRPM=60, got: %d", cfg.ClientMaxRPM)
	}
	if cfg.ClientBurst != 10 {
		t.Errorf("expected ClientBurst=10, got: %d", cfg.ClientBurst)
	}
}

func TestLoadCustomEnvironment(t *testing.T) {
	t.Setenv("GITHUB_API_GATEWAY_PORT", "9090")
	t.Setenv("GITHUB_API_GATEWAY_TOKENS", "ghp_token1, ghp_token2 , github_pat_token3")
	t.Setenv("GITHUB_API_GATEWAY_AUTH_KEYS", "key1, key2 ")
	t.Setenv("GITHUB_API_GATEWAY_POLICY_PATH", "custom-policy.yaml")
	t.Setenv("GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING", "500")
	t.Setenv("GITHUB_API_GATEWAY_FALLBACK_UNAUTHENTICATED", "false")
	t.Setenv("GITHUB_API_GATEWAY_CACHE_TTL_SECONDS", "60")
	t.Setenv("GITHUB_API_GATEWAY_TARGET_URL", "https://github-enterprise.internal/api/v3")
	t.Setenv("GITHUB_API_GATEWAY_CLIENT_MAX_RPM", "120")
	t.Setenv("GITHUB_API_GATEWAY_CLIENT_BURST", "20")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("expected Port=9090, got: %s", cfg.Port)
	}

	expectedTokens := []string{"ghp_token1", "ghp_token2", "github_pat_token3"}
	if !reflect.DeepEqual(cfg.GitHubTokens, expectedTokens) {
		t.Errorf("expected GitHubTokens=%v, got: %v", expectedTokens, cfg.GitHubTokens)
	}

	expectedKeys := []string{"key1", "key2"}
	if !reflect.DeepEqual(cfg.AuthKeys, expectedKeys) {
		t.Errorf("expected AuthKeys=%v, got: %v", expectedKeys, cfg.AuthKeys)
	}

	if cfg.PolicyPath != "custom-policy.yaml" {
		t.Errorf("expected PolicyPath=custom-policy.yaml, got: %s", cfg.PolicyPath)
	}
	if cfg.TokenMinRemaining != 500 {
		t.Errorf("expected TokenMinRemaining=500, got: %d", cfg.TokenMinRemaining)
	}
	if cfg.FallbackUnauthenticated {
		t.Errorf("expected FallbackUnauthenticated=false, got: %v", cfg.FallbackUnauthenticated)
	}
	if cfg.CacheTTLSeconds != 60 {
		t.Errorf("expected CacheTTLSeconds=60, got: %d", cfg.CacheTTLSeconds)
	}
	if cfg.TargetURL != "https://github-enterprise.internal/api/v3" {
		t.Errorf("expected TargetURL=https://github-enterprise.internal/api/v3, got: %s", cfg.TargetURL)
	}
	if cfg.ClientMaxRPM != 120 {
		t.Errorf("expected ClientMaxRPM=120, got: %d", cfg.ClientMaxRPM)
	}
	if cfg.ClientBurst != 20 {
		t.Errorf("expected ClientBurst=20, got: %d", cfg.ClientBurst)
	}
}

func TestLoadDotEnv_PreservesExistingEnv(t *testing.T) {
	tmpFile, err := os.CreateTemp("", ".env-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	_, _ = tmpFile.WriteString("GITHUB_API_GATEWAY_TOKENS=ghp_from_dotenv\nGITHUB_API_GATEWAY_PORT=9999\n")
	_ = tmpFile.Close()

	t.Setenv("GITHUB_API_GATEWAY_PORT", "7777")
	_ = os.Unsetenv("GITHUB_API_GATEWAY_TOKENS")

	cfg, err := config.LoadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Port != "7777" {
		t.Errorf("expected PORT to be overridden by environment (7777), got: %s", cfg.Port)
	}
	if len(cfg.GitHubTokens) != 1 || cfg.GitHubTokens[0] != "ghp_from_dotenv" {
		t.Errorf("expected GITHUB_API_GATEWAY_TOKENS to be loaded from dotenv file, got: %v", cfg.GitHubTokens)
	}
}
