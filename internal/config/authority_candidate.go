package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"go.yaml.in/yaml/v3"
)

// AuthorityCandidate is the future authority configuration paired with the
// current public configuration. It is intentionally candidate-only: Load and
// the published schema continue rejecting these keys until ADR 0008 PR 9.
type AuthorityCandidate struct {
	Config    Config
	Authority authority.Declaration
}

// BaseSelectors returns the full configured coverage set. An omitted queue
// base remains an explicit default-branch sentinel, so a caller can fold the
// eventual resolved default branch into an authority digest without losing the
// distinction from a literal selector.
func (c AuthorityCandidate) BaseSelectors() []authority.BaseSelector {
	selectors := make([]authority.BaseSelector, 0, len(c.Config.Queues))
	for _, q := range c.Config.Queues {
		selectors = append(selectors, authority.BaseSelector{DefaultBranch: q.Base == "", Pattern: q.Base})
	}
	return selectors
}

// LoadAuthorityCandidate validates the current configuration after removing
// the two future keys, then validates their proposed grammar separately. It
// does not resolve actors, activate authority, or alter Config.Load.
func LoadAuthorityCandidate(data []byte) (AuthorityCandidate, []Diagnostic, error) {
	root, err := authorityCandidateRoot(data)
	if err != nil {
		return AuthorityCandidate{}, nil, err
	}
	declaration, candidateErrors := parseAuthorityDeclaration(root)
	publicData, err := authorityCandidatePublicYAMLFromRoot(root)
	if err != nil {
		return AuthorityCandidate{}, nil, err
	}
	cfg, warnings, publicErr := Load(publicData)
	if len(candidateErrors) != 0 {
		return AuthorityCandidate{}, nil, candidateErrors
	}
	if publicErr != nil {
		return AuthorityCandidate{}, nil, publicErr
	}
	candidate := AuthorityCandidate{Config: cfg, Authority: declaration}
	// The default branch is resolved later; validate explicit selectors now.
	if declaration.Mode.Normalized() == authority.OpenMerge {
		if _, err := authority.CompileRefCoverage("candidate-default", candidate.BaseSelectors()); err != nil {
			return AuthorityCandidate{}, nil, err
		}
	}
	return candidate, warnings, nil
}

// authorityCandidatePublicYAML is test-visible only within this package. It
// produces the exact current-config projection used by LoadAuthorityCandidate,
// so the candidate future schema can be checked alongside the published one.
func authorityCandidatePublicYAML(data []byte) ([]byte, error) {
	root, err := authorityCandidateRoot(data)
	if err != nil {
		return nil, err
	}
	return authorityCandidatePublicYAMLFromRoot(root)
}

func authorityCandidateRoot(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Errors{{Line: 1, Message: "version: 1 and at least one queue are required"}}
		}
		return nil, syntaxError(err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, syntaxError(err)
		}
		return nil, Errors{{Line: max(1, extra.Line), Message: "expected exactly one YAML document"}}
	}
	if len(doc.Content) == 0 {
		return nil, Errors{{Line: 1, Message: "version: 1 and at least one queue are required"}}
	}
	budget := 10000
	root, diagnostic := expand(doc.Content[0], make(map[*yaml.Node]bool), 0, &budget)
	if diagnostic != nil {
		return nil, Errors{*diagnostic}
	}
	return root, nil
}

func authorityCandidatePublicYAMLFromRoot(root *yaml.Node) ([]byte, error) {
	if root.Kind != yaml.MappingNode {
		return yaml.Marshal(root)
	}
	projected := *root
	projected.Content = make([]*yaml.Node, 0, len(root.Content))
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind == yaml.ScalarNode && (key.Value == "policy_authority" || key.Value == "break_glass") {
			continue
		}
		projected.Content = append(projected.Content, key, value)
	}
	return yaml.Marshal(&projected)
}

func parseAuthorityDeclaration(root *yaml.Node) (authority.Declaration, Errors) {
	var diagnostics Errors
	if root.Kind != yaml.MappingNode {
		return authority.Declaration{}, Errors{{Line: max(1, root.Line), Message: "expected a mapping"}}
	}
	var modeNode, actorNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			continue // Current Config.Load reports invalid root keys separately.
		}
		switch key.Value {
		case "policy_authority":
			modeNode = value
		case "break_glass":
			actorNode = value
		}
	}
	declaration := authority.Declaration{}
	if modeNode != nil {
		if modeNode.Kind != yaml.ScalarNode || modeNode.ShortTag() != "!!str" {
			diagnostics = append(diagnostics, Diagnostic{Line: max(1, modeNode.Line), Path: "policy_authority", Message: "expected github or openmerge"})
		} else {
			declaration.Mode = authority.Mode(modeNode.Value)
			if modeNode.Value == "" {
				diagnostics = append(diagnostics, Diagnostic{Line: max(1, modeNode.Line), Path: "policy_authority", Message: "expected github or openmerge"})
			} else if err := declaration.Mode.Validate(); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Line: max(1, modeNode.Line), Path: "policy_authority", Message: err.Error()})
			}
		}
	}
	if actorNode != nil {
		actor, actorErrors := parseAuthorityActor(actorNode)
		diagnostics = append(diagnostics, actorErrors...)
		declaration.BreakGlass = &actor
	}
	if err := declaration.Validate(); err != nil {
		line, path := max(1, root.Line), "policy_authority"
		if actorNode != nil {
			line, path = max(1, actorNode.Line), "break_glass"
		} else if modeNode != nil {
			line = max(1, modeNode.Line)
		}
		diagnostics = append(diagnostics, Diagnostic{Line: line, Path: path, Message: err.Error()})
	}
	return declaration, diagnostics
}

func parseAuthorityActor(node *yaml.Node) (authority.Actor, Errors) {
	var diagnostics Errors
	if node.Kind != yaml.MappingNode {
		return authority.Actor{}, Errors{{Line: max(1, node.Line), Path: "break_glass", Message: "expected a mapping"}}
	}
	var actor authority.Actor
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			diagnostics = append(diagnostics, Diagnostic{Line: max(1, key.Line), Path: "break_glass", Message: "expected a string key"})
			continue
		}
		path := "break_glass." + key.Value
		switch key.Value {
		case "team":
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || value.Value == "" {
				diagnostics = append(diagnostics, Diagnostic{Line: max(1, value.Line), Path: path, Message: "expected a nonempty org/team string"})
				continue
			}
			actor.Team = value.Value
		case "repository_admin":
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!bool" || value.Value != "true" {
				diagnostics = append(diagnostics, Diagnostic{Line: max(1, value.Line), Path: path, Message: "must be true"})
				continue
			}
			actor.RepositoryAdmin = true
		default:
			diagnostics = append(diagnostics, Diagnostic{Line: max(1, key.Line), Path: path, Message: fmt.Sprintf("unknown key %q", key.Value)})
		}
	}
	if err := actor.Validate(); err != nil {
		diagnostics = append(diagnostics, Diagnostic{Line: max(1, node.Line), Path: "break_glass", Message: err.Error()})
	}
	return actor, diagnostics
}
