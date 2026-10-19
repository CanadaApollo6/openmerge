package changepolicy

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

// ClassificationSettings uses OpenMerge's whole-path glob dialect.
type ClassificationSettings struct {
	Docs, Generated, Sensitive []string
}

// Repository contains the approval-policy component of a loaded configuration.
type Repository struct {
	Classification ClassificationSettings
	Groups         map[string][]string
	Agents         []Agent
	Queues         []CandidateQueue
}

// CandidateQueue is a policy component, not a replacement for config.Queue.
type CandidateQueue struct {
	Name         string
	PullRequests string
	Approvals    *Approvals
}

// ReviewTermsDiagnostic is shared by candidate parsing and serve assembly.
const ReviewTermsDiagnostic = "approvals changes review eligibility; use explicit pools and move review terms into approvals rules"

// Diagnostic is a candidate-only warning; activation maps it to config's DTO.
type Diagnostic struct {
	Line          int
	Path, Message string
}

// ParseError identifies the original policy node and its YAML path.
type ParseError struct {
	Line          int
	Path, Message string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s: %s", e.Line, e.Path, e.Message) }

type candidateParser struct {
	err   error
	paths map[*yaml.Node]string
}

func (p *candidateParser) fail(n *yaml.Node, message string) {
	if p.err == nil {
		p.err = &ParseError{Line: max(1, n.Line), Path: p.paths[n], Message: message}
	}
}

func (p *candidateParser) fields(n *yaml.Node, allowed ...string) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n.Kind != yaml.MappingNode {
		p.fail(n, "expected a mapping")
		return out
	}
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.ShortTag() != "!!str" || !slices.Contains(allowed, key.Value) {
			p.fail(key, "unknown key "+key.Value)
		}
		if _, exists := out[key.Value]; exists {
			p.fail(key, "duplicate key "+key.Value)
		}
		out[key.Value] = value
	}
	return out
}

func (p *candidateParser) text(n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || strings.TrimSpace(n.Value) == "" {
		p.fail(n, "expected a nonempty string")
	}
	return n.Value
}

func (p *candidateParser) list(n *yaml.Node) []string {
	var out []string
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.fail(n, "expected a nonempty list")
		return out
	}
	for _, item := range n.Content {
		out = append(out, p.text(item))
	}
	return out
}

func (p *candidateParser) globs(n *yaml.Node) []string {
	out := p.list(n)
	for i, glob := range out {
		if _, err := condition.CompileGlob(glob); err != nil {
			p.fail(n.Content[i], "invalid glob: "+err.Error())
		}
	}
	return out
}

func (p *candidateParser) require(n *yaml.Node) Requirement {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" {
		switch n.Value {
		case "none":
			return Requirement{Selector: None}
		case "any_writer":
			return Requirement{Selector: AnyWriter, Count: 1}
		}
		p.fail(n, "expected none, any_writer or a selector object")
		return Requirement{}
	}
	f := p.fields(n, RequirementKeys()...)
	if v := f["code_owners"]; v != nil {
		r := Requirement{Selector: CodeOwners}
		if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!bool" || v.Value != "true" {
			p.fail(v, "code_owners must be true")
		}
		for key, value := range f {
			if key != "code_owners" && key != "receipt" {
				p.fail(value, "code_owners permits only receipt")
			}
		}
		if v := f["receipt"]; v != nil {
			if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!bool" || v.Value != "true" {
				p.fail(v, "receipt must be true")
			}
			r.Receipt = true
		}
		return r
	}
	r := Requirement{Count: 1}
	if v := f["group"]; v != nil {
		r.Selector, r.Group = Group, p.text(v)
	}
	for _, key := range []string{"any_writer", "human", "receipt"} {
		if v := f[key]; v != nil {
			if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!bool" || v.Value != "true" {
				p.fail(v, key+" must be true")
			}
			switch key {
			case "any_writer":
				if r.Selector != "" {
					p.fail(v, "group and any_writer are mutually exclusive")
				}
				r.Selector = AnyWriter
			case "human":
				r.Human = true
			case "receipt":
				r.Receipt = true
			}
		}
	}
	if r.Selector == "" && r.Human {
		r.Selector, r.Human = Human, false
	}
	if v := f["count"]; v != nil {
		if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!int" || v.Decode(&r.Count) != nil {
			p.fail(v, "count must be a positive integer")
		}
	}
	if err := r.Validate(); err != nil {
		where := n
		if r.Count < 1 && f["count"] != nil {
			where = f["count"]
		}
		p.fail(where, err.Error())
	}
	return r
}

func (p *candidateParser) match(n *yaml.Node) Match {
	f := p.fields(n, "labels", "paths", "touches", "authors")
	if len(f) == 0 {
		p.fail(n, "when must contain at least one facet")
	}
	m := Match{}
	if v := f["labels"]; v != nil {
		m.Labels = p.list(v)
		for i, label := range m.Labels {
			if !ValidClassificationLabel(label) {
				message := "invalid classification label " + label
				if strings.HasPrefix(label, "owned:user:") || strings.HasPrefix(label, "owned:team:") {
					message += "; expected canonical ASCII login or org/team"
				}
				p.fail(v.Content[i], message)
			}
		}
	}
	if v := f["paths"]; v != nil {
		m.Paths = p.globs(v)
	}
	if v := f["touches"]; v != nil {
		m.Touches = p.globs(v)
	}
	if v := f["authors"]; v != nil {
		for _, kind := range p.list(v) {
			if kind != string(HumanAccount) && kind != string(AgentAccount) {
				p.fail(v, "authors must contain human or agent")
			}
			m.Authors = append(m.Authors, AccountKind(kind))
		}
	}
	return m
}

// ValidClassificationLabel reports the supported facet and canonical owner-token grammar.
func ValidClassificationLabel(label string) bool {
	switch label {
	case "docs", "generated", "policy", "unknown":
		return true
	}
	if name, ok := strings.CutPrefix(label, "owned:user:"); ok {
		return name == ascii.Lower(name) && ValidLogin(name)
	}
	if name, ok := strings.CutPrefix(label, "owned:team:"); ok {
		return name == ascii.Lower(name) && teamPattern.MatchString(name)
	}
	if name, ok := strings.CutPrefix(label, "owned:email:"); ok {
		local, domain, found := strings.Cut(name, "@")
		return found && local != "" && domain != "" && !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) && !strings.Contains(domain, "@") && domain == strings.ToLower(domain)
	}
	return false
}

func (p *candidateParser) approvals(n *yaml.Node) *Approvals {
	f := p.fields(n, "default", "rules")
	a := &Approvals{}
	if v := f["default"]; v != nil {
		a.Default = p.require(v)
		if a.Default.Selector == None || a.Default.Selector == CodeOwners {
			p.fail(v, "approvals.default cannot be none or code_owners")
		}
	} else {
		p.fail(n, "approvals.default is required")
	}
	if v := f["rules"]; v != nil {
		if v.Kind != yaml.SequenceNode {
			p.fail(v, "rules must be a list")
		}
		for _, item := range v.Content {
			rule := p.fields(item, "when", "require")
			if rule["when"] == nil || rule["require"] == nil {
				p.fail(item, "rule requires when and require")
				continue
			}
			a.Rules = append(a.Rules, Rule{When: p.match(rule["when"]), Require: p.require(rule["require"])})
		}
	}
	return a
}

// ParseCandidate parses the policy-only grammar used by component tests and the
// public loader's projection. It accepts no legacy planner settings. Raw inputs
// reject aliases and explicit tags; the public loader expands YAML first.
func ParseCandidate(data []byte) (Repository, []Diagnostic, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return Repository{}, nil, err
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return Repository{}, nil, fmt.Errorf("expected exactly one document")
	}
	if len(doc.Content) == 0 {
		return Repository{}, nil, fmt.Errorf("candidate mapping is required")
	}
	return ParseCandidateNode(doc.Content[0])
}

// ParseCandidateNode parses a policy projection while retaining original source
// coordinates. Callers must expand aliases and remove explicit tag styles first.
func ParseCandidateNode(root *yaml.Node) (Repository, []Diagnostic, error) {
	p := &candidateParser{paths: map[*yaml.Node]string{}}
	// Reserve the document node counted by the original byte parser.
	budget := 9999
	var inspect func(*yaml.Node, int, string)
	inspect = func(n *yaml.Node, depth int, path string) {
		p.paths[n] = path
		budget--
		if budget < 0 || depth > 64 {
			p.fail(n, "candidate exceeds YAML limits")
			return
		}
		if n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0 {
			p.fail(n, "aliases and explicit tags are not candidate inputs")
		}
		for i, child := range n.Content {
			childPath := path
			switch n.Kind {
			case yaml.MappingNode:
				if path != "" {
					childPath += "."
				}
				childPath += n.Content[i-i%2].Value
			case yaml.SequenceNode:
				childPath += fmt.Sprintf("[%d]", i)
			}
			inspect(child, depth+1, childPath)
		}
	}
	inspect(root, 1, "")
	if p.err != nil {
		return Repository{}, nil, p.err
	}
	f := p.fields(root, "classification", "groups", "agents", "queues")
	r := Repository{Groups: map[string][]string{}}
	if n := f["classification"]; n != nil {
		for key, v := range p.fields(n, "docs", "generated", "sensitive") {
			values := p.globs(v)
			switch key {
			case "docs":
				r.Classification.Docs = values
			case "generated":
				r.Classification.Generated = values
			case "sensitive":
				r.Classification.Sensitive = values
			}
		}
	}
	if n := f["groups"]; n != nil {
		keys := []string{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			keys = append(keys, n.Content[i].Value)
		}
		for name, v := range p.fields(n, keys...) {
			if name == "" || strings.TrimSpace(name) != name {
				p.fail(n, "group name must be nonempty and have no surrounding whitespace")
			}
			members := p.list(v)
			for i, member := range members {
				if !ValidMember(member) {
					p.fail(v.Content[i], "invalid member (expected ASCII login or org/team) "+member)
				}
				members[i] = ascii.Lower(member)
			}
			r.Groups[name] = members
		}
	}
	if n := f["agents"]; n != nil {
		if n.Kind != yaml.SequenceNode {
			p.fail(n, "agents must be a list")
		}
		seen := map[string]bool{}
		for _, item := range n.Content {
			a := p.fields(item, "identity", "accountable")
			if a["identity"] == nil || a["accountable"] == nil {
				p.fail(item, "agent requires identity and accountable")
				continue
			}
			identity := ascii.Lower(p.text(a["identity"]))
			if !ValidLogin(identity) || seen[identity] {
				p.fail(a["identity"], "invalid or duplicate agent identity (expected ASCII login) "+identity)
			}
			seen[identity] = true
			accountable := p.list(a["accountable"])
			for i, name := range accountable {
				if !ValidLogin(name) {
					p.fail(a["accountable"].Content[i], "invalid accountable login (expected ASCII login) "+name)
				}
				accountable[i] = ascii.Lower(name)
			}
			r.Agents = append(r.Agents, Agent{Identity: identity, Accountable: accountable})
		}
	}
	n := f["queues"]
	if n == nil {
		return Repository{}, nil, fmt.Errorf("queues is required")
	}
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.fail(n, "queues must be a nonempty list")
	}
	seen := map[string]bool{}
	var diagnostics []Diagnostic
	for i, item := range n.Content {
		qf := p.fields(item, "name", "changes", "approvals", "enter_when", "merge_when")
		q := CandidateQueue{PullRequests: "required"}
		if v := qf["name"]; v != nil {
			q.Name = p.text(v)
		} else {
			p.fail(item, "queue name is required")
		}
		if seen[q.Name] {
			p.fail(item, "duplicate queue name "+q.Name)
		}
		seen[q.Name] = true
		if v := qf["changes"]; v != nil {
			c := p.fields(v, "pull_requests")
			if mode := c["pull_requests"]; mode != nil {
				q.PullRequests = p.text(mode)
				if q.PullRequests != "required" {
					p.fail(mode, "Stage A requires pull_requests: required")
				}
			}
		}
		if v := qf["approvals"]; v != nil {
			q.Approvals = p.approvals(v)
		}
		for _, field := range []string{"enter_when", "merge_when"} {
			if v := qf[field]; v != nil {
				expr := p.conditions(v)
				if q.Approvals != nil && slices.Contains(expr.Attributes(), "approved-reviews-by") {
					diagnostics = append(diagnostics, Diagnostic{Line: v.Line, Path: fmt.Sprintf("queues[%d].%s", i, field), Message: ReviewTermsDiagnostic})
				}
			}
		}
		if q.Approvals != nil {
			requirements := []Requirement{q.Approvals.Default}
			for _, rule := range q.Approvals.Rules {
				requirements = append(requirements, rule.Require)
			}
			for _, requirement := range requirements {
				if requirement.Selector == Group {
					if _, ok := r.Groups[requirement.Group]; !ok {
						p.fail(item, "unknown group "+requirement.Group)
					}
				}
			}
		}
		r.Queues = append(r.Queues, q)
	}
	if p.err != nil {
		return Repository{}, nil, p.err
	}
	return r, diagnostics, nil
}

func (p *candidateParser) conditions(n *yaml.Node) condition.Expression {
	if n.Kind != yaml.SequenceNode {
		p.fail(n, "conditions must be a list")
		return condition.Expression{}
	}
	var children []condition.Expression
	for _, child := range n.Content {
		children = append(children, p.expression(child))
	}
	return condition.All(children...)
}

func (p *candidateParser) expression(n *yaml.Node) condition.Expression {
	if n.Kind == yaml.ScalarNode {
		expr, err := condition.Parse(p.text(n))
		if err != nil {
			p.fail(n, err.Error())
		}
		return expr
	}
	f := p.fields(n, "and", "or", "not")
	if len(f) != 1 {
		p.fail(n, "condition requires exactly one logical operator")
	}
	if v := f["not"]; v != nil {
		return condition.Not(p.expression(v))
	}
	if v := f["and"]; v != nil {
		return p.conditions(v)
	}
	if v := f["or"]; v != nil {
		if v.Kind != yaml.SequenceNode {
			p.fail(v, "or requires a list")
		}
		var children []condition.Expression
		for _, child := range v.Content {
			children = append(children, p.expression(child))
		}
		return condition.Any(children...)
	}
	return condition.Expression{}
}
