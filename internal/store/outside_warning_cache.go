package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// This index retains policy boundaries and outstanding warnings, not journal
// payloads. A late audit resolves its origin at that origin's policy boundary.
// Acknowledgements delete entries, allowing the bounded ID sample to refill.
// Each queue has a brief fold lock; background I/O uses a private index.
// Foreground readers never wait for that lock and can use the last complete snapshot.
type outsideWarningCache struct {
	mu        sync.Mutex
	published atomic.Pointer[outsideWarningSnapshot]
	outsideWarningIndex
}

type outsideWarningSnapshot struct {
	through int64
	summary OutsideWarningSummary
}

type outsideWarningIndex struct {
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

func (s *Store) warningCache(q QueueID) *outsideWarningCache {
	c, _ := s.warningCaches.LoadOrStore(q, &outsideWarningCache{outsideWarningIndex: outsideWarningIndex{outstanding: make(map[int64]OutsidePushWarning)}})
	return c.(*outsideWarningCache)
}

func (c *outsideWarningIndex) apply(r EventRecord, lookup warningLookup) error {
	// Validate associations even when their audits have already been acknowledged.
	key := ""
	if r.DedupeKey != nil {
		key = *r.DedupeKey
	}
	if strings.HasPrefix(key, "outside-base/") {
		if _, ok := r.Event.(queue.OutsidePushRecorded); !ok {
			return fmt.Errorf("invalid outside push association at event %d", r.ID)
		}
	}
	if strings.HasPrefix(key, "outside-push-acknowledged/") {
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
		source, err := validateOutsidePushOrigin(r, e, lookup)
		if err != nil {
			return err
		}
		boundary := sort.Search(len(c.modes), func(i int) bool { return c.modes[i].after >= source }) - 1
		if boundary >= 0 && c.modes[boundary].enabled {
			c.outstanding[r.ID] = OutsidePushWarning{EventID: r.ID, BaseMovedEventID: source}
			c.dirty = true
		}
	case queue.OutsidePushAcknowledged:
		if key != acknowledgementKey(e.SourceEventID) || r.ID <= e.SourceEventID {
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

func (c *outsideWarningIndex) complete() OutsideWarningSummary {
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
	watermark := int64(0)
	if len(events) > 0 {
		watermark = events[len(events)-1].ID
	}
	if !c.mu.TryLock() {
		// An older repeatable-read snapshot must never see future warning counts.
		if snapshot := c.published.Load(); snapshot != nil && snapshot.through <= watermark {
			return warningReadError(snapshot.summary)
		}
		return warningReadError(OutsideWarningSummary{})
	}
	defer c.mu.Unlock()
	if c.through > watermark { // An older repeatable-read snapshot must not see future warnings.
		c = &outsideWarningCache{outsideWarningIndex: outsideWarningIndex{outstanding: make(map[int64]OutsidePushWarning)}}
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
		return c.complete()
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
	local := c.outsideWarningIndex
	local.modes = slices.Clone(c.modes)
	local.outstanding = maps.Clone(c.outstanding)
	c.mu.Unlock()
	var summary OutsideWarningSummary
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		lookup := func(id int64) (EventRecord, error) {
			return outsideWarningEvent(ctx, tx, q, id)
		}
		for {
			page, err := eventPage(ctx, tx, q, local.through)
			if err != nil {
				return err
			}
			for _, r := range page {
				if err := local.apply(r, lookup); err != nil {
					return err
				}
			}
			if len(page) < warningPageSize {
				break
			}
		}
		summary = local.complete()
		return nil
	})
	if err != nil {
		return warningReadError(c.lastComplete()), err
	}
	c.mu.Lock()
	// A foreground fold may have advanced while this transaction was paging.
	if local.through >= c.through {
		c.outsideWarningIndex = local
		c.published.Store(&outsideWarningSnapshot{through: local.through, summary: summary})
	} else {
		summary = c.lastComplete()
	}
	c.mu.Unlock()
	return cloneWarningSummary(summary), nil
}

func cloneWarningSummary(s OutsideWarningSummary) OutsideWarningSummary {
	s.EventIDs = slices.Clone(s.EventIDs)
	return s
}

func (c *outsideWarningCache) lastComplete() OutsideWarningSummary {
	if summary := c.published.Load(); summary != nil {
		return cloneWarningSummary(summary.summary)
	}
	return OutsideWarningSummary{}
}

func (c *outsideWarningCache) complete() OutsideWarningSummary {
	summary := c.outsideWarningIndex.complete()
	c.published.Store(&outsideWarningSnapshot{through: c.through, summary: summary})
	return cloneWarningSummary(summary)
}
