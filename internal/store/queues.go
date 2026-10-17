package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// QueueKeys includes historical partitions so startup can recover attempts even
// when current configuration and process allowlists no longer select them.
func (s *Store) QueueKeys(ctx context.Context) ([]QueueKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT installation, repository, base, lane FROM queues ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (QueueKey, error) {
		var k QueueKey
		err := row.Scan(&k.Installation, &k.Repository, &k.BaseBranch, &k.Lane)
		return k, err
	})
}

// StoredQueue preserves the persisted identity even for pre-canonical histories.
type StoredQueue struct {
	ID  QueueID
	Key QueueKey
}

// StoredQueues lists every historical partition by its original persisted ID.
func (s *Store) StoredQueues(ctx context.Context) ([]StoredQueue, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, installation, repository, base, lane FROM queues ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StoredQueue, error) {
		var q StoredQueue
		err := row.Scan(&q.ID, &q.Key.Installation, &q.Key.Repository, &q.Key.BaseBranch, &q.Key.Lane)
		return q, err
	})
}
