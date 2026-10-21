package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// This index retains policy boundaries and outstanding warnings, not journal
// payloads. A late audit resolves its origin at that origin's policy boundary.
// Acknowledgements delete entries, allowing the bounded ID sample to refill.
// Each queue has its own mutex, independent of worker ownership and reporting.
type outsideWarningCache struct {
	mu          sync.Mutex
	through     int64
	policy      queue.State
	modes       []warningMode
	outstanding map[int64]OutsidePushWarning
	summary     OutsideWarningSummary
	dirty       bool
}
type warningMode struct {
	after   int64
	enabled bool
}
type warningLookup func(int64) (EventRecord, error)

func (s *Store) warningCache(q QueueID) *outsideWarningCache {
	c, _ := s.warningCaches.LoadOrStore(q, &outsideWarningCache{outstanding: make(map[int64]OutsidePushWarning)})
	return c.(*outsideWarningCache)
}

func (c *outsideWarningCache) apply(r EventRecord, lookup warningLookup) error {
	// Validate associations even when their audits have already been acknowledged.
	if strings.HasPrefix(r.DedupeKey, "outside-base/") {
		if _, ok := r.Event.(queue.OutsidePushRecorded); !ok {
			return fmt.Errorf("invalid outside push association at event %d", r.ID)
		}
	}
	if strings.HasPrefix(r.DedupeKey, "outside-push-acknowledged/") {
		if _, ok := r.Event.(queue.OutsidePushAcknowledged); !ok {
			return fmt.Errorf("invalid outside push acknowledgement association at event %d", r.ID)
		}
	}
	switch e := r.Event.(type) {
	case queue.ConfigApplied, queue.PolicyAuthorityApplied:
		c.policy = queue.Apply(c.policy, e)
		pin := c.policy.AuthorityPin()
		enabled := pin != nil && pin.Authority == authority.OpenMerge
		if len(c.modes) == 0 || c.modes[len(c.modes)-1].enabled != enabled {
			c.modes = append(c.modes, warningMode{r.ID, enabled})
		}
	case queue.OutsidePushRecorded:
		source, err := strconv.ParseInt(strings.TrimPrefix(r.DedupeKey, "outside-base/"), 10, 64)
		if err != nil || source <= 0 || source >= r.ID || r.DedupeKey != fmt.Sprintf("outside-base/%d", source) {
			return fmt.Errorf("invalid outside push origin at event %d", r.ID)
		}
		origin, err := lookup(source)
		if err != nil {
			return err
		}
		base, ok := origin.Event.(queue.BaseMoved)
		if !ok || base.SHA != e.NewSHA {
			return fmt.Errorf("invalid outside push association for base event %d", source)
		}
		boundary := sort.Search(len(c.modes), func(i int) bool { return c.modes[i].after >= source }) - 1
		if boundary >= 0 && c.modes[boundary].enabled {
			c.outstanding[r.ID] = OutsidePushWarning{EventID: r.ID, BaseMovedEventID: source}
			c.dirty = true
		}
	case queue.OutsidePushAcknowledged:
		if r.DedupeKey != acknowledgementKey(e.SourceEventID) || r.ID <= e.SourceEventID {
			return errors.New("invalid outside push acknowledgement association")
		}
		original, err := lookup(e.SourceEventID)
		if err != nil {
			return err
		}
		push, ok := original.Event.(queue.OutsidePushRecorded)
		if !ok || push.NewSHA != e.SHA {
			return errors.New("invalid outside push acknowledgement association")
		}
		if _, ok := c.outstanding[e.SourceEventID]; ok {
			delete(c.outstanding, e.SourceEventID)
			c.dirty = true
		}
	}
	c.through = r.ID
	return nil
}

func (c *outsideWarningCache) complete() OutsideWarningSummary {
	if !c.dirty {
		return cloneWarningSummary(c.summary)
	}
	// Only the smallest 20 origins are retained in the public snapshot.
	first := make([]OutsidePushWarning, 0, OutsideWarningIDLimit+1)
	for _, w := range c.outstanding {
		i := sort.Search(len(first), func(i int) bool { return first[i].BaseMovedEventID > w.BaseMovedEventID })
		if i < OutsideWarningIDLimit {
			first = slices.Insert(first, i, w)
			if len(first) > OutsideWarningIDLimit {
				first = first[:OutsideWarningIDLimit]
			}
		}
	}
	summary := OutsideWarningSummary{Outstanding: len(c.outstanding), Truncated: len(c.outstanding) > OutsideWarningIDLimit}
	for _, w := range first {
		summary.EventIDs = append(summary.EventIDs, w.EventID)
	}
	c.summary = summary
	c.dirty = false
	return cloneWarningSummary(summary)
}

// journalWarnings is the ledger API for an already decoded immutable snapshot.
// It adds zero SQL, including on first read; unchanged watermarks do no ledger
// work. Binary searches locate the suffix and exact origin/receipt targets.
func (s *Store) journalWarnings(ctx context.Context, q QueueID, events []JournalEvent) OutsideWarningSummary {
	c := s.warningCache(q)
	c.mu.Lock()
	defer c.mu.Unlock()
	watermark := int64(0)
	if len(events) > 0 {
		watermark = events[len(events)-1].ID
	}
	if c.through > watermark { // An older repeatable-read snapshot must not see future warnings.
		c = &outsideWarningCache{outstanding: make(map[int64]OutsidePushWarning)}
	}
	lookup := func(id int64) (EventRecord, error) {
		i := sort.Search(len(events), func(i int) bool { return events[i].ID >= id })
		if i == len(events) || events[i].ID != id {
			return EventRecord{}, fmt.Errorf("missing ledger association event %d", id)
		}
		e := events[i]
		return EventRecord{ID: e.ID, Event: e.Event, DedupeKey: e.DedupeKey}, nil
	}
	start := sort.Search(len(events), func(i int) bool { return events[i].ID > c.through })
	if start == len(events) {
		return cloneWarningSummary(c.summary)
	}
	for _, e := range events[start:] {
		if err := ctx.Err(); err != nil {
			return warningReadError(c.summary)
		}
		if err := c.apply(EventRecord{ID: e.ID, Event: e.Event, DedupeKey: e.DedupeKey}, lookup); err != nil {
			return warningReadError(c.summary)
		}
	}
	return c.complete()
}

func warningReadError(previous OutsideWarningSummary) OutsideWarningSummary {
	previous = cloneWarningSummary(previous)
	previous.Error = "ledger_unavailable"
	return previous
}

// cachedOutsidePushWarnings is used only by the independent audit loop and
// registration. It pages the suffix once, with indexed probes only for new
// audits/acknowledgements. Legacy workers skip this API using their projection.
func (s *Store) cachedOutsidePushWarnings(ctx context.Context, q QueueID) (OutsideWarningSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultJournalReadTimeout)
	defer cancel()
	c := s.warningCache(q)
	c.mu.Lock()
	defer c.mu.Unlock()
	var summary OutsideWarningSummary
	before := c.through
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		lookup := func(id int64) (EventRecord, error) {
			var r EventRecord
			var raw []byte
			err := tx.QueryRow(ctx, `SELECT id,payload,COALESCE(dedupe_key,'') FROM events WHERE queue_id=$1 AND id=$2`, q, id).Scan(&r.ID, &raw, &r.DedupeKey)
			if err == nil {
				r.Event, err = queue.DecodeEvent(raw)
			}
			return r, err
		}
		for {
			page, err := eventPage(ctx, tx, q, c.through)
			if err != nil {
				return err
			}
			for _, r := range page {
				if err := c.apply(r, lookup); err != nil {
					return err
				}
			}
			if len(page) < warningPageSize {
				break
			}
		}
		summary = c.summary
		if c.through != before {
			summary = c.complete()
		}
		return nil
	})
	if err != nil {
		return warningReadError(c.summary), err
	}
	return cloneWarningSummary(summary), nil
}

func cloneWarningSummary(s OutsideWarningSummary) OutsideWarningSummary {
	s.EventIDs = slices.Clone(s.EventIDs)
	return s
}
