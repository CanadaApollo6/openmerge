package config

import (
	"errors"
	"fmt"
	"slices"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

// loadPolicy projects the already expanded YAML into the shared policy parser.
// Legacy conditions and planner fields retain their existing parser and defaults.
func loadPolicy(root *yaml.Node, cfg *Config, p *parser) {
	hasPolicy := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "classification", "groups", "agents":
			hasPolicy = true
		case "queues":
			for _, q := range root.Content[i+1].Content {
				for j := 0; j+1 < len(q.Content); j += 2 {
					if q.Content[j].Value == "approvals" || q.Content[j].Value == "changes" {
						hasPolicy = true
					}
				}
			}
		}
	}
	if !hasPolicy {
		return
	}
	policy := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: root.Line, Column: root.Column}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "classification", "groups", "agents":
			policy.Content = append(policy.Content, k, v)
		case "queues":
			qs := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Line: v.Line, Column: v.Column}
			for _, q := range v.Content {
				out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: q.Line, Column: q.Column}
				for j := 0; j+1 < len(q.Content); j += 2 {
					switch q.Content[j].Value {
					case "name", "changes", "approvals":
						out.Content = append(out.Content, q.Content[j], q.Content[j+1])
					}
				}
				qs.Content = append(qs.Content, out)
			}
			policy.Content = append(policy.Content, k, qs)
		}
	}
	var copyNode func(*yaml.Node) *yaml.Node
	copyNode = func(n *yaml.Node) *yaml.Node {
		out := *n
		out.Style &^= yaml.TaggedStyle
		out.Content = nil
		for _, child := range n.Content {
			out.Content = append(out.Content, copyNode(child))
		}
		return &out
	}
	parsed, _, err := cp.ParseCandidateNode(copyNode(policy))
	if err != nil {
		var diagnostic *cp.ParseError
		if errors.As(err, &diagnostic) {
			p.errors = append(p.errors, Diagnostic{Line: diagnostic.Line, Path: diagnostic.Path, Message: diagnostic.Message})
		} else {
			p.error(root, "", "%v", err)
		}
		return
	}
	cfg.Policy = parsed
	cfg.Classification, cfg.Groups, cfg.Agents = parsed.Classification, parsed.Groups, parsed.Agents
	for i, q := range parsed.Queues {
		if i < len(cfg.Queues) {
			cfg.Queues[i].Approvals = q.Approvals
			cfg.Queues[i].Changes.PullRequests = q.PullRequests
		}
	}
	for i, q := range cfg.Queues {
		if q.Approvals == nil {
			continue
		}
		for _, field := range []struct {
			name       string
			expression condition.Expression
		}{{"enter_when", q.EnterWhen}, {"merge_when", q.MergeWhen}} {
			if slices.Contains(field.expression.Attributes(), "approved-reviews-by") {
				p.warnings = append(p.warnings, Diagnostic{Line: q.baseLine, Path: fmt.Sprintf("queues[%d].%s", i, field.name), Message: cp.ReviewTermsDiagnostic})
			}
		}
	}
}
