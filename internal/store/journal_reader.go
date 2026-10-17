package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultJournalReadTimeout bounds the complete journal snapshot read.
const DefaultJournalReadTimeout = 2 * time.Minute

// JournalReader exposes immutable journals without migrations or application locks.
type JournalReader struct {
	store   *Store
	timeout time.Duration
}

// OpenJournalReader creates a connection pool with read-only transactions as a
// connection default. Journals additionally starts an explicit READ ONLY,
// REPEATABLE READ transaction around every query.
func OpenJournalReader(ctx context.Context, url string, timeout time.Duration) (*JournalReader, error) {
	if timeout < time.Millisecond {
		return nil, fmt.Errorf("journal read timeout must be at least 1ms")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database connection string: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = strconv.FormatInt(min(timeout, 5*time.Second).Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open journal reader: %w", err)
	}
	return &JournalReader{store: &Store{pool: pool}, timeout: timeout}, nil
}

// Journals delegates decoding and snapshot selection to the store reader.
func (r *JournalReader) Journals(ctx context.Context, keys []QueueKey) ([]Journal, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.store.Journals(ctx, keys)
}

// Close releases the connection pool.
func (r *JournalReader) Close() { r.store.Close() }
