package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

const warningPageSize = 256

const warningPageSQL = `SELECT id, payload, COALESCE(dedupe_key, '') FROM events
 WHERE queue_id=$1 AND id>$2 ORDER BY id LIMIT $3`

// eventPage uses events_queue_watermark, retaining at most one bounded page.
// Existing EventsAfter callers keep their original unbounded logging behavior.
func eventPage(ctx context.Context, tx pgx.Tx, q QueueID, after int64) ([]EventRecord, error) {
	rows, err := tx.Query(ctx, warningPageSQL, q, after, warningPageSize)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EventRecord, error) {
		var r EventRecord
		var raw []byte
		if err := row.Scan(&r.ID, &raw, &r.DedupeKey); err != nil {
			return r, err
		}
		r.Event, err = queue.DecodeEvent(raw)
		return r, err
	})
}

func dedupeEvent(ctx context.Context, tx pgx.Tx, q QueueID, key string) (EventRecord, error) {
	var r EventRecord
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id, payload, dedupe_key FROM events WHERE queue_id=$1 AND dedupe_key=$2`, q, key).Scan(&r.ID, &raw, &r.DedupeKey)
	if err == nil {
		r.Event, err = queue.DecodeEvent(raw)
	}
	return r, err
}

// OutsidePushWarning preserves the original evidence, including uncertainty.
// EventID is the acknowledgement target; BaseMovedEventID supplies eligibility.
// Warnings are visited in originating BaseMoved order, not audit append order.
type OutsidePushWarning struct {
	EventID, BaseMovedEventID int64
	queue.OutsidePushRecorded
}

// VisitOutsidePushWarnings streams outstanding authority warnings at one DB
// snapshot, including retired queues and pushes outside the recent projection.
// Memory is bounded by one journal page and one pin; the callback must not retain
// unbounded results. On any error callers must discard their partial output.
// The scan is O(N), with indexed dedupe probes for authority-mode base moves.
func (s *Store) VisitOutsidePushWarnings(ctx context.Context, q QueueID, visit func(OutsidePushWarning) error) error {
	if q <= 0 || visit == nil {
		return errors.New("queue and warning visitor are required")
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultJournalReadTimeout)
	defer cancel()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM queues WHERE id=$1)`, q).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("queue %d does not exist", q)
		}
		return visitOutsidePushWarnings(ctx, tx, q, visit)
	})
}

// OutsideWarningIDLimit bounds IDs retained for status and health, independently
// of the outstanding count and the ledger's journal page size.
const OutsideWarningIDLimit = 20

// OutsideWarningSummary has an unambiguous zero value: no outstanding warnings.
// A failed read returns an error rather than a partial count or event list.
type OutsideWarningSummary struct {
	Outstanding int
	EventIDs    []int64
	Truncated   bool
}

func (s *OutsideWarningSummary) add(w OutsidePushWarning) error {
	s.Outstanding++
	if len(s.EventIDs) < OutsideWarningIDLimit {
		s.EventIDs = append(s.EventIDs, w.EventID)
	} else {
		s.Truncated = true
	}
	return nil
}

// ReadOutsidePushWarnings counts the ledger with bounded retained memory. It
// takes no application or advisory locks and never reads EventsAfter.
func (s *Store) ReadOutsidePushWarnings(ctx context.Context, q QueueID) (OutsideWarningSummary, error) {
	var summary OutsideWarningSummary
	if err := s.VisitOutsidePushWarnings(ctx, q, summary.add); err != nil {
		return OutsideWarningSummary{}, err
	}
	return summary, nil
}

func visitOutsidePushWarnings(ctx context.Context, tx pgx.Tx, q QueueID, visit func(OutsidePushWarning) error) error {
	var policy queue.State
	var after int64
	for {
		page, err := eventPage(ctx, tx, q, after)
		if err != nil {
			return err
		}
		for _, r := range page {
			after = r.ID
			switch e := r.Event.(type) {
			case queue.ConfigApplied, queue.PolicyAuthorityApplied:
				// Use the same pin acceptance rules as replay, without building unrelated
				// queue projections or retaining all pending BaseMoved associations.
				policy = queue.Apply(policy, e)
			case queue.OutsidePushRecorded:
				if err := validateOutsidePushOrigin(ctx, tx, q, r, e); err != nil {
					return err
				}
			case queue.BaseMoved:
				pin := policy.AuthorityPin()
				if pin == nil || pin.Authority != authority.OpenMerge {
					continue
				}
				audit, err := dedupeEvent(ctx, tx, q, fmt.Sprintf("outside-base/%d", r.ID))
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				} // audit still awaiting settlement/enrichment
				if err != nil {
					return err
				}
				push, ok := audit.Event.(queue.OutsidePushRecorded)
				if !ok || audit.ID <= r.ID || push.NewSHA != e.SHA {
					return fmt.Errorf("invalid outside push association for base event %d", r.ID)
				}
				receipt, err := acknowledgement(ctx, tx, q, audit.ID, push.NewSHA)
				if err != nil {
					return err
				}
				if receipt == nil {
					if err := visit(OutsidePushWarning{EventID: audit.ID, BaseMovedEventID: r.ID, OutsidePushRecorded: push}); err != nil {
						return err
					}
				}
			}
		}
		if len(page) < warningPageSize {
			return ctx.Err()
		}
	}
}

// OutsidePushAcknowledgement is the original immutable receipt, including its
// journal ID. A retry returns these exact values even with a different operator.
type OutsidePushAcknowledgement struct {
	EventID int64 `json:"event_id"`
	queue.OutsidePushAcknowledged
}

func acknowledgementKey(source int64) string {
	return fmt.Sprintf("outside-push-acknowledged/%d", source)
}

func acknowledgement(ctx context.Context, tx pgx.Tx, q QueueID, source int64, sha string) (*OutsidePushAcknowledgement, error) {
	r, err := dedupeEvent(ctx, tx, q, acknowledgementKey(source))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ack, ok := r.Event.(queue.OutsidePushAcknowledged)
	if !ok || ack.SourceEventID != source || ack.SHA != sha || r.ID <= source {
		return nil, errors.New("invalid outside push acknowledgement association")
	}
	return &OutsidePushAcknowledgement{EventID: r.ID, OutsidePushAcknowledged: ack}, nil
}

// AcknowledgeOutsidePush records awareness in any mode. GitHub-mode pushes have
// no authority warning, but may still receive an explicit audit receipt. Nothing
// invokes this method automatically. No queue projection, action, ref or cursor
// is written. Validation, deduplication and insertion share the queue lock.
func (s *Store) AcknowledgeOutsidePush(ctx context.Context, q QueueID, source int64, sha, operator string) (*OutsidePushAcknowledgement, error) {
	event := queue.OutsidePushAcknowledged{SourceEventID: source, SHA: sha, Operator: operator, At: time.Now().UTC()}
	if q <= 0 {
		return nil, errors.New("queue is required")
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultJournalReadTimeout)
	defer cancel()
	var receipt *OutsidePushAcknowledgement
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockQueue(ctx, tx, q); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, source).Scan(&raw); err != nil {
			return fmt.Errorf("outside push source in queue %d: %w", q, err)
		}
		original, err := queue.DecodeEvent(raw)
		if err != nil {
			return err
		}
		push, ok := original.(queue.OutsidePushRecorded)
		if !ok || push.NewSHA != sha {
			return errors.New("source must be outside_push_recorded with the supplied new SHA")
		}
		receipt, err = acknowledgement(ctx, tx, q, source, sha)
		if err != nil || receipt != nil {
			return err
		}
		name, payload, err := queue.EncodeEvent(event)
		if err != nil {
			return err
		}
		receipt = &OutsidePushAcknowledgement{OutsidePushAcknowledged: event}
		return tx.QueryRow(ctx, `INSERT INTO events(queue_id,type,payload,dedupe_key) VALUES($1,$2,$3::jsonb,$4) RETURNING id`, q, name, string(payload), acknowledgementKey(source)).Scan(&receipt.EventID)
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// validateOutsidePushOrigin uses the primary key, retaining bounded scan memory.
func validateOutsidePushOrigin(ctx context.Context, tx pgx.Tx, q QueueID, r EventRecord, push queue.OutsidePushRecorded) error {
	key := ""
	if r.DedupeKey != nil {
		key = *r.DedupeKey
	}
	source, err := strconv.ParseInt(strings.TrimPrefix(key, "outside-base/"), 10, 64)
	anomaly := fmt.Errorf("outside push ledger anomaly: orphan outside_push_recorded event %d (dedupe key %q)", r.ID, key)
	if err != nil || source <= 0 || source >= r.ID || key != fmt.Sprintf("outside-base/%d", source) {
		return anomaly
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, source).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return anomaly
	}
	if err != nil {
		return err
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		return err
	}
	base, ok := event.(queue.BaseMoved)
	if !ok || base.SHA != push.NewSHA {
		return anomaly
	}
	return nil
}
