package forge

import (
	"context"
	"fmt"
	"strings"

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
		return fmt.Errorf("%w: %w", err, ErrInvalidInput)
	}
	if len(in.BaseSelectors) == 0 || len(in.ExpectedRefs) == 0 || in.AppID == "" {
		return invalid("missing write restriction expectations")
	}
	if in.Repo.Path != in.Binding.Repository {
		return invalid("repository binding mismatch")
	}
	if _, err := BranchRef(in.Binding.DefaultBranch); err != nil {
		return err
	}
	coverage, err := authority.CoverageDigest(in.Binding.DefaultBranch, in.BaseSelectors)
	if err != nil {
		return fmt.Errorf("%w: %w", err, ErrInvalidInput)
	}
	if coverage != in.Binding.CoverageDigest {
		return invalid("coverage binding mismatch")
	}
	seen := map[string]bool{}
	for _, ref := range in.ExpectedRefs {
		if err := ValidateRefName(ref); err != nil {
			return err
		}
		if !strings.HasPrefix(ref, "refs/heads/") || seen[ref] {
			return invalid("expected unique full branch refs")
		}
		seen[ref] = true
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
