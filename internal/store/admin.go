package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenAdmin connects with operator credentials without startup migrations or
// queue registration. Command and database deadlines bound scans and lock waits.
func OpenAdmin(ctx context.Context, url string, timeout time.Duration) (*Store, error) {
	if timeout < time.Millisecond || timeout > DefaultJournalReadTimeout {
		return nil, fmt.Errorf("admin timeout must be 1ms..%s", DefaultJournalReadTimeout)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse admin database URL: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	for _, name := range []string{"statement_timeout", "lock_timeout", "idle_in_transaction_session_timeout"} {
		cfg.ConnConfig.RuntimeParams[name] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open admin database: %w", err)
	}
	return &Store{pool: pool}, nil
}
