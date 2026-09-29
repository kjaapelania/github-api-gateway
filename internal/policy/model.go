package policy

import (
	"fmt"
	"regexp"

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
