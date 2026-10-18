package changepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// GoverningContent records content or proven absence, not blob/commit IDs.
// Unknown content cannot produce an authoritative policy digest.
type GoverningContent struct {
	Path           string
	Known, Present bool
	Content        string
}

// DigestInputs contains static semantic inputs for one queue. Other queues and
// config provenance are deliberately absent from the canonical encoding.
type DigestInputs struct {
	Repository        Repository
	Queue             CandidateQueue
	Resolved          []ResolvedIdentity
	Owners            GoverningContent
	Attributes        []GoverningContent
	ClassifierVersion string
}

func sortedSet(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}

func canonicalRequirement(r Requirement) Requirement {
	if r.Selector == Human {
		r.Selector, r.Human = AnyWriter, true
	}
	return r
}

// PolicyDigest returns no component for a legacy queue, even in a repository
// with opted-in siblings. Membership and current grants never enter this hash.
func PolicyDigest(in DigestInputs) (string, error) {
	if in.Queue.Approvals == nil {
		return "", nil
	}
	if err := in.Repository.ValidateIdentities(in.Resolved); err != nil {
		return "", err
	}
	if in.Queue.PullRequests != "" && in.Queue.PullRequests != "required" {
		return "", fmt.Errorf("stage A requires pull_requests: required")
	}
	if in.ClassifierVersion == "" {
		return "", fmt.Errorf("classifier version is required")
	}
	if err := in.Queue.Approvals.Default.Validate(); err != nil || in.Queue.Approvals.Default.Selector == None || in.Queue.Approvals.Default.Selector == CodeOwners {
		return "", fmt.Errorf("invalid default")
	}
	classification := in.Repository.Classification
	classification.Docs = sortedSet(classification.Docs)
	classification.Generated = sortedSet(classification.Generated)
	classification.Sensitive = sortedSet(classification.Sensitive)
	groups := map[string][]string{}
	for name, members := range in.Repository.Groups {
		groups[name] = sortedSet(members)
	}
	agents := append([]Agent{}, in.Repository.Agents...)
	for i := range agents {
		agents[i].Accountable = sortedSet(agents[i].Accountable)
	}
	slices.SortFunc(agents, func(a, b Agent) int { return strings.Compare(a.Identity, b.Identity) })
	resolved := append([]ResolvedIdentity{}, in.Resolved...)
	for i := range resolved {
		resolved[i].Name = strings.ToLower(resolved[i].Name)
	}
	slices.SortFunc(resolved, func(a, b ResolvedIdentity) int { return strings.Compare(a.Name, b.Name) })
	rules := make([]string, 0, len(in.Queue.Approvals.Rules))
	for _, rule := range in.Queue.Approvals.Rules {
		if err := rule.Require.Validate(); err != nil {
			return "", err
		}
		if _, err := MatchRule(rule.When, Classification{}, ""); err != nil {
			return "", err
		}
		rule.When.Labels = sortedSet(rule.When.Labels)
		rule.When.Paths = sortedSet(rule.When.Paths)
		rule.When.Touches = sortedSet(rule.When.Touches)
		authors := []string{}
		for _, author := range rule.When.Authors {
			authors = append(authors, string(author))
		}
		rule.When.Authors = []AccountKind{}
		for _, author := range sortedSet(authors) {
			rule.When.Authors = append(rule.When.Authors, AccountKind(author))
		}
		rule.Require = canonicalRequirement(rule.Require)
		data, err := json.Marshal(rule)
		if err != nil {
			return "", err
		}
		rules = append(rules, string(data))
	}
	contents := append([]GoverningContent{in.Owners}, in.Attributes...)
	seen := map[string]bool{}
	for _, content := range contents {
		if !content.Known || content.Path == "" || (!content.Present && content.Content != "") || seen[content.Path] {
			return "", fmt.Errorf("unresolved or duplicate governing content %q", content.Path)
		}
		seen[content.Path] = true
	}
	slices.SortFunc(contents, func(a, b GoverningContent) int { return strings.Compare(a.Path, b.Path) })
	canonical := struct {
		Version           string
		Classification    ClassificationSettings
		Default           Requirement
		Rules             []string
		Groups            map[string][]string
		Agents            []Agent
		Resolved          []ResolvedIdentity
		Contents          []GoverningContent
		ClassifierVersion string
	}{"openmerge-policy-v1", classification, canonicalRequirement(in.Queue.Approvals.Default), sortedSet(rules), groups, agents, resolved, contents, in.ClassifierVersion}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// WithPolicyDigest folds the component into the existing effective queue digest
// at future activation. No queue.Config field is added: the legacy serialized
// object and digest bytes remain exactly unchanged when the component is absent.
func WithPolicyDigest(legacy, policy string) (string, error) {
	if policy == "" {
		return legacy, nil
	}
	for _, value := range []string{legacy, policy} {
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != sha256.Size {
			return "", fmt.Errorf("expected a SHA-256 digest")
		}
	}
	data, err := json.Marshal(struct{ Version, Queue, Policy string }{"openmerge-effective-policy-v1", legacy, policy})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
