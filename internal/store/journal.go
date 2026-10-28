package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// DependencyStatus is an audience-safe read projection installed only by the
// private dependency activation seam. It never contains raw source evidence.
type DependencyDiagnostic struct {
	Code   string
	Source string
}

type DependencyStatus struct {
	Diagnostics []DependencyDiagnostic
	Text        string
	Details     []string
	URL         string
	Redacted    bool
	Blocking    bool
}

// Journal is a queue's immutable seed and committed events at one DB snapshot.
type Journal struct {
	DependencyQueue *queue.State
 DependencyRedacted bool
	DependencyStatus   map[int]DependencyStatus
	Key                QueueKey
	Seed               queue.State
	Events             []JournalEvent
	OutsideWarnings    OutsideWarningSummary
	HasAuthorityPin    bool

	ProvenanceCapacity *ProvenanceCapacity
}

// JournalEvent retains the persisted wire envelope for explainability clients.
type JournalEvent struct {
	ID         int64
	RecordedAt time.Time
	Payload    json.RawMessage
	Event      queue.Event // nil for side evidence; Payload retains its envelope
	DedupeKey  *string
}

// Journals reads only explicitly served targets, without locks or writes. All
// targets and their event histories share a repeatable-read snapshot, so current
// state and explanations cannot disagree because of a concurrent append.
func (s *Store) Journals(ctx context.Context, keys []QueueKey) ([]Journal, error) {
	return s.journals(ctx, keys, true)
}

// ReportingJournals omits warning I/O for worker-owned check rendering. The
// independent audit loop owns warning refresh; checks need only the CI ledger.
func (s *Store) ReportingJournals(ctx context.Context, keys []QueueKey) ([]Journal, error) {
	return s.journals(ctx, keys, false)
}

func (s *Store) journals(ctx context.Context, keys []QueueKey, warnings bool) ([]Journal, error) {
	result := make([]Journal, 0, len(keys))
	ids := make([]QueueID, 0, len(keys))
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		for _, key := range keys {
			journal := Journal{Key: key}
			var id QueueID
			err := tx.QueryRow(ctx, `SELECT id, base, COALESCE(initial_fence, '') FROM queues
				WHERE installation = $1 AND repository = $2 AND base = $3 AND lane = $4`,
				key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&id, &journal.Seed.BaseBranch, &journal.Seed.Fence)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id, recorded_at, payload, dedupe_key FROM events WHERE queue_id = $1 ORDER BY id`, id)
			if err != nil {
				return err
			}
			journal.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (JournalEvent, error) {
				var e JournalEvent
				if err := row.Scan(&e.ID, &e.RecordedAt, &e.Payload, &e.DedupeKey); err != nil {
					return e, err
				}
				return e, nil
			})
			if err != nil {
				return err
			}
			reader := sharedReader(ctx, tx, id, journal.Seed)
			for i := range journal.Events {
				event := &journal.Events[i]
				decoded, err := reader.Apply(uint64(event.ID), event.Payload)
				if err != nil {
					return err
				}
				event.Event = decoded.Queue
				if _, ok := event.Event.(queue.PolicyAuthorityApplied); ok {
					journal.HasAuthorityPin = true
				}
			}
			gauge := reader.Provenance.Capacity(s.sideHighWater)
			if gauge.FenceCount != 0 || len(reader.Provenance.Records()) != 0 {
				journal.ProvenanceCapacity = &gauge
			}
			ids = append(ids, id)
			result = append(result, journal)
		}
		return nil
	})
	if err == nil && warnings {
		// Fold immutable journal data after COMMIT: a warning deadline must never
		// poison the DB transaction or fail sibling queues in the response.
		for i := range result {
			if result[i].HasAuthorityPin {
				result[i].OutsideWarnings = s.journalWarnings(ctx, ids[i], result[i].Events)
			}
		}
	}
	return result, err
}
