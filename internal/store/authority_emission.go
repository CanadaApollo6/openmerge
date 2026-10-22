package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// CheckAuthorityEmissionBackup requires the live database to match the tested
// backup before its first authority-boundary event. Later compatible emissions
// retain the original backup; they cannot rewrite that historical boundary.
func (s *Store) CheckAuthorityEmissionBackup(ctx context.Context, watermark int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var live int64
		var boundary bool
		err := tx.QueryRow(ctx, `SELECT COALESCE(max(id),0), COALESCE(bool_or(payload->>'type'=ANY($1) OR payload->'data'->>'source'='write_restriction'),false) FROM events`, authorityDowngradeEventTypes).Scan(&live, &boundary)
		if err != nil {
			return errors.New("first-emission journal inventory unavailable")
		}
		if !boundary && live != watermark {
			return errors.New("first-emission backup watermark differs from live journal; repeat drain, backup and restore test")
		}
		return nil
	})
}
