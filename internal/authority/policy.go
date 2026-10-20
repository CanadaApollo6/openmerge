package authority

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Declaration is the candidate configuration shape before an adapter resolves
// a break-glass actor. It is intentionally separate from Policy so an
// unresolved name cannot be used as an active authority policy.
type Declaration struct {
	Mode       Mode   `json:"policy_authority"`
	BreakGlass *Actor `json:"break_glass,omitempty"`
}

// Validate checks the mode-specific candidate contract. GitHub mode has no
// dormant break-glass actor, and OpenMerge mode requires exactly one actor.
func (d Declaration) Validate() error {
	if err := d.Mode.Validate(); err != nil {
		return err
	}
	switch d.Mode.Normalized() {
	case GitHub:
		if d.BreakGlass != nil {
			return fmt.Errorf("break_glass is only valid with policy_authority: openmerge")
		}
	case OpenMerge:
		if d.BreakGlass == nil {
			return fmt.Errorf("openmerge policy authority requires break_glass")
		}
		if err := d.BreakGlass.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Policy is an active authority declaration after the forge adapter has bound
// its declared break-glass actor to a stable forge identity.
type Policy struct {
	Mode       Mode         `json:"policy_authority"`
	BreakGlass ActorBinding `json:"break_glass"`
}

// Validate checks the mode-specific active authority contract.
func (p Policy) Validate() error {
	if err := p.Mode.Validate(); err != nil {
		return err
	}
	switch p.Mode.Normalized() {
	case GitHub:
		if p.BreakGlass.Declared.Team != "" || p.BreakGlass.Declared.RepositoryAdmin || p.BreakGlass.Resolved.Team != nil || p.BreakGlass.Resolved.RepositoryAdmin != nil {
			return fmt.Errorf("break_glass is only valid with policy_authority: openmerge")
		}
	case OpenMerge:
		if err := p.BreakGlass.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// RequiresPolicyOwnerApproval reports the additional authority-only owner
// obligation. It does not create an ordinary approval requirement, so a queue
// with no approvals policy retains its legacy nil meaning for non-policy work.
func (p Policy) RequiresPolicyOwnerApproval() bool {
	return p.Mode.Normalized() == OpenMerge
}

// MergeMethod is the merge construction semantics that enter an authority
// policy digest. It remains local to the pure authority contract, rather than
// importing queue or forge implementation types.
type MergeMethod string

// Merge methods whose object construction enters authority identity.
const (
	Merge       MergeMethod = "merge"
	Squash      MergeMethod = "squash"
	Rebase      MergeMethod = "rebase"
	FastForward MergeMethod = "fast-forward"
)

// Validate rejects unsupported merge construction semantics.
func (m MergeMethod) Validate() error {
	switch m {
	case Merge, Squash, Rebase, FastForward:
		return nil
	default:
		return fmt.Errorf("unsupported merge method %q", m)
	}
}

// BaseSelector is one configured queue base selector. DefaultBranch is an
// explicit sentinel for an omitted base: it is distinct from a literal base
// name and makes default-branch coverage deterministic.
type BaseSelector struct {
	DefaultBranch bool   `json:"default_branch"`
	Pattern       string `json:"pattern"`
}

func (s BaseSelector) validate() error {
	if s.DefaultBranch && s.Pattern != "" {
		return fmt.Errorf("default-branch selector must not include a pattern")
	}
	if !s.DefaultBranch && s.Pattern == "" {
		return fmt.Errorf("explicit base selector requires a pattern")
	}
	if strings.TrimSpace(s.Pattern) == "" && s.Pattern != "" {
		return fmt.Errorf("base selector must not be whitespace")
	}
	return nil
}

// DigestInputs binds authority-specific static policy to an existing effective
// queue digest. It excludes mutable team membership by construction.
type DigestInputs struct {
	LegacyDigest  string
	Policy        Policy
	DefaultBranch string
	MergeMethod   MergeMethod
	Selectors     []BaseSelector
}

// CoverageDigest identifies the complete configured base coverage. It is
// shared by policy pins and protection observations, and is independent of
// queue declaration order.
func CoverageDigest(defaultBranch string, selectors []BaseSelector) (string, error) {
	if strings.TrimSpace(defaultBranch) == "" {
		return "", fmt.Errorf("default branch is required")
	}
	canonical, err := canonicalSelectors(selectors)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Version       string
		DefaultBranch string
		Selectors     []BaseSelector
	}{"openmerge-authority-coverage-v1", defaultBranch, canonical})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// EffectiveDigest preserves the legacy digest byte-for-byte for omitted or
// GitHub authority. In OpenMerge mode it folds the legacy effective policy,
// the declared and resolved actor, mandatory governing-owner semantics, the
// default branch, merge method, and canonical full selector coverage.
func EffectiveDigest(in DigestInputs) (string, error) {
	if err := in.Policy.Validate(); err != nil {
		return "", err
	}
	if in.Policy.Mode.Normalized() == GitHub {
		return in.LegacyDigest, nil
	}
	if !IsDigest(in.LegacyDigest) {
		return "", fmt.Errorf("legacy effective digest must be a SHA-256 digest")
	}
	if err := in.MergeMethod.Validate(); err != nil {
		return "", err
	}
	coverage, err := CoverageDigest(in.DefaultBranch, in.Selectors)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalSelectors(in.Selectors)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Version                string
		LegacyEffectiveDigest  string
		BreakGlass             ActorBinding
		MandatoryPolicyOwners  string
		DefaultBranch          string
		MergeMethod            MergeMethod
		CoverageDigest         string
		CanonicalBaseSelectors []BaseSelector
	}{
		Version:                "openmerge-effective-authority-policy-v1",
		LegacyEffectiveDigest:  in.LegacyDigest,
		BreakGlass:             in.Policy.BreakGlass,
		MandatoryPolicyOwners:  mandatoryPolicyOwners,
		DefaultBranch:          in.DefaultBranch,
		MergeMethod:            in.MergeMethod,
		CoverageDigest:         coverage,
		CanonicalBaseSelectors: canonical,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func canonicalSelectors(selectors []BaseSelector) ([]BaseSelector, error) {
	if len(selectors) == 0 {
		return nil, fmt.Errorf("at least one base selector is required")
	}
	canonical := slices.Clone(selectors)
	for _, selector := range canonical {
		if err := selector.validate(); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(canonical, func(a, b BaseSelector) int {
		if a.DefaultBranch != b.DefaultBranch {
			if a.DefaultBranch {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Pattern, b.Pattern)
	})
	return slices.CompactFunc(canonical, func(a, b BaseSelector) bool {
		return a.DefaultBranch == b.DefaultBranch && a.Pattern == b.Pattern
	}), nil
}
