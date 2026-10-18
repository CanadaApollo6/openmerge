package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

const outsidePushCursorKey = "outside-push-audit/v1"
const outsidePushScanLimit = 256

// OutsidePushMove identifies a transition by its immutable BaseMoved row.
type OutsidePushMove struct {
	ID                       int64
	PreviousSHA, NewSHA      string
	PossibleStaleObservation bool
	At                       time.Time
}

type pendingOutsideMove struct {
	OutsidePushMove
	AttemptID string
	OwnSHA    string
}

type outsideLanding struct{ SHA, BaseSHA string }

// The scanned position and fold survive restart even when a landing holds the
// audited position back. Pending transitions never require replaying a prefix.
type outsidePushProgress struct {
	ScannedThrough, AuditedBaseMove int64
	State                           json.RawMessage
	ObservedBase                    string
	LastLanding                     outsideLanding
	Pending                         []pendingOutsideMove
}

// OutsidePushAudit is a read-only journal derivation, followed by an atomic
// cursor/event commit. No transaction or queue lock survives this read.
type OutsidePushAudit struct {
	Moves    []OutsidePushMove
	expected []byte
	progress outsidePushProgress
}

// ReadOutsidePushAudit scans only the suffix after the durable cursor. The last
// settled landing travels with each candidate: an old landing at the same SHA
// cannot hide a later rewind to that SHA.
func (s *Store) ReadOutsidePushAudit(ctx context.Context, q QueueID) (*OutsidePushAudit, error) {
	a := &OutsidePushAudit{}
	var err error
	a.expected, err = s.Observation(ctx, q, outsidePushCursorKey)
	if err != nil {
		return nil, err
	}
	var state queue.State
	if len(a.expected) != 0 {
		if err := json.Unmarshal(a.expected, &a.progress); err != nil {
			return nil, err
		}
		state, err = queue.DecodeSnapshot(a.progress.State)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id=$1`, q).Scan(&state.BaseBranch, &state.Fence)
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, recorded_at, payload FROM events
 WHERE queue_id=$1 AND id > $2 ORDER BY id LIMIT $3`, q, a.progress.ScannedThrough, outsidePushScanLimit)
	if err != nil {
		return nil, err
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (JournalEvent, error) {
		var r JournalEvent
		if err := row.Scan(&r.ID, &r.RecordedAt, &r.Payload); err != nil {
			return r, err
		}
		r.Event, err = queue.DecodeEvent(r.Payload)
		return r, err
	})
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		before := state.Active
		next := queue.Apply(state, r.Event)
		switch e := r.Event.(type) {
		case queue.BaseMoved:
			move := pendingOutsideMove{OutsidePushMove: OutsidePushMove{
				ID: r.ID, PreviousSHA: a.progress.ObservedBase, NewSHA: e.SHA, At: r.RecordedAt,
			}, OwnSHA: a.progress.LastLanding.SHA}
			if before != nil && before.Phase == queue.Landing {
				move.AttemptID = before.LandingAttempt.ID
			}
			last := a.progress.LastLanding
			move.PossibleStaleObservation = last.SHA != "" && move.PreviousSHA == last.SHA && move.NewSHA == last.BaseSHA
			a.progress.Pending = append(a.progress.Pending, move)
			a.progress.ObservedBase = e.SHA
		case queue.Landed:
			if before != nil && before.Phase == queue.Landing && before.ID == e.BatchID && next.Fence == e.NewFence && next.BaseSHA == e.NewBaseSHA && (next.Active == nil || next.Active.ID != before.ID) && (before.Method != queue.FastForward || e.NewBaseSHA == before.LandingAttempt.FastForwardHeadSHA) {
				// The successful journal settlement supplies the produced SHA. For
				// fast-forward this is the verified contributor head, not its marker.
				landing := outsideLanding{SHA: e.NewBaseSHA, BaseSHA: before.BaseSHA}
				a.settled(before.LandingAttempt.ID, landing)
				if a.progress.ObservedBase == before.BaseSHA || a.progress.ObservedBase == landing.SHA {
					a.progress.ObservedBase = landing.SHA
				}
				a.progress.LastLanding = landing
			}
		case queue.LandingAbandoned:
			if before != nil && before.Phase == queue.Landing && before.ID == e.BatchID && next.Fence == e.NewFence {
				a.settled(before.LandingAttempt.ID, outsideLanding{})
			}
		}
		state = next
		a.progress.ScannedThrough = r.ID
	}
	for len(a.progress.Pending) > 0 {
		move := a.progress.Pending[0]
		if move.AttemptID != "" {
			break
		}
		own := move.NewSHA == move.OwnSHA && !move.PossibleStaleObservation
		if !own && move.PreviousSHA != "" && move.PreviousSHA != move.NewSHA {
			a.Moves = append(a.Moves, move.OutsidePushMove)
		}
		a.progress.AuditedBaseMove = move.ID
		a.progress.Pending = a.progress.Pending[1:]
	}
	a.progress.State, err = queue.EncodeSnapshot(state)
	return a, err
}

func (a *OutsidePushAudit) settled(attempt string, landing outsideLanding) {
	first := true
	for i := range a.progress.Pending {
		m := &a.progress.Pending[i]
		if m.AttemptID != attempt {
			continue
		}
		m.AttemptID = ""
		// When the first observed tip already contains a successful landing,
		// its outside portion starts at that landing, not the old build base.
		if first && landing.SHA != "" && m.NewSHA != landing.SHA {
			m.PreviousSHA = landing.SHA
		}
		if landing.SHA != "" {
			m.OwnSHA = landing.SHA
			m.PossibleStaleObservation = m.PreviousSHA == landing.SHA && m.NewSHA == landing.BaseSHA
		}
		first = false
	}
}

// CommitOutsidePushAudit atomically appends inert events and advances audit-only
// progress. A competing cursor returns false: the caller retries the durable
// suffix, so a newer transition cannot disappear behind an older receipt.
func (s *Store) CommitOutsidePushAudit(ctx context.Context, q QueueID, a *OutsidePushAudit, events []queue.OutsidePushRecorded) (bool, error) {
	if len(events) != len(a.Moves) {
		return false, fmt.Errorf("outside push audit evidence count mismatch")
	}
	for i, event := range events {
		move := a.Moves[i]
		if event.PreviousSHA != move.PreviousSHA || event.NewSHA != move.NewSHA || event.PossibleStaleObservation != move.PossibleStaleObservation || !event.At.Equal(move.At) {
			return false, fmt.Errorf("outside push evidence does not match durable transition %d", move.ID)
		}
	}
	progress, err := json.Marshal(a.progress)
	if err != nil || bytes.Equal(progress, a.expected) {
		return err == nil, err
	}
	committed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		var current []byte
		err := tx.QueryRow(ctx, `SELECT value FROM observations WHERE queue_id=$1 AND key=$2`, q, outsidePushCursorKey).Scan(&current)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if !bytes.Equal(current, a.expected) {
			return nil
		}
		for i, event := range events {
			name, payload, err := queue.EncodeEvent(event)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO events(queue_id, type, payload, dedupe_key) VALUES ($1,$2,$3::jsonb,$4)
 ON CONFLICT (queue_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`, q, name, string(payload), fmt.Sprintf("outside-base/%d", a.Moves[i].ID)); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO observations(queue_id, key, value) VALUES ($1,$2,$3)
 ON CONFLICT (queue_id, key) DO UPDATE SET value=EXCLUDED.value`, q, outsidePushCursorKey, progress)
		committed = err == nil
		return err
	})
	return committed, err
}
