package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var lockTestKey = QueueKey{Installation: "installation/1", Repository: "Owner/Repo", BaseBranch: "main"}

// These literal legacy keys deliberately do not share implementation constants.
func TestProductionAdvisoryLockKeysUnchanged(t *testing.T) {
	for _, emptyOption := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_options", true: "empty_scope"}[emptyOption], func(t *testing.T) {
			raw := databaseURL(t)
			var options []Option
			if emptyOption {
				options = append(options, WithTestLockScope(""))
			}
			s, err := Open(t.Context(), raw, options...)
			must(t, err)
			defer s.Close()
			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			defer func() { _ = tx.Rollback(context.Background()) }()
			must(t, s.lockQueue(t.Context(), tx, 1))
			must(t, s.lockRegistration(t.Context(), tx, lockTestKey))
			must(t, s.lockMigration(t.Context(), tx))
			assertLocksHeld(t, s, []string{
				`SELECT pg_try_advisory_xact_lock(1330464081, hashtext('1'))`,
				`SELECT pg_try_advisory_xact_lock(1330464082, hashtext('installation/1/owner/repo/main/'))`,
				`SELECT pg_try_advisory_xact_lock(-7046029254386353131::bigint)`,
			})
		})
	}
}

func TestAdvisoryLockScopes(t *testing.T) {
	s := newStore(t)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, 1))
	must(t, s.lockRegistration(t.Context(), tx, lockTestKey))
	must(t, s.lockMigration(t.Context(), tx))

	// Another connection in the same schema must still contend on every lock.
	assertLocksHeld(t, s, []string{
		`SELECT pg_try_advisory_xact_lock(1330464081, hashtext($1 || '/1'))`,
		`SELECT pg_try_advisory_xact_lock(1330464082, hashtext($1 || '/installation/1/owner/repo/main/'))`,
		`SELECT pg_try_advisory_xact_lock(1330464083, hashtext($1))`,
	}, s.testLockScope)

	// A different schema can migrate, register the same queue and lock queue 1
	// while the first schema's transactions remain open.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw := databaseURL(t)
	other, err := Open(ctx, raw, WithTestLockScope(testLockScope(t, raw)))
	must(t, err)
	defer other.Close()
	q, err := other.EnsureQueue(ctx, lockTestKey)
	must(t, err)
	if q != 1 {
		t.Fatalf("first queue = %d, want 1", q)
	}
	must(t, pgx.BeginFunc(ctx, other.pool, func(otherTx pgx.Tx) error {
		if err := other.lockQueue(ctx, otherTx, q); err != nil {
			return err
		}
		if err := other.lockRegistration(ctx, otherTx, lockTestKey); err != nil {
			return err
		}
		return other.lockMigration(ctx, otherTx)
	}))
}

func assertLocksHeld(t *testing.T, s *Store, queries []string, args ...any) {
	t.Helper()
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, query := range queries {
		var acquired bool
		must(t, tx.QueryRow(t.Context(), query, args...).Scan(&acquired))
		if acquired {
			t.Errorf("expected lock held by other connection: %s", query)
		}
	}
}
