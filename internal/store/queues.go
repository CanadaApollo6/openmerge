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
