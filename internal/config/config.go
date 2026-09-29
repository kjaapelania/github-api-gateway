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

// Load parses environment variables, optionally reading from .env if present.
func Load() (*Config, error) {
	return LoadFile(".env")
}

// LoadFile loads environment variables from a specific dotenv file if it exists,
// then parses configuration with defaults and environment overrides.
func LoadFile(envPath string) (*Config, error) {
	loadDotEnv(envPath)

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

func loadDotEnv(filename string) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
				val = val[1 : len(val)-1]
			}
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
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
