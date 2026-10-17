package store

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

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
	_, err := s.NotifyDelivery(ctx, q, delivery)
	return err
}

// NotifyDelivery also reports whether this delivery created a fresh hint.
func (s *Store) NotifyDelivery(ctx context.Context, q QueueID, delivery string) (bool, error) {
	result, err := s.pool.Exec(ctx, `WITH received AS (
 INSERT INTO webhook_deliveries(queue_id, delivery_id) VALUES ($1, $2)
 ON CONFLICT DO NOTHING RETURNING queue_id)
 UPDATE queues SET reconcile_requested = true WHERE id IN (SELECT queue_id FROM received)`, q, delivery)
	return result.RowsAffected() > 0, err
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

// EventRecord is one committed journal entry, ordered by ID within a queue.
type EventRecord struct {
	ID    int64
	Event queue.Event
}

// EventsAfter reads committed transitions for operational logging. It does not
// acknowledge them or participate in queue authorization.
func (s *Store) EventsAfter(ctx context.Context, q QueueID, after int64) ([]EventRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, payload FROM events WHERE queue_id = $1 AND id > $2 ORDER BY id`, q, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EventRecord
	for rows.Next() {
		var record EventRecord
		var payload []byte
		if err := rows.Scan(&record.ID, &payload); err != nil {
			return nil, err
		}
		record.Event, err = queue.DecodeEvent(payload)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}
