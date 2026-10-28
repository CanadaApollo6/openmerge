package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// ClaimedAction carries an ownership token that changes on every lease claim.
// A stale worker cannot begin or complete a replacement worker's claim.
type ClaimedAction struct {
	PlannedAction
	AttemptID  string
	LeaseUntil time.Time
}

// The explicit status predicate matches actions_claimable; its id order avoids
// sorting retained done/cancelled/poisoned history on every worker claim.
const claimQuery = `SELECT id, queue_id, key, payload, watermark FROM actions
    WHERE status IN ('planned', 'claimed', 'in_flight')
      AND (status = 'planned' OR lease_until <= $1)
    ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`

const claimQueueQuery = `SELECT id, queue_id, key, payload, watermark FROM actions
    WHERE status IN ('planned', 'claimed', 'in_flight')
      AND (status = 'planned' OR lease_until <= $1)
      AND queue_id = $3 AND key <> ALL($4::text[])
      AND (NOT $5::boolean OR type IN ('resolve_landing', 'fast_forward_resolve_landing'))
    ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`

// Claim skips rows locked by other claimers. Claimed and in-flight work becomes
// claimable at lease expiry; consequential work must always be revalidated.
func (s *Store) Claim(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]ClaimedAction, error) {
	return s.claim(ctx, 0, limit, lease, now, nil, false)
}

// ClaimQueue claims work only for a configured queue, excluding deferred keys.
func (s *Store) ClaimQueue(ctx context.Context, q QueueID, limit int, lease time.Duration, now time.Time, skip ...string) ([]ClaimedAction, error) {
	return s.claim(ctx, q, limit, lease, now, skip, false)
}

func (s *Store) claim(ctx context.Context, q QueueID, limit int, lease time.Duration, now time.Time, skip []string, settlement bool) ([]ClaimedAction, error) {
	if limit <= 0 || lease.Microseconds() <= 0 {
		return nil, errors.New("claim limit and lease must be positive")
	}
	var result []ClaimedAction
	poisonedQueues := make(map[QueueID]bool)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for len(result) < limit {
			query := claimQuery
			args := []any{now, limit - len(result)}
			if q != 0 {
				query = claimQueueQuery
				args = append(args, q, append([]string{}, skip...), settlement)
			}
			rows, err := tx.Query(ctx, query, args...)
			if err != nil {
				return err
			}
			records, err := pgx.CollectRows(rows, scanAction)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				break
			}
			for _, record := range records {
				action, err := restoreAction(record)
				if err != nil {
					if err := poisonAction(ctx, tx, record.id, err); err != nil {
						return err
					}
					poisonedQueues[record.queue] = true
					continue
				}
				var token [16]byte
				if _, err := rand.Read(token[:]); err != nil {
					return err
				}
				claim := ClaimedAction{PlannedAction: action, AttemptID: hex.EncodeToString(token[:])}
				if err := tx.QueryRow(ctx, `UPDATE actions SET status = 'claimed', attempt_id = $2,
					lease_until = $3, updated_at = $4, lease_duration_us = $5 WHERE id = $1 RETURNING lease_until`,
					record.id, claim.AttemptID, now.Add(lease), now, lease.Microseconds()).Scan(&claim.LeaseUntil); err != nil {
					return err
				}
				result = append(result, claim)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Release action locks before acquiring queue locks, matching Plan's lock
	// order. A crash here is repaired by the next Plan's notification drain.
	for q := range poisonedQueues {
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := s.lockQueue(ctx, tx, q); err != nil {
				return err
			}
			_, err := publishPoisonPauses(ctx, tx, q)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Consequential reports whether an action must pass the durable in-flight gate.
func Consequential(action queue.Action) bool {
	switch action.(type) {
	case queue.BuildChainedTestRef, queue.InvalidateBatches, queue.DiagnoseBatch, queue.BuildTestRef, queue.Land, queue.ResolveLanding, queue.FastForwardResolveLanding, queue.Dequeue, queue.FailBatch,
		queue.RecordCheckRerun, queue.RerunChecks, queue.ConfirmCheckFailure:
		return true
	default:
		return false
	}
}

// ErrClaimLost means the claim expired, was replaced or is no longer executable.
var ErrClaimLost = errors.New("action claim is no longer owned by this worker")

// ErrQueueBusy means another consequential action still owns its execution
// window. Retry this claim later, or release it with Complete(..., Failed).
var ErrQueueBusy = errors.New("another consequential action is in flight")

// ErrActionDeferred asks the worker to release paused work with Complete(Failed)
// and exclude it from further claims until resume, without consuming an outcome.
var ErrActionDeferred = errors.New("action deferred until queue resumes")

// BeginConsequential revalidates a claim under the queue planning lock and
// commits its in-flight record before the caller may perform any forge I/O.
// False with no error cancels a stale action. ErrQueueBusy and ErrActionDeferred
// leave work claimed for the caller to release with Complete(Failed).
// Beginning extends the lease by its original duration. Forge calls and result
// persistence must finish within that lease.
// ResolveLanding may proceed alongside the original Land it is resolving.
func (s *Store) BeginConsequential(ctx context.Context, a ClaimedAction, cfg queue.Config, now time.Time) (bool, error) {
	var proceed bool
	var decodeErr error
	var deferred bool
	switch a.Action.(type) {
	case queue.BuildTestRef, queue.BuildChainedTestRef:
	default:
		ctx = withoutDependencyFormationClock(ctx, a.QueueID)
	}
	ctx, err := s.prepareDependencyClocks(ctx, a.QueueID)
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, a.QueueID); err != nil {
			return err
		}
		var record actionRecord
		var kind string
		// Validate using the database row, not mutable fields supplied by callers.
		err := tx.QueryRow(ctx, `SELECT id, queue_id, key, payload, watermark, type FROM actions
			WHERE queue_id = $1 AND key = $2 AND attempt_id = $3
			AND status = 'claimed' AND lease_until > $4 FOR UPDATE`, a.QueueID, a.Key, a.AttemptID, now).
			Scan(&record.id, &record.queue, &record.key, &record.payload, &record.watermark, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClaimLost
		}
		if err != nil {
			return err
		}
		stored, err := restoreAction(record)
		if err != nil {
			decodeErr = err
			if err := poisonAction(ctx, tx, record.id, err); err != nil {
				return err
			}
			_, err := publishPoisonPauses(ctx, tx, a.QueueID)
			return err
		}
		if !Consequential(stored.Action) {
			return errors.New("action does not require consequential validation")
		}
		state, watermark, err := load(ctx, tx, a.QueueID)
		if err != nil {
			return err
		}
		if watermark > record.watermark {
			current := false
			for _, planned := range queue.Plan(cfg, state, now) {
				if planned.Key() == record.key {
					current = true
					break
				}
			}
			if !current {
				if queue.DeferredByPause(cfg, state, stored.Action, now) {
					deferred = true
					return nil
				}
				_, err := tx.Exec(ctx, `UPDATE actions SET status = 'cancelled', attempt_id = NULL,
					lease_until = NULL, updated_at = $2 WHERE id = $1`, record.id, now)
				return err
			}
		}
		var conflicting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM actions WHERE queue_id = $1
			AND id <> $2 AND status = 'in_flight' AND lease_until > $4
			AND NOT ($3 IN ('resolve_landing', 'fast_forward_resolve_landing') AND type = 'land'))`, a.QueueID, record.id, kind, now).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting {
			return ErrQueueBusy
		}
		_, err = tx.Exec(ctx, `UPDATE actions SET status = 'in_flight', updated_at = $2,
			lease_until = $2::timestamptz + lease_duration_us * interval '1 microsecond' WHERE id = $1`, record.id, now)
		proceed = err == nil
		return err
	})
	if err != nil {
		return false, err
	}
	if deferred {
		return false, ErrActionDeferred
	}
	return proceed, decodeErr
}

// Completion selects success or retry. Failed means retryable infrastructure
// failure; definitive queue outcomes are events, not an action failure status.
type Completion string

const (
	// Done records successful execution.
	Done Completion = "done"
	// Failed releases the action back to planned for revalidation and retry.
	Failed Completion = "failed"
	// Cancelled records an action superseded by the current unpaused plan.
	Cancelled Completion = "cancelled"
)

// Complete changes only the caller's current claim. A failed Land whose
// LandingStarted committed is cancelled by BeginConsequential on its next claim;
// the planner produces ResolveLanding rather than executing Land again.
func (s *Store) Complete(ctx context.Context, a ClaimedAction, status Completion) error {
	var next string
	switch status {
	case Done:
		next = "done"
	case Failed:
		next = "planned"
	case Cancelled:
		next = "cancelled"
	default:
		return errors.New("completion must be done, failed or cancelled")
	}
	result, err := s.pool.Exec(ctx, `UPDATE actions SET status = $4, attempt_id = NULL,
		lease_until = NULL, updated_at = now() WHERE queue_id = $1 AND key = $2
		AND attempt_id = $3 AND status IN ('claimed', 'in_flight')`, a.QueueID, a.Key, a.AttemptID, next)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

// ClaimSettlement cannot consume earlier reporting, cleanup, or admission work.
func (s *Store) ClaimSettlement(ctx context.Context, q QueueID, lease time.Duration, now time.Time) ([]ClaimedAction, error) {
	return s.claim(ctx, q, 1, lease, now, nil, true)
}
