package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// authorityDowngradeEventTypes lists every event added since the pre-ADR-0008
// wire baseline addec7d^. TestAuthorityDowngradeEventCoverage guards this boundary.
var authorityDowngradeEventTypes = []string{
	"policy_authority_applied", "write_restriction_observed", "write_restriction_verified",
	"outside_push_acknowledged", "landing_checks_recorded",
}

// ErrAuthorityDowngradeRefused marks a decided refusal: retained journal or
// cursor data crosses (or cannot be shown not to cross) the ADR 0008 boundary.
// Other errors mean the check itself could not run and decide nothing.
var ErrAuthorityDowngradeRefused = errors.New("authority downgrade refused")

func downgradeRefused(reason string) error {
	return fmt.Errorf("%w: %s", ErrAuthorityDowngradeRefused, reason)
}

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
				return downgradeRefused("journal compatibility unknown")
			}
			var envelope struct {
				Type string                     `json:"type"`
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				rows.Close()
				return downgradeRefused("journal compatibility unknown")
			}
			incompatible := slices.Contains(authorityDowngradeEventTypes, envelope.Type)
			var source string
			_ = json.Unmarshal(envelope.Data["source"], &source)
			incompatible = incompatible || source == "write_restriction"
			if incompatible {
				rows.Close()
				return downgradeRefused("authority journal boundary present; use a compatible reader or restore and reconcile the pre-emission backup")
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
				return downgradeRefused("cursor compatibility unknown")
			}
			var snapshot struct{ Version int }
			if json.Unmarshal(cursor.State, &snapshot) != nil || snapshot.Version > 6 {
				return downgradeRefused("v7 cursor boundary present; use a compatible reader or restore and reconcile the pre-emission backup")
			}
			if _, err := queue.DecodeSnapshot(cursor.State); err != nil {
				return downgradeRefused("cursor compatibility unknown")
			}
		}
		return rows.Err()
	})
}
