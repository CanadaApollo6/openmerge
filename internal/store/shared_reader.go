package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

type journalQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func sharedReader(ctx context.Context, db journalQuery, q QueueID, seed queue.State) journalreader.Reader {
	return journalreader.New(seed, dependency.JournalInitialized{}, func(id uint64) ([]byte, error) {
		var raw []byte
		err := db.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, id).Scan(&raw)
		return raw, err
	})
}

// Close rows before reference lookups: pgx cannot issue a second query while a
// transaction's connection is streaming. The transaction pins one DB snapshot.
func replayJournal(ctx context.Context, db journalQuery, q QueueID, seed queue.State, through int64, strictCheckpoint bool) (journalreader.Reader, int64, error) {
	r := sharedReader(ctx, db, q, seed)
	type record struct {
		id  int64
		raw []byte
	}
	var watermark int64
	// The append checkpoint is independent of outside-push audit progress: using
	// it must never skip unaudited base moves. Both are derived, validated prefixes.
	rows, err := db.Query(ctx, `SELECT key, value FROM observations WHERE queue_id=$1 AND key IN ($2,$3)`, q, sideCheckpointKey, outsidePushCursorKey)
	if err != nil {
		return r, 0, err
	}
	type checkpointValue struct {
		key string
		raw []byte
	}
	values, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (checkpointValue, error) {
		var v checkpointValue
		err := row.Scan(&v.key, &v.raw)
		return v, err
	})
	if err != nil {
		return r, 0, err
	}
	// Checkpoints are disposable caches. Reads replay the authoritative journal
	// if decoding fails, including after a rollback to an older snapshot reader.
	// Appends require verified checkpoints and wait for the audit's cursor reset.
	err = func() error {
		var selected outsidePushProgress
		var selectedKey string
		for _, v := range values {
			var progress outsidePushProgress
			if err := json.Unmarshal(v.raw, &progress); err != nil {
				return fmt.Errorf("%s: %w", v.key, err)
			}
			if progress.ScannedThrough < 0 {
				return fmt.Errorf("%s: negative journal checkpoint", v.key)
			}
			if (through == 0 || progress.ScannedThrough <= through) && progress.ScannedThrough >= watermark && len(progress.State) > 0 {
				watermark, selected, selectedKey = progress.ScannedThrough, progress, v.key
			}
		}
		if len(selected.State) > 0 {
			if err := r.LoadSnapshot(selected.State, uint64(watermark)); err != nil {
				return fmt.Errorf("%s: %w", selectedKey, err)
			}
		}
		return nil
	}()
	if err != nil {
		if strictCheckpoint {
			return r, 0, err
		}
		slog.WarnContext(ctx, "journal checkpoint unusable; replaying from seed", "queue_id", q, "reason", err)
		r, watermark = sharedReader(ctx, db, q, seed), 0
	}
	for {
		rows, err := db.Query(ctx, `SELECT id, payload FROM events WHERE queue_id=$1 AND id>$2 AND ($3::bigint=0 OR id<=$3) ORDER BY id LIMIT 512`, q, watermark, through)
		if err != nil {
			return r, 0, err
		}
		records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (record, error) {
			var v record
			err := row.Scan(&v.id, &v.raw)
			return v, err
		})
		if err != nil {
			return r, 0, err
		}
		for _, v := range records {
			if _, err := r.Apply(uint64(v.id), v.raw); err != nil {
				return r, 0, err
			}
			watermark = v.id
		}
		if len(records) < 512 {
			return r, watermark, nil
		}
	}
}

const sideCheckpointKey = "side-append/checkpoint-v1"
