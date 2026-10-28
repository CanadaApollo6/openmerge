package serve

import (
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/status"

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
	// audience is a complete recorded reader grant set per source, not App access.
	audience map[string][]dependency.Grant
	// checkAudience covers every check reader, independently of API readers.
	checkAudience map[string][]dependency.Grant
	// sources are recorded source journal bindings, never inferred from audience grants.
	sources map[string]dependency.SourceRecord
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

func dependencyJournal(j store.Journal, pass *dependencyPass) (store.Journal, error) {
	if pass == nil {
		return j, nil
	}
	return status.ProjectDependencies(j, pass.binding, pass.audience, pass.sources)
}

func (s *Server) dependencyJournals(journals []store.Journal) ([]store.Journal, error) {
	for i, j := range journals {
		for _, w := range s.workers() {
			if w.Key == j.Key && w.dependencies != nil {
				var err error
				journals[i], err = dependencyJournal(j, w.dependencies)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return journals, nil
}

func dependencyCheckJournal(j store.Journal, pass *dependencyPass) (store.Journal, error) {
	if pass == nil {
		return j, nil
	}
	return status.ProjectDependencies(j, pass.binding, pass.checkAudience, pass.sources)
}

func (s *Server) dependencyDecisionScope(ctx context.Context, key store.QueueKey) error {
	for _, w := range s.workers() {
		if w.Key != key || w.dependencies == nil {
			continue
		}
		journals, err := s.store.ReportingJournals(ctx, []store.QueueKey{key})
		if err != nil || len(journals) != 1 {
			return errors.New("dependency evidence unavailable")
		}
		j, err := dependencyJournal(journals[0], w.dependencies)
		if err != nil {
			return err
		}
		return status.RequireDependencyDecisionScope(j)
	}
	return nil
}
