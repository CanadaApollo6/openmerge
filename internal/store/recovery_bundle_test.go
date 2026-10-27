package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecoverLandedRejectsUnrelatedInventoryAboveHighWater(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
	l := recoveredCompanion(t, s, q)
	growth := SideAppend{Count: 192, DedupeKey: "r3-background", Build: func([]uint64) ([]journalreader.Event, error) {
		events := make([]journalreader.Event, 192)
		for i := range events {
			h := sideInventory(t)
			h.Request.ChangeID = fmt.Sprint("background-", i)
			h.Digest = pj.InventoryDigest(h, h.Commits)
			events[i].Provenance = h
		}
		return events, nil
	}}
	_, err := s.AppendSide(t.Context(), q, growth)
	must(t, err)
	s.sideHighWater = 1
	read := func() journalreader.Reader {
		r, _, e := replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
		must(t, e)
		return r
	}
	rBefore := read()
	before := rBefore.Provenance.Capacity(1)
	h := sideInventory(t)
	h.Request.ChangeID = "unrelated-smuggled"
	h.Digest = pj.InventoryDigest(h, h.Commits)
	_, err = s.AppendSide(t.Context(), q, sideObservation(h, "ordinary-new-evidence"))
	if err == nil {
		t.Fatal("ordinary append unexpectedly admitted")
	}
	t.Logf("ordinary append refused: %v", err)
	request := SideAppend{Count: 2, Build: func([]uint64) ([]journalreader.Event, error) {
		return []journalreader.Event{{Provenance: h}, {Provenance: l}}, nil
	}}
	count := eventCount(t, s, q)
	_, err = s.RecoverLanded(t.Context(), q, landedID, request)
	if err == nil || !strings.Contains(err.Error(), "not bound to historical member") {
		t.Fatal("unrelated recovery inventory admitted", err)
	}
	if eventCount(t, s, q) != count {
		t.Fatal("refused recovery wrote evidence")
	}
	afterReader := read()
	if after := afterReader.Provenance.Capacity(1); after != before || before.ProjectedBytes <= before.HighWaterBytes {
		t.Fatal("refused recovery changed capacity or fixture below high water", before, after)
	}
	// The genuine companion still drains the attempt after the failed bundle.
	_, err = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
	must(t, err)
	t.Logf("unrelated evidence refused atomically above %d-byte high water", before.HighWaterBytes)
}
