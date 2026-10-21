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

// authorityObservationReadFailure exposes only a sanitized seeing-read class.
// Unwrap retains the cause for classification. Runtime observations attempt a
// source-specific block; joined persistence errors still abort reconciliation
// while retaining the read failure classification.
type authorityObservationReadFailure struct{ err error }

func (e *authorityObservationReadFailure) Error() string { return authorityReadFailureReason(e.err) }
func (e *authorityObservationReadFailure) Unwrap() error { return e.err }

// ObserveWriteRestriction is dormant until PR 9 installs the authority hook.
// The initial Load completes before forge I/O; Append owns its own short lock.
// The caller must complete the first-emission drain/backup procedure before
// enabling this hook. A failed read is represented by a source-specific
// observation so it cannot alter unrelated rule sources or their cadence.
func ObserveWriteRestriction(ctx context.Context, db *store.Store, q store.QueueID, reader forge.WriteRestrictionReader, in forge.WriteRestrictionInput) (int64, error) {
	return observeWriteRestriction(ctx, db, q, reader, in, completionWriteTimeout)
}

func observeWriteRestriction(ctx context.Context, db *store.Store, q store.QueueID, reader forge.WriteRestrictionReader, in forge.WriteRestrictionInput, writeTimeout time.Duration) (int64, error) {
	loadCtx, cancelLoad := context.WithTimeout(ctx, writeTimeout)
	state, _, err := db.Load(loadCtx, q)
	cancelLoad()
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
		id, appendErr := appendWriteRestrictionObservation(ctx, db, q, in.Binding, basis, evidence, authorityReadFailureReason(cause), writeTimeout)
		if appendErr != nil {
			return id, errors.Join(&authorityObservationReadFailure{err: cause}, appendErr)
		}
		return id, &authorityObservationReadFailure{err: cause}
	}
	if readErr != nil && len(evidence.Findings) == 0 {
		evidence = forge.EmptyWriteRestrictionObservation()
	}
	readError := ""
	if readErr != nil {
		readError = authorityReadFailureReason(readErr)
	}
	id, appendErr := appendWriteRestrictionObservation(ctx, db, q, in.Binding, basis, evidence, readError, writeTimeout)
	if appendErr != nil {
		if readErr != nil {
			return id, errors.Join(&authorityObservationReadFailure{err: readErr}, appendErr)
		}
		return id, appendErr
	}
	if readErr != nil {
		return id, &authorityObservationReadFailure{err: readErr}
	}
	return id, nil
}

func appendWriteRestrictionObservation(ctx context.Context, db *store.Store, q store.QueueID, binding authority.PolicyBinding, basis uint64, evidence authority.Observation, readError string, writeTimeout time.Duration) (int64, error) {
	persist, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return db.Append(persist, q, []queue.Event{queue.WriteRestrictionObserved{
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
	_, err := s.observeAuthorityRead(ctx, ctx, w, request)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var readFailure *authorityObservationReadFailure
	if errors.As(err, &readFailure) {
		s.scheduleAuthorityRetry(w, readFailure.err)
		s.options.Logger.Warn("authority write restriction read failed", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "cause", authorityReadFailureReason(readFailure.err), "retry_at", w.authorityNextCheck)
		if err != readFailure {
			return err // The read failed and its block could not be persisted.
		}
		return nil
	}
	if err != nil {
		return err
	}
	s.scheduleAuthoritySuccess(w)
	return nil
}

// observeAuthorityRead caps forge I/O at ActionTimeout and the remaining action
// deadline; it is not an independent read budget. A late-starting read can time
// out with a healthy forge, and must fail closed and back off. Each persistence
// operation has its own completionWriteTimeout based on the worker parent, so
// action expiry permits a bounded block write while shutdown cancels it.
func (s *Server) observeAuthorityRead(ctx, parent context.Context, w *worker, request *authorityActivation) (int64, error) {
	readCtx := forge.WithRequestClass(ctx, forge.RulesRequest)
	timeout := s.options.ActionTimeout
	if timeout <= 0 {
		timeout = DefaultActionTimeout
	}
	timedReadCtx, cancel := context.WithTimeout(readCtx, timeout)
	defer cancel()
	read := request.Read
	if read == nil {
		read = func(context.Context) (authority.Observation, error) {
			return forge.EmptyWriteRestrictionObservation(), forge.ErrRulesUnavailable
		}
	}
	reader := authorityObservationReader(func(context.Context) (authority.Observation, error) { return read(timedReadCtx) })
	writeTimeout := s.authorityWriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = completionWriteTimeout
	}
	return observeWriteRestriction(parent, s.store, w.id, reader, request.Input, writeTimeout)
}

// Persist only a bounded class, never transport text that may contain secrets.
func authorityReadFailureReason(cause error) string {
	var rate interface{ RetryAfter() time.Time }
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return authorityReadErrorReason + ": timeout"
	case errors.As(cause, &rate):
		return authorityReadErrorReason + ": quota"
	case errors.Is(cause, context.Canceled):
		return authorityReadErrorReason + ": canceled"
	default:
		return authorityReadErrorReason + ": unavailable"
	}
}
