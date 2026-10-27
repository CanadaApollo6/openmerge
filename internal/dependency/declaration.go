package dependency

import (
	"slices"
	"strings"
)

// BindDeclaration compares a poll's final semantic declaration to the recorded
// alias basis. Editors must be coalesced over the whole poll, not just the final
// webhook. Previously retained aliases/baselines never follow a new registry.
// Nil event means no declaration observation is needed for an unrelated body edit.
// The returned fence is checked again by Apply, so two readers cannot rebind it.
func BindDeclaration(s State, read DependenciesDeclared, inputs []Input, registry Registry, baselines map[string]Baseline) (*DependenciesDeclared, error) {
	if s.initialization == nil || registry.Scope != s.initialization.scope() {
		return nil, ErrOutOfScope
	}
	if len(inputs) > MaxInputs {
		return nil, ErrLimit
	}
	read.Namespace = s.initialization.Namespace
	prior, _ := s.Declaration(read.Alias)
	retained := map[string]DeclaredEdge{}
	for _, edge := range prior.Edges {
		retained[edge.Reference] = edge
	}
	aliases := map[string]string{}
	for _, b := range prior.Bindings {
		aliases[b.Input] = b.Reference
	}
	edges := map[string]DeclaredEdge{}
	bindings := map[string]string{}
	refs := []Reference{}
	for _, input := range inputs {
		var ref Reference
		if input.Alias != "" && input.Reference != (Reference{}) {
			return nil, ErrMalformed
		}
		if bound, ok := aliases[input.Alias]; input.Alias != "" && ok {
			var err error
			ref, err = ParseReference(bound, registry.Scope)
			if err != nil {
				return nil, err
			}
		} else {
			resolved, err := Resolve([]Input{input}, registry)
			if err != nil {
				return nil, err
			}
			ref = resolved[0]
		}
		raw, _ := ref.Canonical()
		edge, ok := retained[raw]
		if !ok {
			edge = DeclaredEdge{Reference: raw, Baseline: baselines[raw]}
			for _, binding := range registry.Bindings {
				if binding.Reference.ChangeID == ref.ChangeID && binding.Reference.Scope == registry.Scope {
					edge.Queue = binding.Queue
				}
			}
			if ref.Revision > 0 {
				edge.PinnedHead = edge.Baseline.Head
			}
		}
		edges[raw] = edge
		refs = append(refs, ref)
		if input.Alias != "" {
			bindings[input.Alias] = raw
		}
	}
	normalized, err := Normalize(refs, registry.Scope)
	if err != nil {
		return nil, err
	}
	read.Edges = []DeclaredEdge{}
	for _, ref := range normalized {
		raw, _ := ref.Canonical()
		read.Edges = append(read.Edges, edges[raw])
	}
	slices.SortFunc(read.Edges, func(a, b DeclaredEdge) int { return strings.Compare(a.Reference, b.Reference) })
	read.Bindings = []AliasBinding{}
	for input, ref := range bindings {
		read.Bindings = append(read.Bindings, AliasBinding{input, ref})
	}
	slices.SortFunc(read.Bindings, func(a, b AliasBinding) int { return strings.Compare(a.Input, b.Input) })
	read.Digest = DeclarationDigest(read.Edges)
	read.Prior = s.Prior(read.Alias)
	if read.Prior.EventID > 0 && prior.Digest == read.Digest && prior.ChangeID == read.ChangeID && prior.Revision == read.Revision && sameAccountability(prior.Accountability, read.Accountability) && slices.Equal(prior.Bindings, read.Bindings) && !read.Redeclaration {
		return nil, nil
	}
	read.Hold = s.declarationHold(read)
	if err := read.Validate(); err != nil {
		return nil, err
	}
	return &read, nil
}
