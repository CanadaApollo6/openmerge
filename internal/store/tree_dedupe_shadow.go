package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TreeDedupeShadowKey is the observations row holding one queue's ADR 0015
// shadow ledger. It is deliberately not journal evidence: no event codec,
// fold, planner, landing path, published check, audit export or downgrade
// check reads it, and older binaries never look at it, so writing it is a
// two-way door. Only status and the savings report read it.
const TreeDedupeShadowKey = "tree-dedupe-shadow/v1"

// UpdateTreeDedupeShadow rewrites the queue's shadow ledger under a lock on
// that row alone. It never takes the queue advisory lock, appends events or
// touches fences, so it cannot delay, reorder or invalidate queue work. The
// update receives the current value (nil or empty when absent) and returns
// the replacement; nil leaves the row unchanged. Callers must treat errors as
// lost shadow evidence, never as queue failures.
func (s *Store) UpdateTreeDedupeShadow(ctx context.Context, q QueueID, update func(current []byte) ([]byte, error)) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Materialize the row first so concurrent first writers serialize on it.
		if _, err := tx.Exec(ctx, `INSERT INTO observations(queue_id, key, value) VALUES ($1, $2, ''::bytea) ON CONFLICT (queue_id, key) DO NOTHING`, q, TreeDedupeShadowKey); err != nil {
			return err
		}
		var current []byte
		if err := tx.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id = $1 AND key = $2 FOR UPDATE`, q, TreeDedupeShadowKey).Scan(&current); err != nil {
			return err
		}
		next, err := update(current)
		if err != nil || next == nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE observations SET value = $3 WHERE queue_id = $1 AND key = $2`, q, TreeDedupeShadowKey, next)
		return err
	})
}
