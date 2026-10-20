package fake

import (
	"context"
	"errors"

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
	if e := o.Validate(); e != nil {
		return e
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeRestrictions == nil {
		f.writeRestrictions = map[forge.RepoRef]writeRestrictionState{}
	}
	f.writeRestrictions[repo] = writeRestrictionState{authority.CloneObservation(o), err}
	return nil
}

// ObserveWriteRestriction retains successful partial evidence even on failure.
func (f *Forge) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	empty := forge.EmptyWriteRestrictionObservation()
	if err := forge.ValidateWriteRestrictionInput(in); err != nil {
		return empty, err
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
