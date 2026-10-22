package serve

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// authorityActivation is an internal, explicit request, never derived from a
// candidate config or a journal pin. w.AuthorityRequired is the mechanism;
// production constructors do not enable it.
// Config selects atomic config/pin lifecycle emission. A nil Config retains
// the pin-only adoption seam for an already durable governing configuration.
type authorityActivation struct {
	Config        *queue.ConfigApplied
	Historical    []queue.ConfigApplied
	Receipt       authority.EmissionReceipt
	ReceiptMaxAge time.Duration
	Pin           queue.PolicyAuthorityApplied
	DraftID       int64
	Input         forge.WriteRestrictionInput
	Read          func(context.Context) (authority.Observation, error)
}

// activateAuthority requires w.AuthorityRequired before identity I/O. Local
// receipt checks precede read-only identity queries; the database binding precedes
// writes and forge reads. Its adoption receipt does not install runtime hooks.
// Receipt age and database identity are rechecked before calling the adoption
// transaction. It can still expire while that transaction waits for the queue
// lock or commits; this is not a transaction-time expiry guarantee.
func (s *Server) activateAuthority(ctx context.Context, w *worker, request authorityActivation) (store.AuthorityAdoptionReceipt, error) {
	var zero store.AuthorityAdoptionReceipt
	if !w.AuthorityRequired {
		return zero, errors.New("internal authority activation opt-in required")
	}
	// Reject locally invalid attestations before opening the identity transaction.
	if err := request.Receipt.ValidateForBinary(request.Receipt.DatabaseIdentity, time.Now().UTC(), request.ReceiptMaxAge, authority.RunningBinaryRevision()); err != nil {
		return zero, err
	}
	validateReceipt := func() error {
		identity, err := s.store.DatabaseIdentity(ctx)
		if err != nil {
			return err
		}
		return request.Receipt.ValidateForBinary(identity, time.Now().UTC(), request.ReceiptMaxAge, authority.RunningBinaryRevision())
	}
	if err := validateReceipt(); err != nil {
		return zero, err
	}
	if request.Pin.Authority != authority.GitHub && request.DraftID > 0 && request.Read == nil {
		return zero, errors.New("authority activation seeing reader required")
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return zero, err
	}
	basis := state.RulesSourceRevision(queue.WriteRestriction)
	started := time.Now().UTC()
	var fresh authority.Observation
	if request.Pin.Authority != authority.GitHub && request.DraftID > 0 {
		fresh, err = request.Read(ctx)
		if err != nil {
			return zero, &authorityObservationReadFailure{err: err}
		}
	}
	at := time.Now().UTC()
	// A slow/quota-delayed read must not outlive the first-emission receipt.
	if err := validateReceipt(); err != nil {
		return zero, err
	}
	var receipt store.AuthorityAdoptionReceipt
	if request.Config != nil {
		receipt, err = s.store.ApplyAuthorityConfig(ctx, w.id, *request.Config, request.Pin, request.DraftID, basis, started, at, request.Input, fresh, request.Historical...)
	} else {
		receipt, err = s.store.AdoptAuthorityPin(ctx, w.id, request.Pin, request.DraftID, basis, started, at, request.Input, fresh)
	}
	if err != nil {
		return zero, err
	}
	if request.Pin.OpenMerge != nil {
		binding := request.Pin.OpenMerge.Binding
		w.authorityDiscoveryPin.Store(&binding)
	} else {
		w.authorityDiscoveryPin.Store(nil)
	}
	s.options.Logger.Info(receipt.String(), "repository", w.Key.Repository, "base", w.Key.BaseBranch)
	return receipt, nil
}

// authorityLifecycleResolution supplies only the independently resolved identity
// and guarded observation request. The lifecycle computes the effective binding
// from the loaded configuration, never from a persisted pin or proposed draft.
// Governing content is acquired independently before this resolver is consulted.
type authorityLifecycleResolution struct {
	Policy       authority.Policy
	RepositoryID int64
	Activation   authorityActivation
}

func (s *Server) prepareAuthorityLifecycle(ctx context.Context, w *worker, state queue.State, branch, tip string) (*authorityActivation, error) {
	w.authorityObservation = nil
	if !w.AuthorityRequired {
		return nil, errors.New("internal authority activation opt-in required")
	}
	if w.resolveAuthorityLifecycle == nil {
		return nil, errors.New("authority lifecycle resolver unavailable")
	}
	candidate := w.configCandidate
	resolved, err := w.resolveAuthorityLifecycle(ctx, candidate, branch, tip)
	if err != nil {
		return nil, &authorityObservationReadFailure{err: err}
	}
	mode := candidate.Authority.Mode.Normalized()
	if err := resolved.Policy.Validate(); err != nil {
		return nil, err
	}
	if resolved.Policy.Mode.Normalized() != mode {
		return nil, errors.New("resolved authority mode differs from configuration")
	}
	if mode == authority.OpenMerge && (candidate.Authority.BreakGlass == nil || !reflect.DeepEqual(*candidate.Authority.BreakGlass, resolved.Policy.BreakGlass.Declared)) {
		return nil, errors.New("resolved authority actor differs from configuration")
	}
	deleteHead := !s.options.DisableDeleteHeadBranch
	if w.deleteHeadBranch != nil {
		deleteHead = *w.deleteHeadBranch
	}
	legacy := w.cfg.Digest(deleteHead)
	if w.stageA != nil {
		legacy = w.configDigest
	}
	digest, err := authority.EffectiveDigest(authority.DigestInputs{LegacyDigest: legacy, Policy: resolved.Policy, DefaultBranch: branch, MergeMethod: authority.MergeMethod(w.cfg.MergeMethod), Selectors: candidate.BaseSelectors()})
	if err != nil {
		return nil, err
	}
	if state.ConfigDigest != digest {
		w.summaries = make(map[int]forge.PullRequestSummary)
		if w.managed {
			w.ready = false
			select {
			case s.refresh <- struct{}{}:
			default:
			}
		}
	}
	w.configDigest = digest
	previous := state.AuthorityPin()
	// A github repository, including one with a historical rollback pin, gains
	// no new authority events on ordinary github configuration changes.
	if mode == authority.GitHub && (previous == nil || previous.Authority != authority.OpenMerge) {
		return nil, nil
	}
	coverage, err := authority.CoverageDigest(branch, candidate.BaseSelectors())
	if err != nil {
		return nil, err
	}
	request := resolved.Activation
	request.Pin = queue.PolicyAuthorityApplied{GoverningCommit: tip, EffectiveDigest: digest, Authority: mode, DefaultBranch: branch, CoverageDigest: coverage}
	if mode == authority.OpenMerge {
		installation, err := strconv.ParseInt(w.Key.Installation, 10, 64)
		if err != nil {
			return nil, err
		}
		binding := authority.PolicyBinding{InstallationID: installation, RepositoryID: resolved.RepositoryID, Repository: w.Key.Repository, QueueKey: w.Key.Installation + "/" + w.Key.Repository + "/" + w.Key.BaseBranch, EffectiveDigest: digest, CoverageDigest: coverage, DefaultBranch: branch, MergeMethod: string(w.cfg.MergeMethod), DeclaredActor: resolved.Policy.BreakGlass.Declared, ResolvedActor: resolved.Policy.BreakGlass.Resolved}
		request.Pin.OpenMerge = &queue.AuthorityPin{Binding: binding}
		request.Input.Repo = w.repo()
		request.Input.Binding = binding
		request.Input.BaseSelectors = candidate.BaseSelectors()
	}
	if err := request.Pin.Validate(); err != nil {
		return nil, err
	}
	// Reconciliation owns recurring observations, independently of pin adoption.
	// A draftless lifecycle operation never spends a seeing read it would discard.
	if mode == authority.OpenMerge {
		w.authorityObservation = &request
	}
	if mode == authority.OpenMerge && previous != nil && previous.OpenMerge != nil && (request.DraftID == 0 || queue.AuthorityDraftAdopted(*previous) && request.DraftID == previous.OpenMerge.Activation.Proposed.EventID) {
		unchanged := request.Pin
		unchanged.OpenMerge = previous.OpenMerge
		input := previous.OpenMerge.VerificationInput
		inputsMatch := !queue.AuthorityDraftAdopted(*previous) || input != nil && reflect.DeepEqual(*input, queue.AuthorityVerificationInput{BaseSelectors: request.Input.BaseSelectors, ExpectedRefs: request.Input.ExpectedRefs, AppID: request.Input.AppID})
		if inputsMatch && reflect.DeepEqual(previous.OpenMerge.Binding, request.Pin.OpenMerge.Binding) && reflect.DeepEqual(*previous, unchanged) {
			return nil, nil
		}
	}
	if mode == authority.OpenMerge && (previous == nil || previous.OpenMerge == nil || !reflect.DeepEqual(previous.OpenMerge.Binding, request.Pin.OpenMerge.Binding) || request.DraftID > 0 && !queue.AuthorityDraftAdopted(*previous)) {
		if request.Read == nil {
			return nil, errors.New("authority governing protection reader unavailable")
		}
		// Even draftless activation requires current, complete forge evidence.
		// The proposed draft and --accept cannot supply or waive this read.
		ids, ok := w.Driver.(forge.IdentityReader)
		if !ok {
			return nil, errors.New("authority governing identity reader unavailable")
		}
		if err := forge.CheckWriteRestrictionActor(ctx, ids, request.Input); err != nil {
			return nil, &authorityObservationReadFailure{err: err}
		}
		fresh, err := request.Read(ctx)
		if err != nil {
			return nil, &authorityObservationReadFailure{err: err}
		}
		fresh, err = forge.InterpretWriteRestriction(request.Input, fresh)
		if err != nil {
			return nil, errors.New("authority governing protection interpretation failed")
		}
		if _, err := fresh.Fingerprint(); err != nil {
			return nil, errors.New("authority governing protection evidence incomplete; retry with a complete seeing read")
		}
		// This acquisition gate cannot clear a source or certify draft adoption:
		// those writes retain independent fresh reads after capturing their basis.
	}
	return &request, nil
}
