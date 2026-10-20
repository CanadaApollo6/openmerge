package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// Journal is a queue's immutable seed and committed events at one DB snapshot.
type Journal struct {
	Key             QueueKey
	Seed            queue.State
	Events          []JournalEvent
	OutsideWarnings OutsideWarningSummary
}

// JournalEvent retains the persisted wire envelope for explainability clients.
type JournalEvent struct {
	ID         int64
	RecordedAt time.Time
	Payload    json.RawMessage
	Event      queue.Event
}

// Journals reads only explicitly served targets, without locks or writes. All
// targets and their event histories share a repeatable-read snapshot, so current
// state and explanations cannot disagree because of a concurrent append.
func (s *Store) Journals(ctx context.Context, keys []QueueKey) ([]Journal, error) {
	result := make([]Journal, 0, len(keys))
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		for _, key := range keys {
			journal := Journal{Key: key}
			var id QueueID
			err := tx.QueryRow(ctx, `SELECT id, base, COALESCE(initial_fence, '') FROM queues
				WHERE installation = $1 AND repository = $2 AND base = $3 AND lane = $4`,
				key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&id, &journal.Seed.BaseBranch, &journal.Seed.Fence)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id, recorded_at, payload FROM events WHERE queue_id = $1 ORDER BY id`, id)
			if err != nil {
				return err
			}
			journal.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (JournalEvent, error) {
				var e JournalEvent
				if err := row.Scan(&e.ID, &e.RecordedAt, &e.Payload); err != nil {
					return e, err
				}
				e.Event, err = queue.DecodeEvent(e.Payload)
				return e, err
			})
			if err != nil {
				return err
			}
			// Share the journal snapshot with the paged outstanding-warning ledger.
			// Its dedupe associations, rather than EventsAfter, establish eligibility.
			if err := visitOutsidePushWarnings(ctx, tx, id, journal.OutsideWarnings.add); err != nil {
				return err
			}
			result = append(result, journal)
		}
		return nil
	})
	return result, err
}
