package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration options for the GitHub API relay.
type Config struct {
	Port                    string
	GitHubTokens            []string
	RelayAuthKeys           []string
	PolicyPath              string
	TokenMinRemaining       int
	FallbackUnauthenticated bool
	CacheTTLSeconds         int
}

// Load parses environment variables and returns a populated Config with defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Port:                    getEnv("PORT", "8080"),
		GitHubTokens:            splitList(os.Getenv("GITHUB_TOKENS")),
		RelayAuthKeys:           splitList(os.Getenv("RELAY_AUTH_KEYS")),
		PolicyPath:              getEnv("POLICY_PATH", "policy.yaml"),
		TokenMinRemaining:       getEnvInt("TOKEN_MIN_REMAINING", 3000),
		FallbackUnauthenticated: getEnvBool("FALLBACK_UNAUTHENTICATED", true),
		CacheTTLSeconds:         getEnvInt("CACHE_TTL_SECONDS", 300),
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

func getEnvBool(key string, defaultVal bool) bool {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.ParseBool(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

func splitList(val string) []string {
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
