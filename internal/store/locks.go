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
