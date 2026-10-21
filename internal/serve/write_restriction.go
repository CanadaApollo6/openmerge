package serve

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const authorityReadErrorReason = "authority write restriction read failed"

// authorityObservationReadFailure means the seeing read failed, but the
// source-specific durable block was recorded. Persistence and wiring errors
// intentionally remain ordinary errors so they still abort reconciliation.
type authorityObservationReadFailure struct{ err error }

func (e *authorityObservationReadFailure) Error() string { return e.err.Error() }
func (e *authorityObservationReadFailure) Unwrap() error { return e.err }

// ObserveWriteRestriction is dormant until PR 9 installs the authority hook.
// The initial Load completes before forge I/O; Append owns its own short lock.
// The caller must complete the first-emission drain/backup procedure before
// enabling this hook. A failed read is represented by a source-specific
// observation so it cannot alter unrelated rule sources or their cadence.
func ObserveWriteRestriction(ctx context.Context, db *store.Store, q store.QueueID, reader forge.WriteRestrictionReader, in forge.WriteRestrictionInput) (int64, error) {
	state, _, err := db.Load(ctx, q)
	if err != nil {
		return 0, err
	}
	pin := state.AuthorityPin()
	if pin == nil || pin.OpenMerge == nil || !reflect.DeepEqual(pin.OpenMerge.Binding, in.Binding) || state.ConfigDigest != in.Binding.EffectiveDigest {
		return 0, errors.New("observer requires the currently applied authority pin")
	}
	basis := state.RulesSourceRevision(queue.WriteRestriction)
	var evidence authority.Observation
	if full, ok := reader.(forge.WriteRestrictionInventoryReader); ok {
		evidence, err = full.ObserveAuthorityInventory(ctx, in)
	} else {
		evidence, err = reader.ObserveWriteRestriction(ctx, in)
	}
	readErr := err
	if readErr != nil {
		evidence.Inventory = authority.ReadFailure
	}
	if readErr == nil && evidence.Inventory == authority.ReadFailure {
		readErr = forge.ErrRulesUnavailable
	}
	evidence, interpretErr := forge.InterpretWriteRestriction(in, evidence)
	if interpretErr != nil {
		cause := errors.Join(readErr, interpretErr)
		if cause == nil {
			cause = interpretErr
		}
		evidence = forge.EmptyWriteRestrictionObservation()
		id, appendErr := appendWriteRestrictionObservation(ctx, db, q, in.Binding, basis, evidence, authorityReadErrorReason)
		if appendErr != nil {
			return id, errors.Join(cause, appendErr)
		}
		return id, &authorityObservationReadFailure{err: cause}
	}
	if readErr != nil && len(evidence.Findings) == 0 {
		evidence = forge.EmptyWriteRestrictionObservation()
	}
	readError := ""
	if readErr != nil {
		readError = authorityReadErrorReason
	}
	id, appendErr := appendWriteRestrictionObservation(ctx, db, q, in.Binding, basis, evidence, readError)
	if appendErr != nil {
		if readErr != nil {
			return id, errors.Join(readErr, appendErr)
		}
		return id, appendErr
	}
	if readErr != nil {
		return id, &authorityObservationReadFailure{err: readErr}
	}
	return id, nil
}

func appendWriteRestrictionObservation(ctx context.Context, db *store.Store, q store.QueueID, binding authority.PolicyBinding, basis uint64, evidence authority.Observation, readError string) (int64, error) {
	return db.Append(ctx, q, []queue.Event{queue.WriteRestrictionObserved{
		Binding:    binding,
		Basis:      basis,
		ObservedAt: time.Now().UTC(),
		Evidence:   evidence,
		ReadError:  readError,
	}}, "")
}

// authorityObservationReader adapts the private lifecycle acquisition seam to
// the shared interpreter/fold. It cannot be installed by persisted evidence.
type authorityObservationReader func(context.Context) (authority.Observation, error)

func (read authorityObservationReader) ObserveWriteRestriction(ctx context.Context, _ forge.WriteRestrictionInput) (authority.Observation, error) {
	return read(ctx)
}

func (s *Server) scheduleAuthorityRetry(w *worker, cause error) {
	now := time.Now()
	w.authorityFailures++
	interval := s.options.RulesInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	delay := min(time.Minute*time.Duration(1<<min(w.authorityFailures-1, 3)), 5*time.Minute, interval)
	deadline := now.Add(delay)
	var rate interface{ RetryAfter() time.Time }
	if errors.As(cause, &rate) && rate.RetryAfter().After(now) {
		deadline = rate.RetryAfter()
	}
	w.authorityNextCheck = deadline
}

func (s *Server) scheduleAuthoritySuccess(w *worker) {
	interval := s.options.RulesInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	w.authorityFailures = 0
	w.authorityNextCheck = time.Now().Add(interval)
}

func resetAuthorityCadence(w *worker) {
	w.authorityNextCheck = time.Time{}
	w.authorityFailures = 0
	w.authorityCadenceBinding = nil
}

// observeWorkerAuthority runs on the rules-style cadence, including the first
// cycle and unadopted pins. Status requests never trigger acquisition. Rollback
// disables new reads without changing warning history or unrelated sources.
func (s *Server) observeWorkerAuthority(ctx context.Context, w *worker) error {
	if !w.AuthorityRequired || w.resolveAuthorityLifecycle == nil || w.configError != nil || w.configCandidate.Authority.Mode != authority.OpenMerge {
		resetAuthorityCadence(w)
		return nil
	}
	request := w.authorityObservation
	if request == nil {
		resetAuthorityCadence(w)
		return errors.New("authority observation reader unavailable")
	}
	if w.authorityCadenceBinding == nil || !reflect.DeepEqual(*w.authorityCadenceBinding, request.Input.Binding) {
		binding := request.Input.Binding
		w.authorityCadenceBinding = &binding
		w.authorityNextCheck = time.Time{}
		w.authorityFailures = 0
	}
	if !w.authorityNextCheck.IsZero() && time.Now().Before(w.authorityNextCheck) {
		return nil
	}
	if request.Read == nil {
		readErr := errors.New("authority observation reader unavailable")
		state, _, loadErr := s.store.Load(ctx, w.id)
		if loadErr != nil {
			return loadErr
		}
		pin := state.AuthorityPin()
		if pin == nil || pin.OpenMerge == nil {
			return errors.New("authority observation requires the current openmerge pin")
		}
		evidence := forge.EmptyWriteRestrictionObservation()
		if _, appendErr := appendWriteRestrictionObservation(ctx, s.store, w.id, pin.OpenMerge.Binding, state.RulesSourceRevision(queue.WriteRestriction), evidence, authorityReadErrorReason); appendErr != nil {
			return appendErr
		}
		s.scheduleAuthorityRetry(w, readErr)
		return nil
	}
	readCtx := forge.WithRequestClass(ctx, forge.RulesRequest)
	timeout := s.options.ActionTimeout
	if timeout <= 0 {
		timeout = DefaultActionTimeout
	}
	timedReadCtx, cancel := context.WithTimeout(readCtx, timeout)
	reader := authorityObservationReader(func(context.Context) (authority.Observation, error) {
		return request.Read(timedReadCtx)
	})
	_, err := ObserveWriteRestriction(readCtx, s.store, w.id, reader, request.Input)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var readFailure *authorityObservationReadFailure
	if errors.As(err, &readFailure) {
		s.scheduleAuthorityRetry(w, readFailure.err)
		s.options.Logger.Warn("authority write restriction read failed", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", readFailure.err, "retry_at", w.authorityNextCheck)
		return nil
	}
	if err != nil {
		return err
	}
	s.scheduleAuthoritySuccess(w)
	return nil
}
