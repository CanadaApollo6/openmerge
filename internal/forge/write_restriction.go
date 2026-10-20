package forge

import (
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// WriteRestrictionInput binds a dormant optional reader to the expected policy,
// complete selector set and protected refs. It contains no mutable team members.
// PR 3a will compile selectors into exact forge-specific ref patterns.
type WriteRestrictionInput struct {
	Repo          RepoRef
	Binding       authority.PolicyBinding
	BaseSelectors []authority.BaseSelector
	ExpectedRefs  []string
	AppID         string
}

// Validate checks repository and coverage bindings before an optional read.
func (in WriteRestrictionInput) Validate() error {
	if err := in.Binding.Validate(); err != nil {
		return err
	}
	if len(in.BaseSelectors) == 0 || len(in.ExpectedRefs) == 0 || in.AppID == "" {
		return fmt.Errorf("missing write restriction expectations")
	}
	if in.Repo.Path != in.Binding.Repository {
		return fmt.Errorf("repository binding mismatch")
	}
	coverage, err := authority.CoverageDigest(in.Binding.DefaultBranch, in.BaseSelectors)
	if err != nil {
		return err
	}
	if coverage != in.Binding.CoverageDigest {
		return fmt.Errorf("coverage binding mismatch")
	}
	for _, set := range [][]string{in.ExpectedRefs} {
		seen := map[string]bool{}
		for _, value := range set {
			if value == "" || seen[value] {
				return fmt.Errorf("invalid write restriction expectations")
			}
			seen[value] = true
		}
	}
	return nil
}

// WriteRestrictionReader only acquires typed facts, preserving partial reads,
// visibility and absence. It does not interpret them or mutate protection.
// No production driver implements or invokes this capability in PR 1.
type WriteRestrictionReader interface {
	ObserveWriteRestriction(context.Context, WriteRestrictionInput) (authority.Observation, error)
}

// WriteRestrictionInterpreter is a pure, forge-specific interpretation boundary.
// It cannot perform network I/O. PR 2 adds readers and PR 3a adds interpretation.
type WriteRestrictionInterpreter func(WriteRestrictionInput, authority.Observation) ([]authority.Finding, error)
