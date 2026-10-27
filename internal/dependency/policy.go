package dependency

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Policy is the candidate value of one queue's dependencies key. A nil policy
// means off. Empty grants allow no edges; there is no wildcard or inherited grant.
// This value is deliberately absent from public config structs and digests.
type Policy struct {
	Mode             string   `yaml:"mode"`
	AllowFrom        []string `yaml:"allow_from"`
	AllowTo          []string `yaml:"allow_to"`
	SingleMaintainer bool     `yaml:"single_maintainer"`
}

// Validate checks candidate syntax. Served scope is checked separately at use.
func (p Policy) Validate() error {
	if p.Mode != "ordered" {
		return fmt.Errorf("dependencies.mode must be ordered")
	}
	for _, list := range [][]string{p.AllowFrom, p.AllowTo} {
		seen := map[string]bool{}
		for _, repo := range list {
			if _, err := repositoryURL(repo); err != nil {
				return fmt.Errorf("invalid canonical repository URL: %w", err)
			}
			if seen[repo] {
				return fmt.Errorf("duplicate repository grant %s", repo)
			}
			seen[repo] = true
		}
	}
	return nil
}

// ParsePolicyCandidate parses only the value of a dependencies key, using the
// schema in testdata. It does not load or project a public configuration.
// Candidate inputs reject YAML aliases, explicit tags, unknown/duplicate fields,
// non-string grants and additional documents.
func ParsePolicyCandidate(data []byte) (Policy, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return Policy{}, err
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return Policy{}, fmt.Errorf("expected exactly one document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return Policy{}, fmt.Errorf("dependencies must be a mapping")
	}
	root := doc.Content[0]
	if root.Style&yaml.TaggedStyle != 0 {
		return Policy{}, fmt.Errorf("explicit tags are not candidate inputs")
	}
	p := Policy{}
	seen := map[string]bool{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || key.Style&yaml.TaggedStyle != 0 || seen[key.Value] {
			return Policy{}, fmt.Errorf("invalid or duplicate dependencies key")
		}
		seen[key.Value] = true
		if value.Style&yaml.TaggedStyle != 0 {
			return Policy{}, fmt.Errorf("explicit tags are not candidate inputs")
		}
		switch key.Value {
		case "single_maintainer":
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!bool" || (value.Value != "true" && value.Value != "false") {
				return Policy{}, fmt.Errorf("single_maintainer must be a boolean")
			}
			p.SingleMaintainer = value.Value == "true"
		case "mode":
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
				return Policy{}, fmt.Errorf("mode must be a string")
			}
			p.Mode = value.Value
		case "allow_from", "allow_to":
			if value.Kind != yaml.SequenceNode {
				return Policy{}, fmt.Errorf("%s must be a list", key.Value)
			}
			list := []string{}
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode || item.ShortTag() != "!!str" || item.Style&yaml.TaggedStyle != 0 {
					return Policy{}, fmt.Errorf("repository grants must be strings")
				}
				list = append(list, item.Value)
			}
			if key.Value == "allow_from" {
				p.AllowFrom = list
			} else {
				p.AllowTo = list
			}
		default:
			return Policy{}, fmt.Errorf("unknown dependencies key %q", key.Value)
		}
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	slices.Sort(p.AllowFrom)
	slices.Sort(p.AllowTo)
	return p, nil
}

// ValidatePolicyScope resolves every candidate grant against supplied served facts.
// A syntactically valid URL alone never grants access to an unserved repository.
func ValidatePolicyScope(p Policy, registry Registry) error {
	return registry.index().validatePolicyScope(p)
}

func (r registryIndex) validatePolicyScope(p Policy) error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if !name(r.Organization) {
		return ErrOutOfScope
	}
	if err := p.Validate(); err != nil {
		return err
	}
	for _, list := range [][]string{p.AllowFrom, p.AllowTo} {
		for _, repo := range list {
			if err := r.validateRepo(repo); err != nil {
				return err
			}
		}
	}
	return nil
}

// BilateralGrant checks B's allow_from and A's allow_to within the same served
// organization. Non-nil ordered policy enables dependencies for a queue; strict
// verification is a separate explicit input. This grants no other permission.
func BilateralGrant(registry Registry, consumer QueueKey, consumerPolicy *Policy, consumerStrict bool, source QueueKey, sourcePolicy *Policy, sourceStrict bool) error {
	if err := registry.Scope.Validate(); err != nil {
		return err
	}
	if err := bilateralGrant(consumer, consumerPolicy, consumerStrict, source, sourcePolicy, sourceStrict); err != nil {
		return err
	}
	index := registry.index()
	if err := index.validateGrantEndpoint(consumer, consumerPolicy); err != nil {
		return err
	}
	if err := index.validateGrantEndpoint(source, sourcePolicy); err != nil {
		return err
	}
	return nil
}

func (r registryIndex) validateGrantEndpoint(key QueueKey, policy *Policy) error {
	if key.Base == "" {
		return ErrUnresolved
	}
	if err := r.validateRepo(key.Repository); err != nil {
		return err
	}
	if policy == nil {
		return ErrOutOfScope
	}
	return r.validatePolicyScope(*policy)
}

// bilateralGrant checks consent only; callers separately validate queue/policy
// scope. Evaluate caches that validation by node instead of reparsing grants.
func bilateralGrant(consumer QueueKey, consumerPolicy *Policy, consumerStrict bool, source QueueKey, sourcePolicy *Policy, sourceStrict bool) error {
	if strings.EqualFold(consumer.Repository, source.Repository) || !consumerStrict || !sourceStrict || consumerPolicy == nil || sourcePolicy == nil {
		return ErrOutOfScope
	}
	if !slices.Contains(consumerPolicy.AllowFrom, source.Repository) || !slices.Contains(sourcePolicy.AllowTo, consumer.Repository) {
		return ErrOutOfScope
	}
	return nil
}
