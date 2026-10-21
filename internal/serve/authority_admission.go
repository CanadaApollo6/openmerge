package serve

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// readAuthorityAdmission is deliberately independent of the background cadence.
// All I/O finishes before StartLanding takes the existing queue lock. Its exact
// source revisions travel in LandingChecksRecorded and are rechecked there.
func (s *Server) readAuthorityAdmission(ctx, parent context.Context, w *worker, basis queue.State) (queue.State, error) {
	if !w.AuthorityRequired || w.resolveAuthorityLifecycle == nil || w.configError != nil || w.configCandidate.Authority.Mode != authority.OpenMerge {
		return queue.State{}, fmt.Errorf("authority admission opt-in/configuration unavailable")
	}
	request := w.authorityObservation
	if request == nil || request.Read == nil {
		return queue.State{}, fmt.Errorf("authority admission reader unavailable")
	}
	if w.authorityCadenceBinding == nil || !reflect.DeepEqual(*w.authorityCadenceBinding, request.Input.Binding) {
		binding := request.Input.Binding
		w.authorityCadenceBinding = &binding
		w.authorityFailures = 0
	}
	ctx = forge.WithRequestClass(ctx, forge.RulesRequest)
	if _, err := s.observeAuthorityRead(ctx, parent, w, request); err != nil {
		var readFailure *authorityObservationReadFailure
		if parent.Err() == nil && errors.As(err, &readFailure) {
			s.scheduleAuthorityRetry(w, readFailure.err)
		}
		return queue.State{}, fmt.Errorf("authority admission read failed: %w", err)
	}
	s.scheduleAuthoritySuccess(w)
	current, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return queue.State{}, err
	}
	if !reflect.DeepEqual(basis.AuthorityPin(), current.AuthorityPin()) || basis.ConfigDigest != current.ConfigDigest || basis.ConfigCommit != current.ConfigCommit {
		return queue.State{}, store.ErrLandingDecisionStale
	}
	before, after := basis.LandingCheckSources(), current.LandingCheckSources()
	for i := range before.SourceRevisions {
		if before.SourceRevisions[i].Source == queue.WriteRestriction {
			before.SourceRevisions[i].Revision = after.SourceRevisions[i].Revision
		}
	}
	if !reflect.DeepEqual(before, after) {
		return queue.State{}, store.ErrLandingDecisionStale
	}
	if current.WriteRestrictionStatus().State != "verified" || len(current.RulesSourceProblems(queue.WriteRestriction)) != 0 {
		return queue.State{}, fmt.Errorf("authority admission blocked: write restriction is not currently verified")
	}
	return current, nil
}
