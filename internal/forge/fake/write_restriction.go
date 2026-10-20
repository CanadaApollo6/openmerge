package fake

import (
	"context"
	"errors"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.WriteRestrictionReader = (*Forge)(nil)

type writeRestrictionState struct {
	observation authority.Observation
	err         error
}

// SetWriteRestrictionObservation models a fresh full or partial acquisition.
// Unconfigured repositories are unknown, never an authoritative empty inventory.
func (f *Forge) SetWriteRestrictionObservation(repo forge.RepoRef, o authority.Observation, err error) error {
	o = authority.CloneObservation(o)
	for i := range o.Protections {
		for j := range o.Protections[i].BypassActors {
			a := &o.Protections[i].BypassActors[j]
			switch a.Kind {
			case "organization_admin", "deploy_key", "enterprise_owner":
				a.ID = ""
			}
		}
	}
	if e := o.Validate(); e != nil {
		return e
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeRestrictions == nil {
		f.writeRestrictions = map[forge.RepoRef]writeRestrictionState{}
	}
	f.writeRestrictions[repo] = writeRestrictionState{o, err}
	return nil
}

// ObserveWriteRestriction retains successful partial evidence even on failure.
func (f *Forge) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	empty := forge.EmptyWriteRestrictionObservation()
	if err := forge.ValidateWriteRestrictionInput(in); err != nil {
		return empty, err
	}
	// Match the built-in repository-admin identity used by the GitHub reader.
	if actor := in.Binding.ResolvedActor.RepositoryAdmin; actor != nil && actor.RoleID != "5" {
		return empty, fmt.Errorf("unsupported repository-admin role: %w", forge.ErrIdentityMismatch)
	}
	f.mu.Lock()
	err := f.failure(ctx, "ObserveWriteRestriction")
	state, ok := f.writeRestrictions[in.Repo]
	o := authority.CloneObservation(state.observation)
	f.mu.Unlock()
	if err != nil {
		return empty, err
	}
	if !ok {
		return empty, forge.ErrRulesUnavailable
	}
	if err := forge.CheckWriteRestrictionActor(ctx, f, in); err != nil {
		o.Inventory = authority.ReadFailure
		return o, errors.Join(state.err, err)
	}
	return o, state.err
}
