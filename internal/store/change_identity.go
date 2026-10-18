package store

import (
	"context"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// ChangeID derives a PR's identity from the canonical key returned by
// CanonicalQueue. Callers must not pass an incoming discovery case alias.
func (key QueueKey) ChangeID(pr int) (queue.ChangeID, error) {
	return queue.PRChangeID(key.Installation, key.Repository, key.BaseBranch, key.Lane, pr)
}

// This runs under append's queue lock and returns an error to roll back the
// entire delivery, including dedupe/observation writes and earlier inserts.
func validateChangeRevision(ctx context.Context, tx pgx.Tx, q QueueID, event queue.ChangeRevisionObserved) error {
	state, _, err := load(ctx, tx, q)
	if err != nil {
		return err
	}
	var key QueueKey
	if err := tx.QueryRow(ctx, `SELECT installation, repository, base, lane FROM queues WHERE id=$1`, q).Scan(&key.Installation, &key.Repository, &key.BaseBranch, &key.Lane); err != nil {
		return err
	}
	id, err := key.ChangeID(event.PR)
	if err != nil {
		return err
	}
	if event.ChangeID != id || !state.CanObserveChangeRevision(event) {
		return ErrStale
	}
	return nil
}
