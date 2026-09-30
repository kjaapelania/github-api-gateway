package policy_test

import (
	"testing"

	"github-api-gateway/internal/policy"
)

func TestPolicyValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yamlDoc string
		wantErr string
	}{
		{
			name: "mutual exclusivity: both repo and repo_regex",
			yamlDoc: `
internal_rules:
  - name: "invalid-repo"
    repo: "my-org/*"
    repo_regex: "^my-org/.*$"
    methods: ["GET"]
`,
			wantErr: "mutually exclusive",
		},
		{
			name: "mutual exclusivity: both path and path_regex",
			yamlDoc: `
internal_rules:
  - name: "invalid-path"
    path: "/repos/**"
    path_regex: "^/repos/.*$"
    methods: ["GET"]
`,
			wantErr: "mutually exclusive",
		},
		{
			name: "missing methods",
			yamlDoc: `
internal_rules:
  - name: "missing-methods"
    repo: "my-org/*"
`,
			wantErr: "methods is mandatory",
		},
		{
			name: "invalid HTTP method typo",
			yamlDoc: `
internal_rules:
  - name: "typo-method"
    repo: "my-org/*"
    methods: ["GTE", "HEAD"]
`,
			wantErr: "invalid HTTP method 'GTE'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := policy.LoadFromBytes([]byte(tt.yamlDoc))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error %q to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestHardcodedDenylist(t *testing.T) {
	// Even with a wide open global allow rule, sensitive endpoints must be blocked
	yamlDoc := `
internal_rules:
  - name: "allow-all"
    methods: ["GET", "HEAD"]
`
	pol, err := policy.LoadFromBytes([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sensitivePaths := []string{
		"/user",
		"/user/keys",
		"/user/emails",
		"/admin",
		"/admin/hooks",
		"/authorizations",
		"/authorizations/123",
	}

	for _, p := range sensitivePaths {
		allowed, reason := pol.Evaluate("GET", p)
		if allowed {
			t.Errorf("expected sensitive path %s to be denied, but was allowed", p)
		}
		if reason != policy.ReasonDeniedSensitivePath {
			t.Errorf("expected reason %v, got %v", policy.ReasonDeniedSensitivePath, reason)
		}
	}
}

func TestDefaultDeny(t *testing.T) {
	yamlDoc := `
internal_rules:
  - name: "team-a"
    repo: "my-org/team-a"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request to completely unlisted repo
	allowed, reason := pol.Evaluate("GET", "/repos/other-org/other-repo/releases")
	if allowed {
		t.Error("expected unlisted repo to be denied by default")
	}
	if reason != policy.ReasonDefaultDeny {
		t.Errorf("expected reason %v, got %v", policy.ReasonDefaultDeny, reason)
	}
}

func TestMethodFiltering(t *testing.T) {
	yamlDoc := `
internal_rules:
  - name: "team-a-readonly"
    repo: "my-org/team-a"
    methods: ["GET", "HEAD"]
`
	pol, err := policy.LoadFromBytes([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	allowed, _ := pol.Evaluate("GET", "/repos/my-org/team-a/releases")
	if !allowed {
		t.Error("expected GET to be allowed")
	}

	allowed, reason := pol.Evaluate("POST", "/repos/my-org/team-a/releases")
	if allowed {
		t.Error("expected POST to be rejected")
	}
	if reason != policy.ReasonMethodNotAllowed {
		t.Errorf("expected reason %v, got %v", policy.ReasonMethodNotAllowed, reason)
	}
}

func TestStringOrArrayAndGlobMatching(t *testing.T) {
	yamlDoc := `
internal_rules:
  # Array of repos and array of paths
  - name: "multi-repo-glob"
    repo:
      - "my-org/app-1"
      - "my-org/app-2"
    path:
      - "/repos/my-org/*/releases/**"
      - "/repos/my-org/*/tags"
    methods: ["GET", "HEAD"]

  # Single string repo with wildcard
  - name: "wildcard-org"
    repo: "public-org/*"
    methods: ["GET"]

  # Path only (any repo)
  - name: "universal-downloads"
    path: "/repos/*/*/releases/download/**"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Multi-repo array match
	if ok, _ := pol.Evaluate("GET", "/repos/my-org/app-1/releases/v1.0.0"); !ok {
		t.Error("expected /repos/my-org/app-1/releases/v1.0.0 to be allowed")
	}
	if ok, _ := pol.Evaluate("GET", "/repos/my-org/app-2/tags"); !ok {
		t.Error("expected /repos/my-org/app-2/tags to be allowed")
	}

	// Wildcard org match
	if ok, _ := pol.Evaluate("GET", "/repos/public-org/any-repo/commits"); !ok {
		t.Error("expected /repos/public-org/any-repo/commits to be allowed")
	}

	// Universal download path match for unknown repo
	if ok, _ := pol.Evaluate("GET", "/repos/random-user/random-repo/releases/download/v1/asset.tar.gz"); !ok {
		t.Error("expected universal release download to be allowed")
	}
}

func TestRegexMatching(t *testing.T) {
	yamlDoc := `
internal_rules:
  - name: "regex-rules"
    repo_regex: "^my-org/(api|worker)-service$"
    path_regex: "^/repos/[^/]+/[^/]+/releases/.*$"
    methods: ["GET"]
`
	pol, err := policy.LoadFromBytes([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ok, _ := pol.Evaluate("GET", "/repos/my-org/api-service/releases/latest"); !ok {
		t.Error("expected api-service releases to match regex")
	}
	if ok, _ := pol.Evaluate("GET", "/repos/my-org/worker-service/releases/tag/v1"); !ok {
		t.Error("expected worker-service releases to match regex")
	}
	if ok, _ := pol.Evaluate("GET", "/repos/my-org/frontend-service/releases/latest"); ok {
		t.Error("expected frontend-service to NOT match regex")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && searchSubstring(s, substr)))
}

func searchSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
