package config_test

import (
	"os"
	"reflect"
	"testing"

	"github-api-relay/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all relevant environment variables
	envVars := []string{
		"PORT", "GITHUB_TOKENS", "RELAY_AUTH_KEYS",
		"POLICY_PATH", "TOKEN_MIN_REMAINING",
		"FALLBACK_UNAUTHENTICATED", "CACHE_TTL_SECONDS",
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
	if len(cfg.RelayAuthKeys) != 0 {
		t.Errorf("expected empty RelayAuthKeys, got: %v", cfg.RelayAuthKeys)
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
}

func TestLoadCustomEnvironment(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("GITHUB_TOKENS", "ghp_token1, ghp_token2 , github_pat_token3")
	t.Setenv("RELAY_AUTH_KEYS", "key1, key2 ")
	t.Setenv("POLICY_PATH", "custom-policy.yaml")
	t.Setenv("TOKEN_MIN_REMAINING", "500")
	t.Setenv("FALLBACK_UNAUTHENTICATED", "false")
	t.Setenv("CACHE_TTL_SECONDS", "60")

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
	if !reflect.DeepEqual(cfg.RelayAuthKeys, expectedKeys) {
		t.Errorf("expected RelayAuthKeys=%v, got: %v", expectedKeys, cfg.RelayAuthKeys)
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
}

func TestLoadDotEnv_PreservesExistingEnv(t *testing.T) {
	tmpFile, err := os.CreateTemp("", ".env-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	_, _ = tmpFile.WriteString("GITHUB_TOKENS=ghp_from_dotenv\nPORT=9999\n")
	_ = tmpFile.Close()

	t.Setenv("PORT", "7777")
	_ = os.Unsetenv("GITHUB_TOKENS")

	cfg, err := config.LoadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Port != "7777" {
		t.Errorf("expected PORT to be overridden by environment (7777), got: %s", cfg.Port)
	}
	if len(cfg.GitHubTokens) != 1 || cfg.GitHubTokens[0] != "ghp_from_dotenv" {
		t.Errorf("expected GITHUB_TOKENS to be loaded from dotenv file, got: %v", cfg.GitHubTokens)
	}
}
