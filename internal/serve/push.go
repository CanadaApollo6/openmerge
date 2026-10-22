package serve

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// AuthorityPushPolicy is the successfully resolved governing policy. PR 9
// installs Target.PrepareAuthorityPush to resolve configuration, durably apply
// its config/mode pin, then observe protection against that pin, in that order.
// A present hook never falls back to legacy options when a step is unavailable.
type AuthorityPushPolicy struct {
	Mode                                    authority.Mode
	GoverningCommit, EffectiveDigest, AppID string
}

// Unresolved startup config defers an absent fence; EnsureFence still gets to
// perform its existing-ref read-first no-op before this guard can run.
var errFenceConfigPending = errors.New("fence bootstrap awaits resolved configuration")

type authorityPushReadError struct{ cause error }

func (e *authorityPushReadError) Error() string { return "authority push durable state unavailable" }
func (e *authorityPushReadError) Unwrap() error { return e.cause }

func unavailablePush(err error) forge.PushOptions {
	return forge.PushOptions{AuthorityRequired: true, Guard: func(context.Context, forge.PushValidationContext) error { return err }}
}

func (s *Server) authorityPushOptions(ctx context.Context, w *worker, operation forge.PushOperation, refs []forge.PushRefIntent) forge.PushOptions {
	refs = slices.Clone(refs)
	var resolved *AuthorityPushPolicy
	internalLifecycle := w.PrepareAuthorityPush == nil && w.AuthorityRequired && w.resolveAuthorityLifecycle != nil
	if w.PrepareAuthorityPush != nil {
		policy, err := w.PrepareAuthorityPush(ctx)
		if err != nil {
			return unavailablePush(err)
		}
		resolved = &policy
		if w.PinAuthorityPush == nil {
			return unavailablePush(errors.New("authority durable pin writer unavailable"))
		}
		if err := w.PinAuthorityPush(ctx, policy); err != nil {
			return unavailablePush(err)
		}
		pinned, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return unavailablePush(&authorityPushReadError{cause: err})
		}
		actual := pinned.AuthorityPin()
		if actual == nil || actual.GoverningCommit != policy.GoverningCommit || actual.EffectiveDigest != policy.EffectiveDigest || actual.Authority != policy.Mode || pinned.ConfigDigest != policy.EffectiveDigest {
			return unavailablePush(errors.New("authority pin was not durably applied before observation"))
		}
		if policy.Mode == authority.OpenMerge {
			if w.ObserveAuthorityPush == nil {
				return unavailablePush(errors.New("authority protection observer unavailable"))
			}
			if err := w.ObserveAuthorityPush(ctx, policy); err != nil {
				return unavailablePush(err)
			}
		}
	}
	// Only the explicit internal opt-in may consume lifecycle resolution. A
	// candidate or restored pin cannot install this adapter on a public target.
	if internalLifecycle {
		if w.configTip == "" || w.configError != nil {
			return unavailablePush(errFenceConfigPending)
		}
		request := w.authorityObservation
		if w.configCandidate.Authority.Mode == authority.OpenMerge {
			if request == nil {
				return unavailablePush(errors.New("authority push lifecycle unavailable"))
			}
			if err := s.observeWorkerAuthority(ctx, w); err != nil {
				return unavailablePush(err)
			}
			resolved = &AuthorityPushPolicy{Mode: authority.OpenMerge, GoverningCommit: w.configTip, EffectiveDigest: w.configDigest, AppID: request.Input.AppID}
		} else {
			resolved = &AuthorityPushPolicy{Mode: authority.GitHub, GoverningCommit: w.configTip, EffectiveDigest: w.configDigest}
		}
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return unavailablePush(&authorityPushReadError{cause: err})
	}
	pin := state.AuthorityPin()
	if internalLifecycle && resolved.Mode == authority.GitHub && state.ConfigDigest == resolved.EffectiveDigest && (pin == nil || pin.Authority == authority.GitHub) {
		return forge.PushOptions{}
	}
	if pin == nil {
		if resolved == nil && !w.AuthorityRequired {
			return forge.PushOptions{}
		}
		return unavailablePush(errors.New("resolved authority policy has no durable pin"))
	}
	if resolved == nil {
		// A surviving pin cannot select an unconfigured legacy bootstrap path.
		if w.configTip == "" {
			return unavailablePush(errFenceConfigPending)
		}
		// A rollback pin is historical evidence, not a permanent binding for
		// subsequent GitHub policies. Require a successfully loaded, durably
		// applied GitHub config; required but unresolved workers still fail closed.
		if (!w.AuthorityRequired || w.resolveAuthorityLifecycle != nil) && pin.Authority == authority.GitHub && w.configCandidate.Authority.Mode.Normalized() == authority.GitHub && w.configError == nil && state.ConfigDigest == w.configDigest {
			return forge.PushOptions{}
		}
		return unavailablePush(errors.New("authority policy has not been resolved"))
	}
	if pin.GoverningCommit != resolved.GoverningCommit || pin.EffectiveDigest != resolved.EffectiveDigest || pin.Authority != resolved.Mode || state.ConfigDigest != resolved.EffectiveDigest {
		return unavailablePush(errors.New("resolved policy differs from durable authority pin"))
	}
	if resolved.Mode == authority.GitHub {
		return forge.PushOptions{}
	}
	if pin.OpenMerge == nil || !strings.EqualFold(pin.OpenMerge.Binding.Repository, w.Key.Repository) || strconv.FormatInt(pin.OpenMerge.Binding.InstallationID, 10) != w.Key.Installation || pin.OpenMerge.Binding.QueueKey != w.Key.Installation+"/"+pin.OpenMerge.Binding.Repository+"/"+w.Key.BaseBranch || w.Key.Lane != "" {
		return unavailablePush(errors.New("authority repository binding missing"))
	}
	if !queue.AuthorityDraftAdopted(*pin) {
		return unavailablePush(errors.New("authority push requires adopted interpretation evidence"))
	}
	if w.AuthorityRequired && w.resolveAuthorityLifecycle != nil {
		request := w.authorityObservation
		if request == nil || !queue.AuthorityDraftAdopted(*pin) || !queue.AuthorityInputsMatch(pin.OpenMerge.VerificationInput, request.Input) {
			return unavailablePush(errors.New("authority push requires current adopted interpretation inputs"))
		}
	}
	// PR 9 must supply the pinned seeing observation whose fingerprint was
	// accepted at activation. Never substitute a fresh runtime protection read.
	basis, currentEvidence, ok := state.AuthorityProtection()
	if !ok {
		return unavailablePush(errors.New("authority protection evidence missing"))
	}
	if pin.OpenMerge.Activation == nil {
		return unavailablePush(errors.New("pinned seeing activation evidence missing"))
	}
	evidence := pin.OpenMerge.Activation.Evidence
	fingerprint, err := evidence.Fingerprint()
	if err != nil {
		return unavailablePush(err)
	}
	if fingerprint != pin.OpenMerge.Activation.ProtectionFingerprint {
		return unavailablePush(errors.New("invalid pinned seeing activation fingerprint"))
	}
	// Ordinary runtime reads may hide bypass lists. They never replace the
	// pinned seeing inventory. A later complete seeing read must still agree.
	if currentEvidence.SeeingComplete() {
		currentFingerprint, err := currentEvidence.Fingerprint()
		if err != nil || currentFingerprint != fingerprint {
			return unavailablePush(errors.New("protection differs from pinned seeing activation"))
		}
	}
	if operation == forge.PushBuild {
		reader, ok := w.Git.(forge.PreparedRefReader)
		if !ok {
			return unavailablePush(errors.New("authority build needs exact ref lease reader"))
		}
		for i := range refs {
			if !authorityBuildBase(state, refs[i].BuildBaseSHA) {
				return unavailablePush(errors.New("authority build base differs from durable queue planning"))
			}
			old, err := reader.ReadPreparedRef(ctx, w.repo(), refs[i].Ref)
			if err != nil {
				return unavailablePush(err)
			}
			refs[i].OldSHA = old
		}
	}
	binding := forge.PushBinding{Repository: w.repo(), QueueKey: pin.OpenMerge.Binding.QueueKey, GoverningCommit: pin.GoverningCommit, EffectiveDigest: pin.EffectiveDigest, ProtectionBasis: basis, ProtectionFingerprint: fingerprint}
	frozen, err := forge.NewPushValidationContext(binding, operation, refs, forge.PushRuleContext{DefaultBranch: pin.DefaultBranch, BaseRef: "refs/heads/" + w.Key.BaseBranch, AppID: resolved.AppID, Observation: evidence})
	if err != nil {
		return unavailablePush(err)
	}
	id, repo, base, logger := w.id, w.Key.Repository, w.Key.BaseBranch, s.options.Logger
	guard := forge.PushValidityGuard(func(ctx context.Context, c forge.PushValidationContext) error {
		// Never close over a cached state: reload errors propagate on every call.
		current, _, err := s.store.Load(ctx, id)
		if err != nil {
			return &authorityPushReadError{cause: err}
		}
		actual := current.AuthorityPin()
		b := c.Binding()
		if !reflect.DeepEqual(actual, pin) || actual == nil || actual.OpenMerge == nil || actual.Authority != authority.OpenMerge || actual.GoverningCommit != b.GoverningCommit || actual.EffectiveDigest != b.EffectiveDigest || current.ConfigDigest != b.EffectiveDigest || actual.OpenMerge.Binding.QueueKey != b.QueueKey {
			return errors.New("stale push authority pin")
		}
		revision, evidence, ok := current.AuthorityProtection()
		if !ok || revision != b.ProtectionBasis {
			return errors.New("stale push protection basis")
		}
		// Bind even an unseeing observation exactly: a new source revision or
		// changed evidence requires replanning. Evaluation remains pinned.
		if !reflect.DeepEqual(evidence, currentEvidence) {
			return errors.New("stale push protection evidence")
		}
		if operation == forge.PushBuild {
			for _, ref := range refs {
				if !authorityBuildBase(current, ref.BuildBaseSHA) {
					return errors.New("stale push build base")
				}
			}
		}
		return nil
	})
	return forge.PushOptions{AuthorityRequired: true, Context: frozen, Guard: guard, Warn: func(warnings []forge.PushWarning) {
		for _, warning := range warnings {
			logger.Warn("App bypasses pull-request policy", "repository", repo, "base", base, "protection", warning.ProtectionID, "ref", warning.Ref, "rule", warning.Rule)
		}
	}}
}

func authorityBuildBase(state queue.State, sha string) bool {
	if sha == "" {
		return false
	}
	if sha == state.BaseSHA {
		return true
	}
	for _, batch := range state.Batches() {
		if batch.ConfigDigest == state.ConfigDigest && batch.TestedSHA == sha && (batch.Phase == queue.Testing || batch.Phase == queue.Landing) {
			return true
		}
	}
	return false
}
