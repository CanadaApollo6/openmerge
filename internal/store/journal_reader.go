package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JournalReader exposes only immutable journals. Unlike Open, its constructor
// never migrates, pings, creates queues, or acquires application locks.
type JournalReader struct{ store *Store }

// OpenJournalReader creates a connection pool with read-only transactions as a
// connection default. Journals additionally starts an explicit READ ONLY,
// REPEATABLE READ transaction around every query.
func OpenJournalReader(ctx context.Context, url string) (*JournalReader, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database connection string: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open journal reader: %w", err)
	}
	return &JournalReader{store: &Store{pool: pool}}, nil
}

// Journals delegates decoding and snapshot selection to the store reader.
func (r *JournalReader) Journals(ctx context.Context, keys []QueueKey) ([]Journal, error) {
	return r.store.Journals(ctx, keys)
}

// Close releases the connection pool.
func (r *JournalReader) Close() { r.store.Close() }
