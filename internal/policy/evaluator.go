package policy

import (
	"strings"
)

// Policy holds validated and compiled access rules.
type Policy struct {
	rules []Rule
}

// RuleCount returns the number of active rules.
func (p *Policy) RuleCount() int {
	if p == nil {
		return 0
	}
	return len(p.rules)
}

// Evaluate evaluates an incoming HTTP request against the policy and returns whether it is allowed.
func (p *Policy) Evaluate(method, urlPath string) (bool, DenialReason) {
	upperMethod := strings.ToUpper(strings.TrimSpace(method))
	cleanPath := cleanURLPath(urlPath)

	// 1. Hardcoded Denylist Check (Unconditionally Blocked)
	if isHardcodedDenylisted(cleanPath) {
		return false, ReasonDeniedSensitivePath
	}

	// Extract repo from path if path is /repos/{owner}/{repo}/...
	repo := extractRepoFromPath(cleanPath)

	// 2. Evaluate rules in order
	matchedPattern := false

	for _, rule := range p.rules {
		// Check repo match (if rule defines repos)
		if len(rule.compiledRepoRegex) > 0 {
			if repo == "" {
				continue
			}
			matchedRepo := false
			for _, re := range rule.compiledRepoRegex {
				if re.MatchString(repo) {
					matchedRepo = true
					break
				}
			}
			if !matchedRepo {
				continue
			}
		}

		// Check path match (if rule defines paths)
		if len(rule.compiledPathRegex) > 0 {
			matchedPath := false
			for _, re := range rule.compiledPathRegex {
				if re.MatchString(cleanPath) {
					matchedPath = true
					break
				}
			}
			if !matchedPath {
				continue
			}
		}

		// If we reached here, repo and path matched this rule!
		matchedPattern = true

		// Check if the requested method is allowed
		for _, m := range rule.Methods {
			if m == upperMethod {
				return true, ReasonAllowed
			}
		}
	}

	// If pattern matched but method was not allowed
	if matchedPattern {
		return false, ReasonMethodNotAllowed
	}

	// 3. Fail-Closed: Default Deny
	return false, ReasonDefaultDeny
}

func isHardcodedDenylisted(path string) bool {
	// Sensitive account, admin, and authorization paths
	deniedPrefixes := []string{
		"/user",
		"/admin",
		"/authorizations",
		"/settings",
	}

	for _, prefix := range deniedPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func cleanURLPath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	// Strip query parameters if any were passed in path
	if idx := strings.Index(path, "?"); idx != -1 {
		return path[:idx]
	}
	return path
}

func extractRepoFromPath(path string) string {
	// Paths typically look like /repos/{owner}/{repo}/...
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		return parts[1] + "/" + parts[2]
	}
	return ""
}
