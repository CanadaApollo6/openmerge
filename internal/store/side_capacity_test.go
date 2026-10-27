package store

import (
	"fmt"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
)

func TestSideBundlePacksFencesOnceUnderLock(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedSideFences(t, s, q)
	for _, n := range []int{3, 10, 30} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			parts := make([]SideAppend, n/3)
			for i := range parts {
				h := sideInventory(t)
				h.Request.ChangeID = fmt.Sprintf("chg1_%064x", 1000+n*100+i)
				h.Digest = pj.InventoryDigest(h, h.Commits)
				key := fmt.Sprintf("bundle-%d-%d", n, i)
				parts[i] = prepareSideBundle(t, s, q, sideBundle(t, h, key, key, nil))
			}
			request := SideAppend{Count: n, DedupeKey: fmt.Sprint("bundle-", n), Build: func(ids []uint64) ([]journalreader.Event, error) {
				out := make([]journalreader.Event, 0, n)
				for i, part := range parts {
					events, err := part.Build(ids[i*3 : i*3+3])
					if err != nil {
						return nil, err
					}
					out = append(out, events...)
				}
				if len(out) < n {
					extra := sideInventory(t)
					extra.Request.ChangeID = fmt.Sprintf("chg1_%064x", 10000+n)
					extra.Digest = pj.InventoryDigest(extra, extra.Commits)
					out = append(out, journalreader.Event{Provenance: extra})
				}
				return out, nil
			}}
			var work uint64
			var entries int
			calls := 0
			s.afterSidePreflight = func(w uint64, e int) { work = w; entries = e; calls++ }
			_, err := s.AppendSide(t.Context(), q, request)
			must(t, err)
			s.afterSidePreflight = nil
			if calls != 1 || work != uint64(entries) {
				t.Fatalf("%d records: %d preflights, %d entries visited, %d fences; want one pack", n, calls, work, entries)
			}
			t.Logf("%d-record bundle: 1 candidate fence pack, %d entry visits", n, work)
		})
	}
	var key QueueKey
	must(t, s.pool.QueryRow(t.Context(), `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&key.Installation, &key.Repository, &key.BaseBranch, &key.Lane))
	journals, err := s.Journals(t.Context(), []QueueKey{key})
	must(t, err)
	gauge := journals[0].ProvenanceCapacity
	if gauge == nil || gauge.FenceCount < 128 || gauge.ProjectedBytes == 0 || gauge.HighWaterBytes != gauge.LimitBytes*80/100 {
		t.Fatal("operator gauge missing", gauge)
	}
	// No writer path is involved in the status read.
	_, _, err = s.Load(t.Context(), q)
	must(t, err)
}

func seedSideFences(t *testing.T, s *Store, q QueueID) {
	t.Helper()
	for i := range 64 {
		h := sideInventory(t)
		h.Request.ChangeID = fmt.Sprintf("chg1_%064x", i)
		h.Digest = pj.InventoryDigest(h, h.Commits)
		request := prepareSideBundle(t, s, q, sideBundle(t, h, fmt.Sprint("seed-", i), fmt.Sprint("seed-", i), nil))
		_, err := s.AppendSide(t.Context(), q, request)
		must(t, err)
	}
}
