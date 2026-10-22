package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) lockQueue(ctx context.Context, tx pgx.Tx, q QueueID) error {
	if s.beforeQueueLock != nil {
		s.beforeQueueLock()
	}
	if s.testLockScope != "" {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464081, hashtext($1 || '/' || $2))`, s.testLockScope, fmt.Sprint(q))
		return err
	}
	// A dedicated two-int namespace keeps queue hashes separate from migrations.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464081, hashtext($1))`, fmt.Sprint(q))
	return err
}

func (s *Store) lockRegistration(ctx context.Context, tx pgx.Tx, key QueueKey) error {
	if s.testLockScope != "" {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464082, hashtext($5 || '/' || $1 || '/' || lower($2) || '/' || $3 || '/' || $4))`, key.Installation, key.Repository, key.BaseBranch, key.Lane, s.testLockScope)
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464082, hashtext($1 || '/' || lower($2) || '/' || $3 || '/' || $4))`, key.Installation, key.Repository, key.BaseBranch, key.Lane)
	return err
}

func (s *Store) lockMigration(ctx context.Context, tx pgx.Tx) error {
	if s.testLockScope != "" {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464083, hashtext($1))`, s.testLockScope)
		return err
	}
	// The production bigint key is disjoint from the two-int lock space.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(-7046029254386353131::bigint)`)
	return err
}

// Tenancy uses two-int namespace 1330464084, disjoint from queue (4081),
// registration (4082), test migration (4083), and PostgreSQL's bigint key space.
// Lock order inventory (all locks are transaction scoped):
// registration: shared tenancy -> registration key -> queues INSERT/FK row locks
// -> canonical_queues upsert; history reads take only ACCESS SHARE on events.
// Notify: delivery INSERT/FK KEY SHARE -> queues UPDATE (ROW EXCLUSIVE/row lock).
// TakeNotification: queues ROW SHARE/FOR UPDATE -> UPDATE ROW EXCLUSIVE.
// InitializeFence: queue advisory -> queues UPDATE -> events ACCESS SHARE read.
// Append/EmitWithPolicy: queue advisory -> events ROW EXCLUSIVE INSERT -> queues
// FK KEY SHARE (and action/observation locks under the same queue advisory).
// Verification commit, AdoptAuthorityPin, ApplyAuthorityConfig: when gated,
// exclusive tenancy FIRST -> ascending queue advisories -> tenancy/boundary reads
// -> events SHARE ROW EXCLUSIVE -> events INSERT/FK KEY SHARE. Ungated paths
// start at queue advisory and do not acquire tenancy. Preflight has no queue lock.
// No queues table lock is upgraded. Notification holders never wait for tenancy,
// queue advisories or events; registration never waits for event write locks or
// queue advisories. Thus neither can close a cycle through an emitter's FK lock.
func (s *Store) lockTenancy(ctx context.Context, tx pgx.Tx, shared bool) error {
	if shared {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(1330464084, hashtext($1))`, s.testLockScope)
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464084, hashtext($1))`, s.testLockScope)
	return err
}
