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

// Claim skips rows locked by other claimers. Claimed and in-flight work becomes
// claimable at lease expiry; consequential work must always be revalidated.
func (s *Store) Claim(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]ClaimedAction, error) {
	if limit <= 0 || lease <= 0 {
		return nil, errors.New("claim limit and lease must be positive")
	}
	var result []ClaimedAction
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, queue_id, key, payload, watermark FROM actions
			WHERE status = 'planned' OR (status IN ('claimed', 'in_flight') AND lease_until <= $1)
			ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		records, err := pgx.CollectRows(rows, scanAction)
		if err != nil {
			return err
		}
		for _, record := range records {
			action, err := restoreAction(ctx, tx, record)
			if err != nil {
				return err
			}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return err
			}
			claim := ClaimedAction{PlannedAction: action, AttemptID: hex.EncodeToString(token[:])}
			if err := tx.QueryRow(ctx, `UPDATE actions SET status = 'claimed', attempt_id = $2,
				lease_until = $3, updated_at = $4 WHERE id = $1 RETURNING lease_until`, record.id, claim.AttemptID, now.Add(lease), now).Scan(&claim.LeaseUntil); err != nil {
				return err
			}
			result = append(result, claim)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Consequential reports whether an action must pass the durable in-flight gate.
func Consequential(action queue.Action) bool {
	switch action.(type) {
	case queue.BuildTestRef, queue.Land, queue.ResolveLanding, queue.Dequeue:
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

// BeginConsequential revalidates a claim under the queue planning lock and
// commits its in-flight record before the caller may perform any forge I/O.
// False with no error cancels a stale action. ErrQueueBusy leaves work claimed.
// ResolveLanding may proceed alongside the original Land it is resolving.
func (s *Store) BeginConsequential(ctx context.Context, a ClaimedAction, cfg queue.Config, now time.Time) (bool, error) {
	var proceed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, a.QueueID); err != nil {
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
		stored, err := restoreAction(ctx, tx, record)
		if err != nil {
			return err
		}
		if !Consequential(stored.Action) {
			return errors.New("action does not require consequential validation")
		}
		state, watermark, err := load(ctx, tx, a.QueueID, nil)
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
				_, err := tx.Exec(ctx, `UPDATE actions SET status = 'cancelled', attempt_id = NULL,
					lease_until = NULL, updated_at = $2 WHERE id = $1`, record.id, now)
				return err
			}
		}
		var conflicting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM actions WHERE queue_id = $1
			AND id <> $2 AND status = 'in_flight' AND lease_until > $4
			AND NOT ($3 = 'resolve_landing' AND type = 'land'))`, a.QueueID, record.id, kind, now).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting {
			return ErrQueueBusy
		}
		_, err = tx.Exec(ctx, `UPDATE actions SET status = 'in_flight', updated_at = $2 WHERE id = $1`, record.id, now)
		proceed = err == nil
		return err
	})
	return proceed, err
}

// Completion selects success or retry. Failed means retryable infrastructure
// failure; definitive queue outcomes are events, not an action failure status.
type Completion string

const (
	// Done records successful execution.
	Done Completion = "done"
	// Failed releases the action back to planned for revalidation and retry.
	Failed Completion = "failed"
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
	default:
		return errors.New("completion must be done or failed")
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
