package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration options for the GitHub API Gateway.
type Config struct {
	Port                    string
	GitHubTokens            []string
	AuthKeys                []string
	PolicyPath              string
	TokenMinRemaining       int
	FallbackUnauthenticated bool
	CacheTTLSeconds         int
	TargetURL               string
	ClientMaxRPM            int
	ClientBurst             int
	PolicyPollInterval      time.Duration
	AllowRemoteAdmin        bool
	AdminToken              string
}

// Load parses environment variables, optionally reading from .env if present.
func Load() (*Config, error) {
	return LoadFile(".env")
}

// LoadFile loads environment variables from a specific dotenv file if it exists,
// then parses configuration with defaults and environment overrides using the
// conflict-free GITHUB_API_GATEWAY_ prefix. Real process environment variables
// take precedence over values in the dotenv file, and the process environment
// is not mutated so subsequent hot-reloads reflect edits to the dotenv file.
func LoadFile(envPath string) (*Config, error) {
	dotEnv := parseDotEnv(envPath)

	pollIntervalStr := getEnv("GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL", "", dotEnv)
	var pollInterval time.Duration
	if pollIntervalStr != "" {
		dur, err := time.ParseDuration(pollIntervalStr)
		if err != nil {
			return nil, fmt.Errorf("invalid GITHUB_API_GATEWAY_POLICY_POLL_INTERVAL %q: %w", pollIntervalStr, err)
		}
		pollInterval = dur
	}

	cfg := &Config{
		Port:                    getEnv("GITHUB_API_GATEWAY_PORT", "28280", dotEnv),
		GitHubTokens:            splitList(getEnv("GITHUB_API_GATEWAY_TOKENS", "", dotEnv)),
		AuthKeys:                splitList(getEnv("GITHUB_API_GATEWAY_AUTH_KEYS", "", dotEnv)),
		PolicyPath:              getEnv("GITHUB_API_GATEWAY_POLICY_PATH", "policy.yaml", dotEnv),
		TokenMinRemaining:       getEnvInt("GITHUB_API_GATEWAY_TOKEN_MIN_REMAINING", 3000, dotEnv),
		FallbackUnauthenticated: getEnvBool("GITHUB_API_GATEWAY_FALLBACK_UNAUTHENTICATED", true, dotEnv),
		CacheTTLSeconds:         getEnvInt("GITHUB_API_GATEWAY_CACHE_TTL_SECONDS", 300, dotEnv),
		TargetURL:               getEnv("GITHUB_API_GATEWAY_TARGET_URL", "https://api.github.com", dotEnv),
		ClientMaxRPM:            getEnvInt("GITHUB_API_GATEWAY_CLIENT_MAX_RPM", 60, dotEnv),
		ClientBurst:             getEnvInt("GITHUB_API_GATEWAY_CLIENT_BURST", 10, dotEnv),
		PolicyPollInterval:      pollInterval,
		AllowRemoteAdmin:        getEnvBool("GITHUB_API_GATEWAY_ALLOW_REMOTE_ADMIN", false, dotEnv),
		AdminToken:              getEnv("GITHUB_API_GATEWAY_ADMIN_TOKEN", "", dotEnv),
	}

	if cfg.AllowRemoteAdmin && cfg.AdminToken == "" {
		return nil, fmt.Errorf("remote admin cannot be enabled without GITHUB_API_GATEWAY_ADMIN_TOKEN")
	}

	return cfg, nil
}

func parseDotEnv(filename string) map[string]string {
	values := make(map[string]string)
	data, err := os.ReadFile(filename)
	if err != nil {
		return values
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
			values[key] = val
		}
	}
	return values
}

func lookupVal(key string, dotEnv map[string]string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	if dotEnv != nil {
		if val := dotEnv[key]; val != "" {
			return val
		}
	}
	return ""
}

func getEnv(key, defaultVal string, dotEnv map[string]string) string {
	if val := lookupVal(key, dotEnv); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int, dotEnv map[string]string) int {
	valStr := lookupVal(key, dotEnv)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

func getEnvBool(key string, defaultVal bool, dotEnv map[string]string) bool {
	valStr := lookupVal(key, dotEnv)
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
