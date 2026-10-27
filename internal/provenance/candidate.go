package provenance

import (
	"bytes"
	"fmt"
	"io"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

// Candidate holds future additions separately from the public configuration.
// config.Load does not import this package. The internal 4a seam cannot publish
// candidate additions through the public loader.
type Candidate struct {
	Config       config.Config
	Requirements map[string][]Rule
}

// LoadCandidate validates a full repository document by projecting only the
// provenance additions out before calling the public loader (ADR 0008's 9a
// pattern). Raw candidate input rejects aliases, merge keys and explicit tags;
// it is not a replacement public loader. Resolver attributes are candidate-only.
func LoadCandidate(data []byte) (Candidate, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return Candidate{}, err
	}
	if err := dec.Decode(&extra); err != io.EOF {
		return Candidate{}, fmt.Errorf("expected one YAML document")
	}
	if len(doc.Content) != 1 {
		return Candidate{}, fmt.Errorf("expected a mapping")
	}
	budget := 10000
	if err := inspectCandidate(doc.Content[0], 0, &budget); err != nil {
		return Candidate{}, err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return Candidate{}, fmt.Errorf("expected a mapping")
	}
	pending := map[int]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "queues" {
			continue
		}
		for qi, q := range root.Content[i+1].Content {
			if q.Kind != yaml.MappingNode {
				continue
			}
			var keep []*yaml.Node
			for j := 0; j < len(q.Content); j += 2 {
				if q.Content[j].Value == "provenance" {
					pending[qi] = q.Content[j+1]
				} else {
					keep = append(keep, q.Content[j], q.Content[j+1])
				}
			}
			q.Content = keep
		}
	}
	projected, err := yaml.Marshal(root)
	if err != nil {
		return Candidate{}, err
	}
	cfg, _, err := config.Load(projected)
	if err != nil {
		return Candidate{}, err
	}
	out := Candidate{Config: cfg, Requirements: map[string][]Rule{}}
	for i, n := range pending {
		rules, err := parseRequirements(n, cfg.Queues[i].Approvals != nil, cfg.Groups)
		if err != nil {
			return Candidate{}, fmt.Errorf("queues[%d].provenance: %w", i, err)
		}
		out.Requirements[cfg.Queues[i].Name] = rules
	}
	return out, nil
}

func inspectCandidate(n *yaml.Node, depth int, budget *int) error {
	*budget--
	if *budget < 0 || depth > 64 {
		return fmt.Errorf("candidate exceeds YAML limits")
	}
	if n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0 {
		return fmt.Errorf("aliases and explicit tags are not candidate inputs")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" || seen[k.Value] {
				return fmt.Errorf("line %d: invalid or duplicate key %q", k.Line, k.Value)
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := inspectCandidate(c, depth+1, budget); err != nil {
			return err
		}
	}
	return nil
}

func fields(n *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: expected a mapping", n.Line)
	}
	out := map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		valid := false
		for _, name := range allowed {
			valid = valid || name == k.Value
		}
		if !valid {
			return nil, fmt.Errorf("line %d: unknown key %q", k.Line, k.Value)
		}
		out[k.Value] = n.Content[i+1]
	}
	return out, nil
}

func parseRequirements(n *yaml.Node, enabled bool, groups map[string][]string) ([]Rule, error) {
	f, err := fields(n, "requirements")
	if err != nil {
		return nil, err
	}
	list := f["requirements"]
	if list == nil || list.Kind != yaml.SequenceNode || len(list.Content) == 0 {
		return nil, fmt.Errorf("requirements must be a nonempty list")
	}
	var out []Rule
	for _, item := range list.Content {
		f, err := fields(item, "when", "require")
		if err != nil {
			return nil, err
		}
		if f["when"] == nil || f["require"] == nil {
			return nil, fmt.Errorf("rule needs when and require")
		}
		when, err := parseExpression(f["when"])
		if err != nil {
			return nil, err
		}
		req, err := fields(f["require"], "conditions", "approvals")
		if err != nil {
			return nil, err
		}
		r := Rule{When: when}
		if n := req["conditions"]; n != nil {
			e, err := parseExpression(n)
			if err != nil {
				return nil, err
			}
			r.Conditions = &e
		}
		if n := req["approvals"]; n != nil {
			a, err := parseApproval(n, groups)
			if err != nil {
				return nil, err
			}
			r.Approvals = &a
		}
		if err := r.Validate(enabled, groups); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func parseExpression(n *yaml.Node) (condition.Expression, error) {
	if n.Kind == yaml.SequenceNode {
		return parseConditionList(n, false)
	}
	return parseConditionNode(n)
}

func parseConditionList(n *yaml.Node, anyOf bool) (condition.Expression, error) {
	if n.Kind != yaml.SequenceNode {
		return condition.Expression{}, fmt.Errorf("and/or needs a list")
	}
	var children []condition.Expression
	for _, child := range n.Content {
		e, err := parseConditionNode(child)
		if err != nil {
			return e, err
		}
		children = append(children, e)
	}
	if anyOf {
		return condition.Any(children...), nil
	}
	return condition.All(children...), nil
}

func parseConditionNode(n *yaml.Node) (condition.Expression, error) {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" {
		return ParseCondition(n.Value)
	}
	f, err := fields(n, "and", "or", "not")
	if err != nil {
		return condition.Expression{}, err
	}
	if len(f) != 1 {
		return condition.Expression{}, fmt.Errorf("expected one of and/or/not")
	}
	for op, v := range f {
		if op == "not" {
			e, err := parseConditionNode(v)
			return condition.Not(e), err
		}
		return parseConditionList(v, op == "or")
	}
	return condition.Expression{}, fmt.Errorf("expected an expression")
}

// Reuse the ADR 0007 selector parser instead of defining a second approval
// grammar. A synthetic rule permits code_owners; Validate rejects none later.
func parseApproval(n *yaml.Node, groups map[string][]string) (cp.Requirement, error) {
	var root yaml.Node
	projection := map[string]any{
		"groups": groups,
		"queues": []any{map[string]any{
			"name": "candidate",
			"approvals": map[string]any{
				"default": "any_writer",
				"rules":   []any{map[string]any{"when": map[string]any{"labels": []string{"docs"}}, "require": n}},
			},
		}},
	}
	if err := root.Encode(projection); err != nil {
		return cp.Requirement{}, err
	}
	parsed, _, err := cp.ParseCandidateNode(&root)
	if err != nil {
		return cp.Requirement{}, err
	}
	return parsed.Queues[0].Approvals.Rules[0].Require, nil
}
