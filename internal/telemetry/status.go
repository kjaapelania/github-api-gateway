package telemetry

import (
	"encoding/json"
	"net/http"
	"time"

	"github-api-relay/internal/cache"
	"github-api-relay/internal/policy"
	"github-api-relay/internal/token"
)

// StatusResponse is the JSON payload returned by /_relay/status.
type StatusResponse struct {
	Status          string            `json:"status"`
	Uptime          string            `json:"uptime"`
	UptimeSeconds   float64           `json:"uptime_seconds"`
	ActiveRelayKeys int               `json:"active_relay_keys"`
	CacheEntries    int               `json:"cache_entries"`
	PolicyRules     int               `json:"policy_rules"`
	Tokens          []token.TokenInfo `json:"tokens"`
}

// NewStatusHandler returns an http.Handler that reports runtime health and telemetry.
func NewStatusHandler(startTime time.Time, pool *token.Pool, c *cache.Cache, activeKeys int, pol *policy.Policy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		now := time.Now()
		uptimeDuration := now.Sub(startTime)

		var tokenInfos []token.TokenInfo
		if pool != nil {
			tokenInfos = pool.Status()
		}
		if tokenInfos == nil {
			tokenInfos = []token.TokenInfo{}
		}

		cacheCount := 0
		if c != nil {
			cacheCount = c.Count()
		}

		policyCount := 0
		if pol != nil {
			policyCount = pol.RuleCount()
		}

		resp := StatusResponse{
			Status:          "ok",
			Uptime:          uptimeDuration.Truncate(time.Second).String(),
			UptimeSeconds:   uptimeDuration.Seconds(),
			ActiveRelayKeys: activeKeys,
			CacheEntries:    cacheCount,
			PolicyRules:     policyCount,
			Tokens:          tokenInfos,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
