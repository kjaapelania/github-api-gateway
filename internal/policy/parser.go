package policy

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var validHTTPMethods = map[string]bool{
	"GET":     true,
	"HEAD":    true,
	"POST":    true,
	"PUT":     true,
	"DELETE":  true,
	"PATCH":   true,
	"OPTIONS": true,
}

// LoadFromFile parses a policy.yaml file from disk.
func LoadFromFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read policy file %s: %w", path, err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes parses policy YAML content from a byte slice.
func LoadFromBytes(data []byte) (*Policy, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse policy YAML: %w", err)
	}

	p := &Policy{}

	for i := range cfg.InternalRules {
		r := &cfg.InternalRules[i]
		if err := validateAndCompileRule(r); err != nil {
			return nil, fmt.Errorf("invalid internal rule %d (%s): %w", i, r.Name, err)
		}
		p.rules = append(p.rules, *r)
	}

	for i := range cfg.ExternalRules {
		r := &cfg.ExternalRules[i]
		if err := validateAndCompileRule(r); err != nil {
			return nil, fmt.Errorf("invalid external rule %d (%s): %w", i, r.Name, err)
		}
		p.rules = append(p.rules, *r)
	}

	return p, nil
}

func validateAndCompileRule(r *Rule) error {
	// Mutual exclusivity: repo vs repo_regex
	if len(r.Repo) > 0 && len(r.RepoRegex) > 0 {
		return fmt.Errorf("attributes 'repo' and 'repo_regex' are mutually exclusive")
	}

	// Mutual exclusivity: path vs path_regex
	if len(r.Path) > 0 && len(r.PathRegex) > 0 {
		return fmt.Errorf("attributes 'path' and 'path_regex' are mutually exclusive")
	}

	// Mandatory methods
	if len(r.Methods) == 0 {
		return fmt.Errorf("methods is mandatory in every rule")
	}

	// Validate each method
	for i, m := range r.Methods {
		upper := strings.ToUpper(strings.TrimSpace(m))
		if !validHTTPMethods[upper] {
			return fmt.Errorf("invalid HTTP method '%s'. Allowed methods: GET, HEAD, POST, PUT, DELETE, PATCH, OPTIONS", m)
		}
		r.Methods[i] = upper
	}

	// Compile repo regexes
	for _, pattern := range r.RepoRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid repo_regex '%s': %w", pattern, err)
		}
		r.compiledRepoRegex = append(r.compiledRepoRegex, re)
	}

	// Compile path regexes
	for _, pattern := range r.PathRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid path_regex '%s': %w", pattern, err)
		}
		r.compiledPathRegex = append(r.compiledPathRegex, re)
	}

	// Convert repo globs to regex
	for _, glob := range r.Repo {
		re, err := globToRegex(glob)
		if err != nil {
			return fmt.Errorf("invalid repo glob '%s': %w", glob, err)
		}
		r.compiledRepoRegex = append(r.compiledRepoRegex, re)
	}

	// Convert path globs to regex
	for _, glob := range r.Path {
		re, err := globToRegex(glob)
		if err != nil {
			return fmt.Errorf("invalid path glob '%s': %w", glob, err)
		}
		r.compiledPathRegex = append(r.compiledPathRegex, re)
	}

	return nil
}

// globToRegex converts simple glob patterns (* and **) into exact compiled regexes.
func globToRegex(glob string) (*regexp.Regexp, error) {
	// Escape special regex chars except *
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(glob) {
		if i+1 < len(glob) && glob[i] == '*' && glob[i+1] == '*' {
			// ** matches anything including slashes
			b.WriteString(".*")
			i += 2
			continue
		}
		if glob[i] == '*' {
			// * matches anything except slash
			b.WriteString("[^/]*")
			i++
			continue
		}
		ch := glob[i]
		if strings.ContainsRune(`.+?()|[]{}^$\`, rune(ch)) {
			b.WriteByte('\\')
		}
		b.WriteByte(ch)
		i++
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
