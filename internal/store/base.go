package store

import (
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// sameBaseBasis compares only facts needed to classify a git observation.
// PR/check/config observations do not make an otherwise sound observation stale.
func sameBaseBasis(a, b queue.State) bool {
	if a.BaseSHA != b.BaseSHA || a.Fence != b.Fence {
		return false
	}
	if a.Active == nil || b.Active == nil {
		return a.Active == nil && b.Active == nil
	}
	return a.Active.ID == b.Active.ID && a.Active.Phase == b.Active.Phase && a.Active.LandingAttempt == b.Active.LandingAttempt && a.Active.LandingCommit() == b.Active.LandingCommit()
}

// baseMoveBasis reconstructs the durable state immediately before this BaseMoved.
// It is read under the queue lock, after ordinary reconciliation has committed.
func baseMoveBasis(ctx context.Context, tx pgx.Tx, q QueueID, moveID int64) (queue.State, string, error) {
	var state queue.State
	if err := tx.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id=$1`, q).Scan(&state.BaseBranch, &state.Fence); err != nil {
		return state, "", err
	}
	rows, err := tx.Query(ctx, `SELECT id, payload FROM events WHERE queue_id=$1 AND id <= $2 ORDER BY id`, q, moveID)
	if err != nil {
		return state, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return state, "", err
		}
		event, err := queue.DecodeEvent(payload)
		if err != nil {
			return state, "", err
		}
		if id == moveID {
			if moved, ok := event.(queue.BaseMoved); ok {
				return state, moved.SHA, nil
			}
			break
		}
		state = queue.Apply(state, event)
	}
	if err := rows.Err(); err != nil {
		return state, "", err
	}
	return state, "", fmt.Errorf("base move %d not found", moveID)
}

// RecordBaseMove appends only an audit, never BaseMoved. The caller has already
// committed BaseMoved through ordinary Append. Validate both its durable basis
// and today's state under the lock; a peer landing skips the audit, not reconcile.
func (s *Store) RecordBaseMove(ctx context.Context, q QueueID, basis queue.State, moveID int64, outside queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		before, tip, err := baseMoveBasis(ctx, tx, q, moveID)
		if err != nil {
			return err
		}
		current, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		if !sameBaseBasis(before, basis) {
			// Main may append a pre-peer git tip after that peer has settled. Keep
			// main's BaseMoved, but remember that its inverse is also audit-ineligible:
			// observing the peer's landed tip again is repair, not an outside push.
			_, err := tx.Exec(ctx, `INSERT INTO observations(queue_id, key, value) VALUES ($1,$2,$3)
    ON CONFLICT (queue_id, key) DO NOTHING`, q, fmt.Sprintf("outside-stale-base/%d", moveID), []byte(before.BaseSHA))
			return err
		}
		if !sameBaseBasis(current, queue.Apply(basis, queue.BaseMoved{SHA: tip})) || basis.Active != nil && basis.Active.Phase == queue.Landing || basis.BaseSHA == "" || basis.BaseSHA == tip {
			return nil
		}
		var repairing bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observations WHERE queue_id=$1
            AND key='outside-stale-base/' || (SELECT id::text FROM events WHERE queue_id=$1 AND type='base_moved' AND id < $2 ORDER BY events.id DESC LIMIT 1)
            AND value=$3)`, q, moveID, []byte(tip)).Scan(&repairing); err != nil {
			return err
		}
		if repairing {
			return nil
		}
		if outside.PreviousSHA != basis.BaseSHA || outside.NewSHA != tip {
			return fmt.Errorf("outside push does not match base move")
		}
		return insertOutsidePush(ctx, tx, q, fmt.Sprintf("outside-base/%d", moveID), outside)
	})
}

// LandingRewriteTip returns the last audited tip and its receipt ID. Read both
// before git so a peer audit cannot supply a newer basis for an older ref read.
func (s *Store) LandingRewriteTip(ctx context.Context, q QueueID, attemptID string) (string, int64, error) {
	return landingRewriteTip(ctx, s.pool, q, attemptID)
}

func landingRewriteTip(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, q QueueID, attemptID string) (string, int64, error) {
	var tip string
	var receiptID int64
	err := db.QueryRow(ctx, `SELECT id, payload->'data'->>'new_sha' FROM events WHERE queue_id=$1 AND starts_with(dedupe_key, $2) ORDER BY id DESC LIMIT 1`, q, "outside-landing-rewrite/"+attemptID+"/").Scan(&receiptID, &tip)
	if err == pgx.ErrNoRows {
		return "", 0, nil
	}
	return tip, receiptID, err
}

// RecordLandingRewrite retains the safety block and unsettled attempt. Only the
// observation basis is revalidated; unrelated journal traffic is irrelevant.
// A receipt per previous/new tip and preceding receipt deduplicates repeated
// polls and peers, while retaining repeated transitions after an away/back cycle.
func (s *Store) RecordLandingRewrite(ctx context.Context, q QueueID, basis queue.State, previousReceipt int64, outside queue.OutsidePushRecorded) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		current, _, err := load(ctx, tx, q)
		if err != nil {
			return err
		}
		b := basis.Active
		if b == nil || b.Phase != queue.Landing || !sameBaseBasis(current, basis) {
			return nil
		}
		previous, receiptID, err := landingRewriteTip(ctx, tx, q, b.LandingAttempt.ID)
		if err != nil {
			return err
		}
		if previous == "" {
			previous = b.LandingCommit()
		}
		if outside.PreviousSHA != previous || receiptID != previousReceipt {
			return nil
		}
		return insertOutsidePush(ctx, tx, q, "outside-landing-rewrite/"+b.LandingAttempt.ID+"/"+outside.PreviousSHA+"/"+outside.NewSHA+fmt.Sprintf("/%d", receiptID), outside)
	})
}

// AbandonedBaseMove identifies one individually observed transition during an
// attempt later abandoned through the normal claim-bound settlement path.
type AbandonedBaseMove struct {
	ID                  int64
	AttemptID           string
	PreviousSHA, NewSHA string
}

// AbandonedBaseMoves derives audits from immutable history, including away/back
// observations that cancel out in the state projection. No git reads or audit
// writes participate in abandonment. A restart can retry every receipt safely.
func (s *Store) AbandonedBaseMoves(ctx context.Context, q QueueID) ([]AbandonedBaseMove, error) {
	records, err := s.EventsAfter(ctx, q, 0)
	if err != nil {
		return nil, err
	}
	return abandonedBaseMoves(records), nil
}

func abandonedBaseMoves(records []EventRecord) []AbandonedBaseMove {
	var state queue.State
	var pending, result []AbandonedBaseMove
	previous := ""
	for _, record := range records {
		b := state.Active
		switch event := record.Event.(type) {
		case queue.LandingStarted:
			pending = nil
			previous = state.BaseSHA
		case queue.BaseMoved:
			if b != nil && b.Phase == queue.Landing && event.SHA != previous {
				pending = append(pending, AbandonedBaseMove{ID: record.ID, AttemptID: b.LandingAttempt.ID, PreviousSHA: previous, NewSHA: event.SHA})
				previous = event.SHA
			}
		case queue.LandingAbandoned:
			if b != nil && b.Phase == queue.Landing && b.ID == event.BatchID {
				result = append(result, pending...)
			}
			pending = nil
		case queue.Landed:
			pending = nil
		}
		state = queue.Apply(state, record.Event)
	}
	return result
}

// RecordAbandonedBaseMove records an immutable transition after settlement. It
// cannot emit abandonment or modify the base/fence, even if audit evidence fails.
func (s *Store) RecordAbandonedBaseMove(ctx context.Context, q QueueID, move AbandonedBaseMove, outside queue.OutsidePushRecorded) error {
	moves, err := s.AbandonedBaseMoves(ctx, q)
	if err != nil {
		return err
	}
	found := false
	for _, candidate := range moves {
		if candidate == move {
			found = true
			break
		}
	}
	if !found || outside.PreviousSHA != move.PreviousSHA || outside.NewSHA != move.NewSHA {
		return fmt.Errorf("missing durable abandoned base transition")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		return insertOutsidePush(ctx, tx, q, fmt.Sprintf("outside-base/%d", move.ID), outside)
	})
}

func insertOutsidePush(ctx context.Context, tx pgx.Tx, q QueueID, key string, outside queue.OutsidePushRecorded) error {
	name, payload, err := queue.EncodeEvent(outside)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload, dedupe_key) VALUES ($1,$2,$3::jsonb,$4)
 ON CONFLICT (queue_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`, q, name, string(payload), key)
	return err
}

// OutsidePushRecorded checks a durable transition receipt without decoding the
// journal. It is an optimization only; insertion still deduplicates atomically.
func (s *Store) OutsidePushRecorded(ctx context.Context, q QueueID, moveID int64) (bool, error) {
	var seen bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE queue_id=$1 AND dedupe_key=$2)`, q, fmt.Sprintf("outside-base/%d", moveID)).Scan(&seen)
	return seen, err
}
