package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// CheckAuthorityDowngrade refuses a binary-only downgrade across the ADR 0008
// boundary even after rollback/retirement, and even when v7 exists only in a
// cursor. This does not certify compatibility with other binary differences.
// Restore/reconciliation is deliberately not a bypass: run this on the restored
// database with workers stopped, retaining the source history separately.
func (r *JournalReader) CheckAuthorityDowngrade(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return pgx.BeginTxFunc(ctx, r.store.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload FROM events ORDER BY id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if _, err := queue.DecodeEvent(raw); err != nil {
				rows.Close()
				return errors.New("journal compatibility unknown")
			}
			var envelope struct {
				Type string                     `json:"type"`
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				rows.Close()
				return errors.New("journal compatibility unknown")
			}
			incompatible := false
			switch envelope.Type {
			case "policy_authority_applied", "write_restriction_observed", "write_restriction_verified", "outside_push_acknowledged":
				incompatible = true
			}
			for _, field := range []string{"source", "origin_authority"} {
				var value string
				_ = json.Unmarshal(envelope.Data[field], &value)
				if value == "write_restriction" || value == "openmerge" {
					incompatible = true
				}
			}
			if incompatible {
				rows.Close()
				return errors.New("authority journal boundary present; use a compatible reader or restore and reconcile the pre-emission backup")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT value FROM observations WHERE key LIKE $1`, "outside-push-audit/%")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var cursor struct{ State json.RawMessage }
			if json.Unmarshal(raw, &cursor) != nil || len(cursor.State) == 0 {
				return errors.New("cursor compatibility unknown")
			}
			var snapshot struct{ Version int }
			if json.Unmarshal(cursor.State, &snapshot) != nil || snapshot.Version > 6 {
				return errors.New("v7 cursor boundary present; use a compatible reader or restore and reconcile the pre-emission backup")
			}
			if _, err := queue.DecodeSnapshot(cursor.State); err != nil {
				return errors.New("cursor compatibility unknown")
			}
		}
		return rows.Err()
	})
}
