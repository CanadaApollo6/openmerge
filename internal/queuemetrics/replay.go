// Package queuemetrics derives read-only measurements from a recorded queue
// journal. It replays events through the production queue.Apply fold and never
// writes, plans or contacts a forge. tools/dogfood (ADR 0009) and the
// `openmerge report savings` command share these primitives.
package queuemetrics

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ReadJournal opens a restricted, read-only journal reader (no migrations, no
// pings) and returns the single journal for key. The timeout bounds connection
// acquisition and the whole snapshot read.
func ReadJournal(ctx context.Context, url string, key store.QueueKey, timeout time.Duration) (store.Journal, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := store.OpenJournalReader(ctx, url, timeout)
	if err != nil {
		return store.Journal{}, err
	}
	defer r.Close()
	journals, err := r.Journals(ctx, []store.QueueKey{key})
	if err != nil {
		return store.Journal{}, err
	}
	if len(journals) != 1 {
		return store.Journal{}, fmt.Errorf("no journal for %+v; identifiers are exact and case-sensitive", key)
	}
	return journals[0], nil
}

// Entry returns a copy of the admission recorded for pr, or nil.
func Entry(s queue.State, pr int) *queue.Entry {
	for _, e := range s.Entries {
		if e.PR == pr {
			return &e
		}
	}
	return nil
}

// Admitted reports the admission newly created by an EnqueueRequested event.
// Rejected and duplicate requests leave the admission unchanged.
func Admitted(before, after queue.State, e queue.Event) (queue.Entry, bool) {
	req, ok := e.(queue.EnqueueRequested)
	if !ok {
		return queue.Entry{}, false
	}
	a, b := Entry(before, req.PR), Entry(after, req.PR)
	if b != nil && (a == nil || a.AdmissionID != b.AdmissionID) {
		return *b, true
	}
	return queue.Entry{}, false
}

// LandedBatch reports the batch a Landed event names and whether replay
// accepted it. isLanded is false for every other event type.
func LandedBatch(before, after queue.State, e queue.Event) (b *queue.Batch, accepted, isLanded bool) {
	l, ok := e.(queue.Landed)
	if !ok {
		return nil, false, false
	}
	b = before.Batch(l.BatchID)
	return b, b != nil && b.Phase == queue.Landing && after.Batch(l.BatchID) == nil, true
}

// Percentile is the nearest-rank percentile (q in (0, 1]) of values, or zero
// for no values. The input is not modified.
func Percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	values = slices.Clone(values)
	slices.Sort(values)
	return values[int(math.Ceil(float64(len(values))*q))-1]
}
