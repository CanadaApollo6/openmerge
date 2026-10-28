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
// Append/EmitWithPolicy/Record, landing-start and outside-push writers: queue
// advisory -> action/observation locks as needed -> events ROW EXCLUSIVE INSERT
// -> queues FK KEY SHARE. appendSideTx inherits the already-held queue
// advisory from startLandingWithSide (or AppendSide/RecoverLanded); it takes
// observations INSERT/UPDATE locks only AFTER that advisory and existing action
// row locks. Observation INSERTs also take queues FK KEY SHARE. None of these
// paths takes queues FOR UPDATE or updates its key, and no observation holder
// subsequently waits for an action row: there is no action/observation inversion.
// Notify's queues UPDATE takes NO KEY UPDATE and is compatible with FK KEY SHARE;
// TakeNotification's FOR UPDATE can delay inserts but never waits on the queue
// advisory/action/observation locks. Shared append never acquires a second tx.
// Activated dependency landing start (ADR 0011 slice 4a, Hold): source-queue
// resolution and each direct source's settlement-prefix verification are
// lock-free reads (that prefix is immutable once its row commits); then the
// queue advisories of B plus its direct sources in ascending queue-ID order,
// all before any other lock in the transaction, never a source advisory while
// already holding B's -> then exactly the landing-start order above. Two
// starts with overlapping sets thus cannot deadlock on queue advisories.
// Plan/BeginConsequential/poison publication take queue
// advisory -> actions row locks -> event INSERT/FK locks. Claim/ClaimQueue commit
// action row locks before a separate queue-locked poison-publication transaction;
// Complete updates action rows only. None acquires tenancy after a queue lock.
// Verification commit, AdoptAuthorityPin, ApplyAuthorityConfig: when gated,
// exclusive tenancy FIRST -> ascending queue advisories -> tenancy/boundary reads
// -> events SHARE ROW EXCLUSIVE -> events INSERT/FK KEY SHARE. Ungated paths
// start at queue advisory and do not acquire tenancy. Preflight has no queue lock.
// Emission never upgrades a queues table lock. Notification holders never wait
// for tenancy, queue advisories or events; registration never waits for event write locks or
// queue advisories. Thus neither can close a cycle through an emitter's FK lock.
func (s *Store) lockTenancy(ctx context.Context, tx pgx.Tx, shared bool) error {
	if shared {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(1330464084, hashtext($1))`, s.testLockScope)
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1330464084, hashtext($1))`, s.testLockScope)
	return err
}
