package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// HeadChangedSince compares the last stable observation before a command with
// the current head. A one-second window covers GitHub timestamp precision and
// small clock skew; any differing head in that window is ambiguous and refused.
// No prior evidence permits an unpinned command on first discovery.
func (s *Store) HeadChangedSince(ctx context.Context, q QueueID, pr int, head string, createdAt time.Time) (bool, error) {
	const margin = time.Second
	var changed bool
	err := s.pool.QueryRow(ctx, `WITH history AS (
 SELECT id, recorded_at, payload->'data'->>'head_sha' AS head FROM events
 WHERE queue_id = $1 AND type = 'pr_observed' AND payload->'data'->>'pr' = $2
), baseline AS (
 SELECT id FROM history WHERE recorded_at < $3 ORDER BY recorded_at DESC, id DESC LIMIT 1
)
SELECT EXISTS (SELECT FROM history WHERE head <> $5 AND recorded_at <= $4
 AND id >= COALESCE((SELECT id FROM baseline), 0))`, q, fmt.Sprint(pr), createdAt.Add(-margin), createdAt.Add(margin), head).Scan(&changed)
	return changed, err
}

type record struct {
	key   string
	value []byte
}

// Record atomically updates an observation and appends its changed-state events.
// Equal bytes append nothing. Transitions A -> B -> A retain all three events.
// Command receipts use immutable comment IDs; cursors can commit without events.
func (s *Store) Record(ctx context.Context, q QueueID, key string, value []byte, events ...queue.Event) error {
	_, err := s.append(ctx, q, events, "", nil, &record{key, value})
	return err
}

// Observation reads durable reconciliation progress. Missing keys return nil.
func (s *Store) Observation(ctx context.Context, q QueueID, key string) ([]byte, error) {
	var value []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id = $1 AND key = $2`, q, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return value, err
}

// Notify commits delivery deduplication and its reconciliation request together.
func (s *Store) Notify(ctx context.Context, q QueueID, delivery string) error {
	_, err := s.pool.Exec(ctx, `WITH received AS (
 INSERT INTO webhook_deliveries(queue_id, delivery_id) VALUES ($1, $2)
 ON CONFLICT DO NOTHING RETURNING queue_id)
 UPDATE queues SET reconcile_requested = true WHERE id IN (SELECT queue_id FROM received)`, q, delivery)
	return err
}

// TakeNotification consumes a hint; polling and a full read after restart repair
// a crash after consumption. Notifications arriving during a read remain pending.
func (s *Store) TakeNotification(ctx context.Context, q QueueID) (bool, error) {
	var requested bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT reconcile_requested FROM queues WHERE id = $1 FOR UPDATE`, q).Scan(&requested); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE queues SET reconcile_requested = false WHERE id = $1`, q)
		return err
	})
	return requested, err
}
