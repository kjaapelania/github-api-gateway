package policy

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DenialReason explains why a request was rejected or allowed.
type DenialReason string

const (
	ReasonAllowed             DenialReason = "ALLOWED"
	ReasonDeniedSensitivePath DenialReason = "DENIED_SENSITIVE_PATH"
	ReasonDefaultDeny         DenialReason = "DENIED_DEFAULT_DENY"
	ReasonMethodNotAllowed    DenialReason = "DENIED_METHOD_NOT_ALLOWED"
)

// StringOrSlice is a helper type that unmarshals either a single string or a slice of strings.
type StringOrSlice []string

// UnmarshalYAML implements custom unmarshaling for StringOrSlice.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		var items []string
		for _, item := range value.Content {
			items = append(items, item.Value)
		}
		*s = items
		return nil
	default:
		return fmt.Errorf("expected string or list of strings, got node kind %d", value.Kind)
	}
}

// Rule represents a single granular policy rule.
type Rule struct {
	Name      string        `yaml:"name"`
	Repo      StringOrSlice `yaml:"repo"`
	RepoRegex StringOrSlice `yaml:"repo_regex"`
	Path      StringOrSlice `yaml:"path"`
	PathRegex StringOrSlice `yaml:"path_regex"`
	Methods   []string      `yaml:"methods"`

	// Pre-compiled regexes for performance
	compiledRepoRegex []*regexp.Regexp
	compiledPathRegex []*regexp.Regexp
}

// Config represents the raw structure of policy.yaml.
type Config struct {
	InternalRules []Rule `yaml:"internal_rules"`
	ExternalRules []Rule `yaml:"external_rules"`
}

// MaxPolicySizeBytes is the maximum allowed size (10MB) for a remote policy payload.
const MaxPolicySizeBytes = 10 * 1024 * 1024

// PolicySource represents the origin and cache metadata of the gateway access policy.
type PolicySource struct {
	URI          string
	IsRemote     bool
	LastETag     string
	LastHash     [32]byte
	LastModified time.Time
	RawContent   []byte
}

// NewPolicySource initializes a PolicySource and validates the URI.
func NewPolicySource(uri string) (*PolicySource, error) {
	if uri == "" {
		return nil, fmt.Errorf("policy URI must not be empty")
	}

	isRemote := false
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		isRemote = true
		if _, err := url.ParseRequestURI(uri); err != nil {
			return nil, fmt.Errorf("invalid remote policy URL %q: %w", uri, err)
		}
	}

	return &PolicySource{
		URI:      uri,
		IsRemote: isRemote,
	}, nil
}

// ReconciliationConfig defines the parameters of the pull-based background synchronizer.
type ReconciliationConfig struct {
	PollInterval time.Duration
	Enabled      bool
	Timeout      time.Duration
}

// AdminConfig defines administrative perimeter and authentication rules.
type AdminConfig struct {
	AllowRemoteAdmin bool
	AdminToken       string
}
