package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// LandingDecisionRequiredError means store policy requires the atomic gate.
// It is a wiring failure, never stale work: workers must fail the Land action.
type LandingDecisionRequiredError struct{}

func (LandingDecisionRequiredError) Error() string {
	return "approval policy requires landing decision gate"
}

// LandingChecksRequiredError identifies an authority start bypassing the atomic
// check-evidence seam. Like the approval equivalent, this is a wiring failure.
type LandingChecksRequiredError struct{}

func (LandingChecksRequiredError) Error() string {
	return "authority policy requires atomic landing check evidence"
}

// ErrLandingDecisionStale asks the worker to re-plan after an atomic rollback.
// It also wraps ErrStale, but must not use legacy starts' done/cancelled path.
var ErrLandingDecisionStale = errors.New("landing decision inputs changed; waiting for stable approval evidence; re-plan")

func retryLandingDecision(err error) error {
	if errors.Is(err, ErrStale) {
		return fmt.Errorf("%w: %w", ErrLandingDecisionStale, err)
	}
	return err
}

// StartLanding authorizes the existing Land action and atomically records the
// complete decision set, its exact fresh eligibility, and unchanged start.
// All forge reads must finish before this call. No callback runs under the lock.
// Unlike append's legacy cancellation path, any stale authorization returns an
// error INSIDE the transaction, rolling back even authorization side effects.
func (s *Store) StartLanding(ctx context.Context, claim ClaimedAction, cfg queue.Config, policy cp.Policy, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted, checks ...queue.LandingChecksRecorded) error {
	return retryLandingDecision(s.startLanding(ctx, claim, cfg, policy, fresh, decisions, start, s.authorizeStart, checks...))
}

func (s *Store) startLanding(ctx context.Context, claim ClaimedAction, cfg queue.Config, policy cp.Policy, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted, authorize func(context.Context, pgx.Tx, emission, queue.Event) (bool, error), checks ...queue.LandingChecksRecorded) error {
	if !policy.Enabled() && (len(fresh) != 0 || len(decisions) != 0) {
		return LandingDecisionRequiredError{}
	}
	// Charge lock waits and validation time to the lease in the worker clock.
	// Legacy starts retain their original timestamp behavior.
	entered := time.Now()
	currentStart := func() queue.LandingStarted {
		event := start
		if policy.Enabled() || len(checks) != 0 {
			event.At = event.At.Add(time.Since(entered))
		}
		return event
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, claim.QueueID); err != nil {
			return err
		}
		if err := checkApprovalRead(ctx, tx, claim.QueueID); err != nil {
			return err
		}
		execution := emission{claim: claim, cfg: cfg, decisionStart: true, approvalsRequired: policy.Enabled()}
		stale, err := authorize(ctx, tx, execution, currentStart())
		if err != nil {
			return err
		}
		if stale {
			return ErrStale
		}
		state, _, err := load(ctx, tx, claim.QueueID)
		if err != nil {
			return err
		}
		land, ok := claim.Action.(queue.Land)
		if !ok {
			return ErrStale
		}
		if policy.Enabled() {
			if err := state.ValidateLandingDecisions(policy, land, start, fresh, decisions); err != nil {
				return fmt.Errorf("%w: %w", ErrStale, err)
			}
		}
		pin := state.AuthorityPin()
		authority := pin != nil && pin.Authority == "openmerge"
		if authority {
			if len(checks) != 1 {
				return LandingChecksRequiredError{}
			}
			if err := state.ValidateLandingChecks(policy.Digest, land, start, checks[0]); err != nil {
				if errors.Is(err, queue.ErrLandingChecksTooLarge) {
					return err
				}
				return fmt.Errorf("%w: %w", ErrStale, err)
			}
		} else if len(checks) != 0 {
			return fmt.Errorf("%w: check evidence requires authority", ErrStale)
		}
		events := make([]queue.Event, 0, len(fresh)+len(decisions)+len(checks)+1)
		for i := range decisions {
			events = append(events, fresh[i], decisions[i])
		}
		for _, evidence := range checks {
			events = append(events, evidence)
		}
		events = append(events, start)
		// Pre-encode the whole operation before inserting. SQL failures still roll
		// back every insert. The final authorization is deliberately last, so a
		// stale final start cannot commit even a fresh eligibility observation.
		type encoded struct {
			name    string
			payload []byte
		}
		encodedEvents := make([]encoded, 0, len(events))
		for _, event := range events {
			name, payload, err := queue.EncodeEvent(event)
			if err != nil {
				return err
			}
			encodedEvents = append(encodedEvents, encoded{name, payload})
		}
		stale, err = authorize(ctx, tx, execution, currentStart())
		if err != nil {
			return err
		}
		if stale || !queue.CanStart(cfg, state, claim.Action, currentStart()) {
			return ErrStale
		}
		for _, event := range encodedEvents {
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb)`, claim.QueueID, event.name, string(event.payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RejectLanding journals fresh eligibility and the resulting waits in one
// transaction. expected pins pre-read inputs, so a permission/head/receipt race
// rejects this rejection too; a stale read must never regress or wake a wait.
func (s *Store) RejectLanding(ctx context.Context, claim ClaimedAction, cfg queue.Config, policy cp.Policy, fresh []queue.ApprovalEligibilityObserved, expected []string, start queue.LandingStarted, rejection queue.ApprovalGateRejected) (result error) {
	defer func() { result = retryLandingDecision(result) }()
	if !policy.Enabled() {
		return LandingDecisionRequiredError{}
	}
	entered := time.Now()
	currentStart := func() queue.LandingStarted {
		event := start
		event.At = event.At.Add(time.Since(entered))
		return event
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, claim.QueueID); err != nil {
			return err
		}
		if err := checkApprovalRead(ctx, tx, claim.QueueID); err != nil {
			return err
		}
		execution := emission{claim: claim, cfg: cfg, decisionStart: true, approvalsRequired: policy.Enabled()}
		stale, err := s.authorizeStart(ctx, tx, execution, currentStart())
		if err != nil {
			return err
		}
		if stale {
			return ErrStale
		}
		state, _, err := load(ctx, tx, claim.QueueID)
		if err != nil {
			return err
		}
		land, ok := claim.Action.(queue.Land)
		if !ok || len(expected) != len(land.PRs) || len(fresh) != len(land.PRs) {
			return ErrStale
		}
		bindings := state.BatchRevisions(land.BatchID)
		if len(bindings) != len(land.PRs) {
			return ErrStale
		}
		for i, f := range fresh {
			r, ok := state.PRRevision(land.PRs[i].PR)
			if !ok || f.Basis.ChangeID != r.ChangeID || f.Basis.Revision != bindings[i].Revision || f.Basis.Head != land.PRs[i].HeadSHA || expected[i] != state.ApprovalFingerprint(r.ChangeID) || f.Basis.At.After(start.At) {
				return ErrStale
			}
		}
		actual, err := state.PrepareLandingRejection(policy, land, fresh)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStale, err)
		}
		if len(actual.Waiting) == 0 || !reflect.DeepEqual(actual, rejection) {
			return ErrStale
		}
		events := make([]queue.Event, 0, len(fresh)+1)
		for _, f := range fresh {
			events = append(events, f)
		}
		events = append(events, actual)
		// Validate encodings and the final start authorization before any insert.
		payloads := make([][]byte, len(events))
		names := make([]string, len(events))
		for i, e := range events {
			names[i], payloads[i], err = queue.EncodeEvent(e)
			if err != nil {
				return err
			}
		}
		stale, err = s.authorizeStart(ctx, tx, execution, currentStart())
		if err != nil {
			return err
		}
		if stale {
			return ErrStale
		}
		for i := range events {
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id,type,payload) VALUES ($1,$2,$3::jsonb)`, claim.QueueID, names[i], string(payloads[i])); err != nil {
				return err
			}
		}
		return nil
	})
}
