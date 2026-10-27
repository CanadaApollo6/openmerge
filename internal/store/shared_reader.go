package store

import (
	"context"

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
func replayJournal(ctx context.Context, db journalQuery, q QueueID, seed queue.State, through int64) (journalreader.Reader, int64, error) {
	r := sharedReader(ctx, db, q, seed)
	type record struct {
		id  int64
		raw []byte
	}
	var watermark int64
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
