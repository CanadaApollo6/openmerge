package store

import (
	"context"
	"errors"
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
// boundary. Proving absence scans the discriminator, and a later lookup may
// scan the legacy prefix before its first match. No index migration is added.
func (s *Store) AuthorityBoundaryExists(ctx context.Context) (bool, error) {
	var boundary bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE type=ANY($1) LIMIT 1)`, authorityEmissionEventTypes).Scan(&boundary)
	return boundary, err
}

// CheckAuthorityEmissionBackup is a preflight only. The committing transaction
// must also call checkAuthorityEmission while holding the global append lock.
func (s *Store) CheckAuthorityEmissionBackup(ctx context.Context, watermark int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return checkAuthorityEmission(ctx, tx, AuthorityEmissionGate{Watermark: watermark})
	})
}

func checkAuthorityEmission(ctx context.Context, tx pgx.Tx, gate AuthorityEmissionGate) error {
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
	// READ COMMITTED takes a fresh snapshot after any lock wait.
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
