package config

import (
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

// Expand aliases once before validation, retaining definition line numbers.
// Bound recursion and expansion so cyclic or exponentially reused anchors fail
// with a diagnostic instead of exhausting the loader's stack or memory.
func expand(n *yaml.Node, active map[*yaml.Node]bool, depth int, budget *int) (*yaml.Node, *Diagnostic) {
	if active[n] || depth > 100 || *budget <= 0 {
		return nil, &Diagnostic{Line: max(1, n.Line), Message: "cyclic alias or YAML expansion limit exceeded"}
	}
	*budget--
	active[n] = true
	defer delete(active, n)
	if n.Kind == yaml.AliasNode {
		return expand(n.Alias, active, depth+1, budget)
	}
	cloned := *n
	cloned.Content = nil
	for _, child := range n.Content {
		expanded, err := expand(child, active, depth+1, budget)
		if err != nil {
			return nil, err
		}
		cloned.Content = append(cloned.Content, expanded)
	}
	if cloned.Kind == yaml.MappingNode {
		return mergeMappings(&cloned)
	}
	return &cloned, nil
}

// YAML merge keys inherit fields; explicit fields override inherited ones and
// earlier mappings in a merge sequence take precedence, per YAML merge semantics.
func mergeMappings(n *yaml.Node) (*yaml.Node, *Diagnostic) {
	var direct, inherited []*yaml.Node
	seen := make(map[string]bool)
	var merge *yaml.Node
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.ShortTag() != "!!merge" {
			direct = append(direct, key, value)
			seen[key.Value] = true
			continue
		}
		if merge != nil {
			return nil, &Diagnostic{Line: key.Line, Message: "duplicate YAML merge key"}
		}
		merge = value
	}
	if merge != nil {
		sources := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			sources = merge.Content
		}
		for _, source := range sources {
			if source.Kind != yaml.MappingNode {
				return nil, &Diagnostic{Line: source.Line, Message: "YAML merge requires a mapping or list of mappings"}
			}
			for i := 0; i < len(source.Content); i += 2 {
				key, value := source.Content[i], source.Content[i+1]
				if !seen[key.Value] {
					inherited = append(inherited, key, value)
					seen[key.Value] = true
				}
			}
		}
	}
	n.Content = append(inherited, direct...)
	return n, nil
}

func (p *parser) fields(n *yaml.Node, path string, allowed ...string) map[string]*yaml.Node {
	fields := make(map[string]*yaml.Node)
	if n.Kind != yaml.MappingNode {
		p.error(n, path, "expected a mapping")
		return fields
	}
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			p.error(key, path, "expected a string key")
			continue
		}
		childPath := key.Value
		if path != "" {
			childPath = path + "." + childPath
		}
		if _, exists := fields[key.Value]; exists {
			p.error(key, childPath, "duplicate key")
		}
		fields[key.Value] = value
		known := false
		for _, name := range allowed {
			known = known || key.Value == name
		}
		if !known {
			p.error(key, childPath, "unknown key %q; did you mean %q?", key.Value, condition.Closest(key.Value, allowed))
		}
	}
	return fields
}

func (p *parser) conditionList(n *yaml.Node, path string) []condition.Expression {
	if n.Kind != yaml.SequenceNode {
		p.error(n, path, "expected a list of conditions")
		return nil
	}
	expressions := make([]condition.Expression, 0, len(n.Content))
	for i, child := range n.Content {
		expressions = append(expressions, p.expression(child, fmt.Sprintf("%s[%d]", path, i)))
	}
	return expressions
}

func (p *parser) expression(n *yaml.Node, path string) condition.Expression {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" {
		expression, err := condition.Parse(n.Value)
		if err != nil {
			p.error(n, path, "%s", err)
		}
		return expression
	}
	if n.Kind != yaml.MappingNode {
		p.error(n, path, "expected a condition string or an and/or/not mapping")
		return condition.Expression{}
	}
	fields := p.fields(n, path, "and", "or", "not")
	if len(n.Content) != 2 {
		p.error(n, path, "expected exactly one of and, or, not")
		return condition.Expression{}
	}
	for _, name := range []string{"and", "or", "not"} {
		if value := fields[name]; value != nil {
			if name == "not" {
				return condition.Not(p.expression(value, path+".not"))
			}
			children := p.conditionList(value, path+"."+name)
			if name == "and" {
				return condition.All(children...)
			}
			return condition.Any(children...)
		}
	}
	return condition.Expression{}
}
