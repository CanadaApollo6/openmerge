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
// The queue lock excludes LandingStarted and deduplicates competing observers.
// A first observation establishes the baseline, with no invented outside history.
func (s *Store) RecordBaseMove(ctx context.Context, q QueueID, previous, tip string, outside *queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		state, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		if state.Active != nil && state.Active.Phase == queue.Landing {
			return ErrBaseObservationStale
		}
		if state.BaseSHA == tip {
			return nil
		}
		if state.BaseSHA != previous {
			return ErrBaseObservationStale
		}
		events := []queue.Event{}
		if previous != "" {
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
