package serve

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ErrAuthorityVerificationRequired identifies a missing pre-switch operator read.
var ErrAuthorityVerificationRequired = errors.New("public authority activation requires setup --verify proposed verification")

// AuthorityTarget is the reviewed verification plan and its durable proposed
// event. It grants no authority by itself: the lifecycle validates the actual
// governing tree, actor, receipt and fresh interpreted protection evidence.
type AuthorityTarget struct {
	Key     store.QueueKey              `json:"queue"`
	Input   forge.WriteRestrictionInput `json:"input"`
	DraftID int64                       `json:"draft_id,omitempty"`
}

// AuthorityRuntime enables the completed lifecycle for reviewed targets only.
// Receipt gates only the first authority-boundary event, never steady-state operation.
type AuthorityRuntime struct {
	DatabaseIdentity string
	Targets          []AuthorityTarget
	Receipt          func() (authority.EmissionReceipt, error)
	ReceiptMaxAge    time.Duration
}

// Validate checks a reviewed runtime plan without reading receipts or the forge.
func (r *AuthorityRuntime) Validate() error {
	if r == nil {
		return nil
	}
	if len(r.Targets) == 0 || len(r.Targets) > 1000 || r.Receipt == nil || r.ReceiptMaxAge <= 0 {
		return errors.New("authority opt-in requires 1..1000 targets, receipt reader and positive maximum age")
	}
	installation := ""
	seen := map[store.QueueKey]bool{}
	for _, target := range r.Targets {
		if forge.ValidateWriteRestrictionInput(target.Input) != nil || target.Key.BaseBranch == "" || target.Key.Lane != "" || target.Key.Installation != strconv.FormatInt(target.Input.Binding.InstallationID, 10) || target.Key.Repository != target.Input.Binding.Repository || store.AuthorityQueueKey(target.Key) != target.Input.Binding.QueueKey || target.Input.Binding.DeclaredActor.Team == "" || seen[target.Key] {
			return errors.New("authority plan queue binding mismatch, unsupported lane or duplicate target")
		}
		if installation != "" && installation != target.Key.Installation {
			return errors.New("authority activation supports a single installation only")
		}
		installation = target.Key.Installation
		seen[target.Key] = true
	}
	return nil
}

func (s *Server) publicAuthorityTarget(target Target) Target {
	runtime := s.options.Authority
	if runtime == nil || target.resolveAuthorityLifecycle != nil || target.Driver == nil || target.Git == nil {
		return target
	}
	var intended *AuthorityTarget
	for i := range runtime.Targets {
		if runtime.Targets[i].Key == target.Key {
			intended = &runtime.Targets[i]
			break
		}
	}
	// Discovery asks for a repository target without a base.
	if intended == nil && target.Key.BaseBranch == "" {
		for i := range runtime.Targets {
			if runtime.Targets[i].Key.Installation == target.Key.Installation && runtime.Targets[i].Key.Repository == target.Key.Repository {
				intended = &runtime.Targets[i]
				break
			}
		}
	}
	if intended == nil {
		return target
	}
	plan := *intended
	target.AuthorityRequired = true
	target.publicAuthority = true
	target.resolveAuthorityLifecycle = func(ctx context.Context, candidate config.AuthorityCandidate, branch, _ string) (authorityLifecycleResolution, error) {
		policy := authority.Policy{Mode: candidate.Authority.Mode.Normalized()}
		if policy.Mode == authority.GitHub {
			return authorityLifecycleResolution{Policy: policy}, nil
		}
		if plan.DraftID <= 0 {
			return authorityLifecycleResolution{}, ErrAuthorityVerificationRequired
		}

		if target.AuthorityCompletionGit == nil || target.Key.Lane != "" || runtime.Receipt == nil || plan.Input.Binding.DefaultBranch != branch || candidate.Authority.BreakGlass == nil || !reflect.DeepEqual(*candidate.Authority.BreakGlass, plan.Input.Binding.DeclaredActor) {
			return authorityLifecycleResolution{}, errors.New("authority reviewed plan differs from current policy")
		}
		if runtime.DatabaseIdentity != "" {
			identity, err := s.store.DatabaseIdentity(ctx)
			if err != nil {
				return authorityLifecycleResolution{}, err
			}
			if identity != runtime.DatabaseIdentity {
				return authorityLifecycleResolution{}, errors.New("authority configured database identity mismatch")
			}
		}
		var receipt authority.EmissionReceipt
		boundary, err := s.store.AuthorityBoundaryExists(ctx)
		if err != nil {
			return authorityLifecycleResolution{}, err
		}
		if !boundary {
			receipt, err = runtime.Receipt()
			if err != nil {
				return authorityLifecycleResolution{}, &emissionReceiptFailure{err: err}
			}
			identity, err := s.store.DatabaseIdentity(ctx)
			if err != nil {
				return authorityLifecycleResolution{}, err
			}
			if err := receipt.ValidateForBinary(identity, time.Now(), runtime.ReceiptMaxAge, authority.RunningBinaryRevision()); err != nil {
				return authorityLifecycleResolution{}, &emissionReceiptFailure{err: err}
			}
		}
		ids, ok := target.Driver.(forge.IdentityReader)
		if !ok {
			return authorityLifecycleResolution{}, errors.New("authority identity reader unavailable")
		}
		input := plan.Input
		input.Repo = forge.RepoRef{Path: target.Key.Repository}
		if err := forge.CheckWriteRestrictionActor(ctx, ids, input); err != nil {
			cause := &config.GoverningReadError{Boundary: "identities", Cause: err}
			if errors.Is(err, forge.ErrIdentityUnavailable) || errors.Is(err, forge.ErrIdentityMismatch) {
				return authorityLifecycleResolution{}, &config.GoverningIdentityError{Cause: cause}
			}
			return authorityLifecycleResolution{}, cause
		}
		policy.BreakGlass = authority.ActorBinding{Declared: input.Binding.DeclaredActor, Resolved: input.Binding.ResolvedActor}
		reader, ok := target.Driver.(forge.WriteRestrictionReader)
		if !ok {
			return authorityLifecycleResolution{}, errors.New("authority protection reader unavailable")
		}
		// Live branch names are interpretation inputs, even with an unchanged binding.
		repo, err := s.authorityCompletionRepository(ctx, store.AuthorityCompletionRequest{Installation: target.Key.Installation, Repository: target.Key.Repository})
		if err != nil {
			return authorityLifecycleResolution{}, &sanitizedAuthorityInventoryError{cause: err}
		}
		if repo.Branches == nil {
			return authorityLifecycleResolution{}, errors.New("authority branch inventory unavailable")
		}
		branches, err := repo.Branches(ctx)
		if err != nil {
			return authorityLifecycleResolution{}, &sanitizedAuthorityInventoryError{cause: err}
		}
		coverage, err := authority.CompileRefCoverage(branch, candidate.BaseSelectors())
		if err != nil {
			return authorityLifecycleResolution{}, err
		}
		input.ExpectedRefs = nil
		for _, base := range branches {
			ref := "refs/heads/" + base
			selected, err := coverage.ContainsRef(ref, "refs/heads/"+branch)
			if err != nil {
				return authorityLifecycleResolution{}, err
			}
			if selected {
				input.ExpectedRefs = append(input.ExpectedRefs, ref)
			}
		}
		slices.Sort(input.ExpectedRefs)
		// Setup's plan also includes the internal generated refs needed for coverage.
		for _, ref := range plan.Input.ExpectedRefs {
			if len(ref) >= 21 && ref[:21] == "refs/heads/openmerge/" {
				input.ExpectedRefs = append(input.ExpectedRefs, ref)
			}
		}
		input.BaseSelectors = candidate.BaseSelectors()

		read := func(ctx context.Context) (authority.Observation, error) {
			evidence, err := reader.ObserveWriteRestriction(ctx, input)
			if err != nil {
				return evidence, &authorityObservationReadFailure{err: err}
			}
			return evidence, nil
		}
		return authorityLifecycleResolution{Policy: policy, RepositoryID: input.Binding.RepositoryID, Activation: authorityActivation{Receipt: receipt, ReceiptMaxAge: runtime.ReceiptMaxAge, Input: input, DraftID: plan.DraftID, Read: read}}, nil
	}
	return target
}

// RequireAuthorityActivation is the public final switch gate. Its only source
// is fresh repository discovery plus the actual all-partition store snapshot.
func (s *Server) RequireAuthorityActivation(ctx context.Context, request store.AuthorityCompletionRequest) error {
	completion, err := s.AuthorityCompletion(ctx, request)
	if err != nil {
		return err
	}
	return completion.RequireComplete()
}

func (s *Server) publicAuthorityRequest(w *worker) store.AuthorityCompletionRequest {
	request := store.AuthorityCompletionRequest{Installation: w.Key.Installation, Repository: w.Key.Repository, Targets: map[string]string{}}
	for _, target := range s.options.Authority.Targets {
		if target.Key.Installation == w.Key.Installation && target.Key.Repository == w.Key.Repository {
			request.Targets[target.Key.BaseBranch] = target.Input.Binding.EffectiveDigest
		}
	}
	return request
}

// Preserve quota and cancellation identity without exposing forge diagnostics.
type sanitizedAuthorityInventoryError struct{ cause error }

func (e *sanitizedAuthorityInventoryError) Error() string {
	return "authority branch inventory unavailable"
}
func (e *sanitizedAuthorityInventoryError) Unwrap() error { return e.cause }

type emissionReceiptFailure struct{ err error }

func (e *emissionReceiptFailure) Error() string { return "emission receipt stale/invalid" }
func (e *emissionReceiptFailure) Unwrap() error { return e.err }
