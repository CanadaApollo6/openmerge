package store

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// ErrBaseObservationStale requests a fresh observation after another worker moved
// the recorded base or started a landing. Never classify across an unsettled attempt.
var ErrBaseObservationStale = errors.New("base observation overlapped a base change or landing; retry reconciliation")

// RecordBaseMove commits the audit record and the existing BaseMoved atomically.
// watermark must come from Load before reading git, not after. The queue lock
// rejects any intervening journal change, including a complete peer landing or
// a base moving away and back, and deduplicates competing observers. During an
// unchanged outstanding attempt, nil evidence preserves ordinary BaseMoved
// observation without classifying it. Settlement supplies the audit baseline.
// A first observation establishes the baseline, with no invented outside history.
func (s *Store) RecordBaseMove(ctx context.Context, q QueueID, watermark int64, previous, tip string, outside *queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		state, currentWatermark, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		landing := state.Active != nil && state.Active.Phase == queue.Landing
		if landing && outside != nil {
			return ErrBaseObservationStale
		}
		if state.BaseSHA == tip {
			return nil
		}
		if state.BaseSHA != previous || currentWatermark != watermark {
			return ErrBaseObservationStale
		}
		events := []queue.Event{}
		if previous != "" && !landing {
			if outside == nil || outside.PreviousSHA != previous || outside.NewSHA != tip {
				return errors.New("missing outside push evidence")
			}
			events = append(events, *outside)
		}
		events = append(events, queue.BaseMoved{SHA: tip})
		for _, event := range events {
			name, payload, err := queue.EncodeEvent(event)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb)`, q, name, string(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordLandingRewrite audits a proven landing removed before settlement. It
// deliberately emits no BaseMoved or settlement event: the safety block and
// outstanding attempt must remain. One durable receipt per attempt survives
// retries, competing servers, and restarts.
func (s *Store) RecordLandingRewrite(ctx context.Context, q QueueID, watermark int64, attemptID string, outside queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		state, currentWatermark, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		b := state.Active
		if currentWatermark != watermark || b == nil || b.Phase != queue.Landing || b.LandingAttempt.ID != attemptID || b.LandingCommit() != outside.PreviousSHA {
			return ErrBaseObservationStale
		}
		if !outside.AncestryKnown || !outside.Rewrite {
			return errors.New("missing landing rewrite evidence")
		}
		name, payload, err := queue.EncodeEvent(outside)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload, dedupe_key) VALUES ($1, $2, $3::jsonb, $4)
			ON CONFLICT (queue_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`, q, name, string(payload), "outside-landing-rewrite/"+attemptID)
		return err
	})
}

// RecordAbandonedBaseMove commits abandonment and the audit of an already
// observed base atomically. The fence-certified abandonment proves this attempt
// cannot have made that move. BaseMoved has already applied at its original time.
func (s *Store) RecordAbandonedBaseMove(ctx context.Context, q QueueID, watermark int64, abandoned queue.LandingAbandoned, outside queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		state, currentWatermark, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		b := state.Active
		if currentWatermark != watermark || state.BaseSHA != outside.NewSHA || b == nil || b.Phase != queue.Landing || b.ID != abandoned.BatchID || b.BaseSHA != outside.PreviousSHA {
			return ErrBaseObservationStale
		}
		for _, event := range []queue.Event{abandoned, outside} {
			name, payload, err := queue.EncodeEvent(event)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb)`, q, name, string(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}
