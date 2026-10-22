package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Authority emission is narrower than binary downgrade compatibility. In
// particular, a legacy outside-push acknowledgement has its own operator receipt
// contract and cannot waive the database-bound authority emission gate.
var authorityEmissionEventTypes = []string{
	"policy_authority_applied", "write_restriction_observed", "write_restriction_verified",
}

// AuthorityEmissionGate is supplied by the operator emission path. Validate
// rechecks the receipt window without network or database I/O.
type AuthorityEmissionGate struct {
	Watermark int64
	Validate  func() error
}

// AuthorityBoundaryExists uses only the event discriminator, never JSON payloads.
// There is no type-only index in the retained schema: LIMIT 1 stops at the first
// boundary. Cache only positive results: boundaries are monotonic while this
// process runs. A restore under a live process would invalidate this cache;
// the documented restore procedure requires stopping all workers first.
// Negative results must query again to notice another process emitting.
func (s *Store) AuthorityBoundaryExists(ctx context.Context) (bool, error) {
	if s.authorityBoundary.Load() {
		return true, nil
	}
	var boundary bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE type=ANY($1) LIMIT 1)`, authorityEmissionEventTypes).Scan(&boundary)
	if err == nil && boundary {
		s.authorityBoundary.Store(true)
	}
	return boundary, err
}

// CheckAuthorityEmissionBackup is a preflight only. The committing transaction
// must also call checkAuthorityEmission while holding the global append lock.
func (s *Store) CheckAuthorityEmissionBackup(ctx context.Context, watermark int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := s.lockTenancy(ctx, tx, false); err != nil {
			return err
		}
		return checkAuthorityEmission(ctx, tx, AuthorityEmissionGate{Watermark: watermark})
	})
}

// AuthorityTenancyError exposes only the installation count, never queue identities.
type AuthorityTenancyError struct{ Installations int }

func (e *AuthorityTenancyError) Error() string {
	return fmt.Sprintf("authority mode requires a dedicated database per installation: this database serves %d installations", e.Installations)
}

// CheckAuthorityTenancy inventories ALL queues, regardless of repository, lane,
// lifecycle or discovery membership. The verdict is never cached.
func (s *Store) CheckAuthorityTenancy(ctx context.Context) error {
	return checkAuthorityTenancy(ctx, s.pool)
}

func checkAuthorityTenancy(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var count int
	if err := db.QueryRow(ctx, `SELECT count(DISTINCT installation) FROM queues`).Scan(&count); err != nil {
		return errors.New("authority database tenancy inventory unavailable")
	}
	if count > 1 {
		return &AuthorityTenancyError{Installations: count}
	}
	return nil
}

func checkAuthorityEmission(ctx context.Context, tx pgx.Tx, gate AuthorityEmissionGate) error {
	// The caller holds exclusive tenancy from its FIRST lock (see locks.go).
	// This post-boundary inventory is defense in depth for explicit gate callers;
	// production supplies no gate once a boundary exists.
	if err := checkAuthorityTenancy(ctx, tx); err != nil {
		return err
	}

	var boundary bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE type=ANY($1) LIMIT 1)`, authorityEmissionEventTypes).Scan(&boundary); err != nil {
		return errors.New("first-emission journal inventory unavailable")
	}
	if boundary {
		return nil
	}
	// INSERT takes ROW EXCLUSIVE even for other queues. SHARE ROW EXCLUSIVE
	// conflicts with it and itself, serializing the one-time boundary against all
	// appends without changing ordinary append paths. Queue locks come first so
	// a writer already holding a queue can finish before we acquire this lock.
	if _, err := tx.Exec(ctx, `LOCK TABLE events IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	// Callers explicitly select READ COMMITTED so role/DSN isolation defaults
	// cannot retain the pre-lock snapshot after waiting for an append.
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE type=ANY($1) LIMIT 1)`, authorityEmissionEventTypes).Scan(&boundary); err != nil {
		return err
	}
	if boundary {
		return nil
	}
	var live int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events`).Scan(&live); err != nil {
		return err
	}
	if live != gate.Watermark {
		return errors.New("first-emission backup watermark differs from live journal; repeat drain, backup and restore test")
	}
	if gate.Validate != nil {
		return gate.Validate()
	}
	return nil
}
