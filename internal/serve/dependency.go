package serve

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// dependencyPass is the private ADR 0011 slice 4a activation seam (Hold). Public
// loaders and constructors cannot install it; the public config loader still
// rejects every dependency key. Only internal tests supply its callbacks.
type dependencyPass struct {
	// binding is independently verified journal identity, never a snapshot's.
	binding dependency.JournalInitialized
	// landingStart supplies trusted fresh landing-start reads, completed before
	// the store transaction opens. It must not emit or mutate journal state.
	landingStart func(context.Context, queue.Land, queue.LandingStarted) (store.DependencyStart, error)
	// recover drains accepted Landed outcomes through RecoverLanded before any
	// admission or start; production wiring is Store.RecoverProvenance.
	recover func(context.Context, store.QueueID) error
}

// complete refuses partial activation and unsupported combinations. Stage A
// approval and provenance writers keep their own sanctioned starts in 4a-1.
func (pass *dependencyPass) complete(w *worker) error {
	if pass == nil || pass.landingStart == nil || pass.recover == nil || w.provenance != nil || w.stageA != nil {
		return errors.New("incomplete dependency activation")
	}
	return pass.binding.Validate()
}
