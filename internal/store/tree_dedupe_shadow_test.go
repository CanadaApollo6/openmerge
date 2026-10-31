package store

import (
	"errors"
	"testing"
	"time"
)

// TestTreeDedupeShadowSideRow proves the shadow ledger is a two-way door: it
// appends no journal event, leaves the watermark and fence alone, is visible
// only to status/report readers, and never trips the downgrade check.
func TestTreeDedupeShadowSideRow(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	before, watermark, err := s.Load(t.Context(), q)
	must(t, err)
	events := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q)

	for _, value := range []string{"one", "two"} {
		must(t, s.UpdateTreeDedupeShadow(t.Context(), q, func(current []byte) ([]byte, error) {
			if value == "two" && string(current) != "one" {
				t.Fatalf("update saw %q", current)
			}
			return []byte(value), nil
		}))
	}
	// nil keeps the row; an update error rolls back and is returned.
	must(t, s.UpdateTreeDedupeShadow(t.Context(), q, func([]byte) ([]byte, error) { return nil, nil }))
	boom := errors.New("boom")
	if err := s.UpdateTreeDedupeShadow(t.Context(), q, func([]byte) ([]byte, error) { return []byte("lost"), boom }); !errors.Is(err, boom) {
		t.Fatalf("update error = %v", err)
	}

	after, afterWatermark, err := s.Load(t.Context(), q)
	must(t, err)
	if afterWatermark != watermark || after.Fence != before.Fence || count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q) != events {
		t.Fatal("shadow write changed the journal, watermark or fence")
	}
	key := QueueKey{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main"}
	journals, err := s.Journals(t.Context(), []QueueKey{key})
	must(t, err)
	if len(journals) != 1 || string(journals[0].TreeDedupeShadow) != "two" {
		t.Fatalf("status journal shadow = %q", journals[0].TreeDedupeShadow)
	}
	reporting, err := s.ReportingJournals(t.Context(), []QueueKey{key})
	must(t, err)
	if len(reporting) != 1 || reporting[0].TreeDedupeShadow != nil {
		t.Fatal("check rendering journal read the shadow ledger")
	}
	reader := &JournalReader{store: s, timeout: time.Minute}
	must(t, reader.CheckAuthorityDowngrade(t.Context()))
	other := ensureQueue(t, s, "other")
	journals, err = s.Journals(t.Context(), []QueueKey{{Installation: key.Installation, Repository: key.Repository, BaseBranch: key.BaseBranch, Lane: "other"}})
	must(t, err)
	if other == q || len(journals) != 1 || journals[0].TreeDedupeShadow != nil {
		t.Fatal("shadow ledger leaked across queues")
	}
}
