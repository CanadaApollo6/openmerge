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

// Rebuild earlier audit-only cursors with attempt-scoped suppression. Existing
// receipts retain their BaseMoved dedupe keys; missing real moves can be added.
const outsidePushCursorKey = "outside-push-audit/v2"
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
	Own       bool
}

type outsideLanding struct {
	AttemptID, SHA, BaseSHA string
	Consumed                bool
}

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
	Moves []OutsidePushMove

	// CursorResetReason reports an unusable derived cursor rebuilt from the
	// journal. Its original bytes remain the compare-and-swap expectation.
	CursorResetReason string
	expected          []byte
	progress          outsidePushProgress
}

// ReadOutsidePushAudit scans the suffix after a usable durable cursor, or
// rebuilds an unusable cursor from the journal in bounded passes. The last settled
// landing travels with each candidate: an old landing at the same SHA cannot hide
// a later rewind to that SHA.
func (s *Store) ReadOutsidePushAudit(ctx context.Context, q QueueID) (*OutsidePushAudit, error) {
	a := &OutsidePushAudit{}
	var err error
	a.expected, err = s.Observation(ctx, q, outsidePushCursorKey)
	if err != nil {
		return nil, err
	}
	var state queue.State
	reader := sharedReader(ctx, s.pool, q, state)
	if len(a.expected) != 0 {
		err = json.Unmarshal(a.expected, &a.progress)
		if err == nil {
			reader = sharedReader(ctx, s.pool, q, queue.State{})
			err = reader.LoadSnapshot(a.progress.State, uint64(a.progress.ScannedThrough))
			state = reader.Queue
		}
		if err != nil {
			a.CursorResetReason = err.Error()
			a.progress = outsidePushProgress{}
			state = queue.State{}
			reader = sharedReader(ctx, s.pool, q, state)
		}
	}
	if len(a.expected) == 0 || a.CursorResetReason != "" {
		// This observation is only a cache. Rebuild in the same bounded passes
		// as a new audit; immutable BaseMoved dedupe keys preserve receipts.
		err = s.pool.QueryRow(ctx, `SELECT base, COALESCE(initial_fence, '') FROM queues WHERE id=$1`, q).Scan(&state.BaseBranch, &state.Fence)
	}
	if err != nil {
		return nil, err
	}
	// Legacy-cursor prefix invariant: a legacy snapshot means no side evidence
	// exists through its watermark. Writers MUST upgrade this queue's cursor
	// in or before the first side-evidence append transaction. No prefix probe
	// or rebuild is needed; a side event in this suffix upgrades the snapshot.
	reader.Queue = state
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
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		before := state.Active
		decoded, err := reader.Apply(uint64(r.ID), r.Payload)
		if err != nil {
			return nil, err
		}
		r.Event = decoded.Queue
		next := reader.Queue
		switch e := r.Event.(type) {
		case queue.BaseMoved:
			move := pendingOutsideMove{OutsidePushMove: OutsidePushMove{
				ID: r.ID, PreviousSHA: a.progress.ObservedBase, NewSHA: e.SHA, At: r.RecordedAt,
			}}
			if before != nil && before.Phase == queue.Landing {
				move.AttemptID = before.LandingAttempt.ID
			}
			if move.AttemptID == "" {
				classifyOutsideMove(&move, &a.progress.LastLanding)
			}
			a.progress.Pending = append(a.progress.Pending, move)
			a.progress.ObservedBase = e.SHA
		case queue.Landed:
			if before != nil && before.Phase == queue.Landing && before.ID == e.BatchID && next.Fence == e.NewFence && next.BaseSHA == e.NewBaseSHA && (next.Active == nil || next.Active.ID != before.ID) && (before.Method != queue.FastForward || e.NewBaseSHA == before.LandingAttempt.FastForwardHeadSHA) {
				// The successful journal settlement supplies the produced SHA. For
				// fast-forward this is the verified contributor head, not its marker.
				landing := outsideLanding{AttemptID: before.LandingAttempt.ID, SHA: e.NewBaseSHA, BaseSHA: before.BaseSHA}
				landing = a.settled(before.LandingAttempt.ID, landing)
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
		if !move.Own && move.PreviousSHA != "" && move.PreviousSHA != move.NewSHA {
			a.Moves = append(a.Moves, move.OutsidePushMove)
		}
		a.progress.AuditedBaseMove = move.ID
		a.progress.Pending = a.progress.Pending[1:]
	}
	a.progress.State, err = reader.CursorSnapshot()
	return a, err
}

func (a *OutsidePushAudit) settled(attempt string, landing outsideLanding) outsideLanding {
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
		if landing.SHA == "" {
			classifyOutsideMove(m, &a.progress.LastLanding)
		} else {
			classifyOutsideMove(m, &landing)
		}
		first = false
	}
	// Only synthesize the landing baseline when no BaseMoved observed the
	// attempt. An explicit final build-base observation must survive settlement
	// so the next return cannot become an invisible landed-to-landed no-op.
	if first && landing.SHA != "" && a.progress.ObservedBase == landing.BaseSHA {
		a.progress.ObservedBase = landing.SHA
	}
	return landing
}

// Each settled attempt explains at most one base-to-landing observation. A
// flagged landing-to-build-base rewind preserves that allowance for the #62
// stale-read repair. Any other movement invalidates it, including an outside
// advance: returning from that outside tip to the landed SHA is a real move.
func classifyOutsideMove(m *pendingOutsideMove, landing *outsideLanding) {
	if landing.AttemptID == "" {
		return
	}
	m.PossibleStaleObservation = m.PreviousSHA == landing.SHA && m.NewSHA == landing.BaseSHA
	if m.PreviousSHA == m.NewSHA || m.PossibleStaleObservation {
		return
	}
	m.Own = !landing.Consumed && m.PreviousSHA == landing.BaseSHA && m.NewSHA == landing.SHA
	landing.Consumed = true
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
