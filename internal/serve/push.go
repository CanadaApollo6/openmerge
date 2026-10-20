package serve

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// AuthorityPushPolicy is the successfully resolved governing policy. PR 9
// installs Target.PrepareAuthorityPush to resolve configuration, durably apply
// its config/mode pin, then observe protection against that pin, in that order.
// A present hook never falls back to legacy options when a step is unavailable.
type AuthorityPushPolicy struct {
	Mode                                    authority.Mode
	GoverningCommit, EffectiveDigest, AppID string
}

func unavailablePush(err error) forge.PushOptions {
	return forge.PushOptions{AuthorityRequired: true, Guard: func(context.Context, forge.PushValidationContext) error { return err }}
}

func (s *Server) authorityPushOptions(ctx context.Context, w *worker, operation forge.PushOperation, refs []forge.PushRefIntent) forge.PushOptions {
	var resolved *AuthorityPushPolicy
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
		if policy.Mode == authority.OpenMerge {
			if w.ObserveAuthorityPush == nil {
				return unavailablePush(errors.New("authority protection observer unavailable"))
			}
			if err := w.ObserveAuthorityPush(ctx, policy); err != nil {
				return unavailablePush(err)
			}
		}
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return unavailablePush(err)
	}
	pin := state.AuthorityPin()
	if pin == nil {
		if resolved == nil {
			return forge.PushOptions{}
		}
		return unavailablePush(errors.New("resolved authority policy has no durable pin"))
	}
	if resolved == nil {
		// A surviving pin cannot select an unconfigured legacy bootstrap path.
		if pin.Authority == authority.GitHub && pin.GoverningCommit == w.configTip && pin.EffectiveDigest == w.configDigest {
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
	if pin.OpenMerge == nil || !strings.EqualFold(pin.OpenMerge.Binding.Repository, w.Key.Repository) {
		return unavailablePush(errors.New("authority repository binding missing"))
	}
	basis, evidence, ok := state.AuthorityProtection()
	if !ok {
		return unavailablePush(errors.New("authority protection evidence missing"))
	}
	fingerprint, err := evidence.Fingerprint()
	if err != nil {
		return unavailablePush(err)
	}
	if operation == forge.PushBuild {
		reader, ok := w.Git.(forge.PreparedRefReader)
		if !ok {
			return unavailablePush(errors.New("authority build needs exact ref lease reader"))
		}
		for i := range refs {
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
	guard := forge.PushValidityGuard(func(ctx context.Context, c forge.PushValidationContext) error {
		// Never close over a cached state: reload errors propagate on every call.
		current, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
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
		fingerprint, err := evidence.Fingerprint()
		if err != nil {
			return err
		}
		if fingerprint != b.ProtectionFingerprint {
			return errors.New("stale push protection evidence")
		}
		return nil
	})
	return forge.PushOptions{AuthorityRequired: true, Context: frozen, Guard: guard, Warn: func(warnings []forge.PushWarning) {
		for _, warning := range warnings {
			s.options.Logger.Warn("App bypasses pull-request policy", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "protection", warning.ProtectionID, "ref", warning.Ref, "rule", warning.Rule)
		}
	}}
}
