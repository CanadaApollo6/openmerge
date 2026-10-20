package forge

import (
	"context"
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// PushBinding ties a transient authorization to a durable queue policy and
// protection revision. QueueKey is the canonical durable queue identity.
type PushBinding struct {
	Repository            RepoRef
	QueueKey              string
	GoverningCommit       string
	EffectiveDigest       string
	ProtectionBasis       uint64
	ProtectionFingerprint string
}

// PushValidationContext owns immutable copies of policy and intended writes.
// Construct it with NewPushValidationContext; zero is intentionally invalid.
type PushValidationContext struct {
	binding   PushBinding
	operation PushOperation
	refs      []PushRefIntent
	rules     PushRuleContext
}

// NewPushValidationContext freezes the policy snapshot before dispatch.
func NewPushValidationContext(binding PushBinding, operation PushOperation, refs []PushRefIntent, rules PushRuleContext) (PushValidationContext, error) {
	if binding.Repository.Path == "" || binding.QueueKey == "" || ValidateCommitIDs(binding.GoverningCommit) != nil || !authority.IsDigest(binding.EffectiveDigest) {
		return PushValidationContext{}, pushRejected("missing policy binding")
	}
	fingerprint, err := rules.Observation.Fingerprint()
	if err != nil || fingerprint != binding.ProtectionFingerprint || rules.AppID == "" || rules.DefaultBranch == "" {
		return PushValidationContext{}, pushRejected("invalid protection binding")
	}
	switch operation {
	case PushBuild, PushLand, PushEnsureFence, PushAdvanceFence, PushDeleteBranch:
	default:
		return PushValidationContext{}, pushRejected("unknown operation binding")
	}
	if operation == PushBuild && (rules.BaseRef == "" || ValidateRefName(rules.BaseRef) != nil) {
		return PushValidationContext{}, pushRejected("build requires intended base ref")
	}
	if len(refs) == 0 || len(refs) > MaxPushRefs {
		return PushValidationContext{}, pushRejected("missing ref intent")
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ValidateRefName(ref.Ref) != nil || seen[ref.Ref] {
			return PushValidationContext{}, pushRejected("invalid ref intent")
		}
		seen[ref.Ref] = true
		for _, oid := range []string{ref.OldSHA, ref.NewSHA} {
			if oid != "" && ValidateCommitIDs(oid) != nil {
				return PushValidationContext{}, pushRejected("invalid lease intent")
			}
		}
	}
	rules.Observation = authority.CloneObservation(rules.Observation)
	return PushValidationContext{binding: binding, operation: operation, refs: slices.Clone(refs), rules: rules}, nil
}

// Binding returns a value copy for the store-reloading validity guard.
func (c PushValidationContext) Binding() PushBinding { return c.binding }

// Operation returns the intended mutation family.
func (c PushValidationContext) Operation() PushOperation { return c.operation }

// RefIntents returns an owned copy of the exact intended refs and leases.
func (c PushValidationContext) RefIntents() []PushRefIntent { return slices.Clone(c.refs) }

// PushValidityGuard reloads the bound queue from durable storage on every call.
// A changed pin/basis or a reload error requires fresh planning, never reuse.
type PushValidityGuard func(context.Context, PushValidationContext) error

// PushOptions is transient and never enters deployed action/event payloads.
// Zero means the byte-identical legacy path: no reads, validation, or guard.
type PushOptions struct {
	AuthorityRequired bool
	Context           PushValidationContext
	Guard             PushValidityGuard
	// Warn reports policy-specific bypasses; it never changes acceptance.
	Warn func([]PushWarning)
}

// CheckDispatch rejects missing/stale bindings before preparing a write. Call
// only after read-first no-ops; no-op fence/cleanup must not require authority.
func (o PushOptions) CheckDispatch(ctx context.Context, repo RepoRef, operation PushOperation, refs []PushRefIntent) error {
	if !o.AuthorityRequired {
		return nil
	}
	if o.Guard == nil {
		return pushRejected("missing authority guard")
	}
	if err := o.Guard(ctx, o.Context); err != nil {
		return err
	}
	if o.Context.binding.Repository != repo || o.Context.operation != operation || !sameIntents(o.Context.refs, refs) || !authority.IsDigest(o.Context.binding.EffectiveDigest) {
		return pushRejected("missing or mismatched authority context")
	}
	return nil
}

func sameIntents(a, b []PushRefIntent) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// ValidateBeforePush checks actual prepared objects, then reloads validity as
// the final step before publication. It must execute inside the session lock.
func (o PushOptions) ValidateBeforePush(ctx context.Context, repo RepoRef, p PreparedPush) error {
	if !o.AuthorityRequired {
		return nil
	}
	if o.Guard == nil || o.Context.binding.Repository != repo || o.Context.operation != p.Operation || !MatchesPreparedRefs(o.Context.refs, p.Refs) {
		return pushRejected("prepared push differs from authorization")
	}
	warnings, err := EvaluatePushRules(p, o.Context.rules)
	if err != nil {
		return err
	}
	if len(warnings) > 0 && o.Warn != nil {
		o.Warn(slices.Clone(warnings))
	}
	if err := o.Guard(ctx, o.Context); err != nil {
		return fmt.Errorf("push validity guard: %w", err)
	}
	return nil
}

// PreparedRefReader optionally reads an exact prospective push destination.
// Absence returns an empty SHA; transport failures remain errors. Serve uses
// it only for authority builds whose force-update lease must be bound exactly.
type PreparedRefReader interface {
	ReadPreparedRef(context.Context, RepoRef, string) (string, error)
}
