package journalreader

import (
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"testing"
	"time"
)

func TestTenThousandAbandonedAttemptsRetireDurably(t *testing.T) {
	records := map[uint64][]byte{}
	r := New(queue.State{BaseBranch: "main", BaseSHA: "base"}, dependency.JournalInitialized{}, lookup(records))
	var id, previous uint64
	add := func(e any) {
		t.Helper()
		id++
		raw := encode(t, e)
		records[id] = raw
		if _, err := r.Apply(id, raw); err != nil {
			t.Fatalf("event %d (%T): %v", id, e, err)
		}
	}
	h := provenanceFixture(t)
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	add(queue.PRObserved{PR: 1, HeadSHA: h.Request.Head, Trusted: true, Eligible: true})
	add(queue.EnqueueRequested{PR: 1, At: at})
	for i := 0; i < 10000; i++ {
		batch, attempt := fmt.Sprintf("b%d", i+1), fmt.Sprintf("attempt-%d", i+1)
		add(queue.BatchStarted{BatchID: batch, BaseSHA: r.Queue.BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.Request.Head}}, At: at})
		testedSHA := fmt.Sprintf("%040x", i+100)
		add(queue.TestRefBuilt{BatchID: batch, TestedSHA: testedSHA, Method: queue.Merge, At: at})
		h.Request.Generation = fmt.Sprint(i)
		h.PreviousObservationID = previous
		h.Digest = pj.InventoryDigest(h, h.Commits)
		add(h)
		previous = id
		d := decisionFixture(h)
		d.BatchID, d.AttemptID, d.TestedSHA = batch, attempt, testedSHA
		add(d)
		decisionID := id
		l := landingFixture(t, h)
		l.BatchID, l.AttemptID, l.DecisionID = batch, attempt, decisionID
		l.Members[0].ObservationIDs = []uint64{previous}
		l.Members[0].Mapping.Build.Result = testedSHA
		for j := range l.Members[0].Layers {
			l.Members[0].Layers[j].References = []uint64{decisionID}
		}
		l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
		add(l)
		add(queue.LandingStarted{BatchID: batch, AttemptID: attempt, FenceToken: "f", At: at})
		if r.Queue.Active == nil || r.Queue.Active.Phase != queue.Landing {
			t.Fatal("fixture never started landing")
		}
		add(queue.LandingAbandoned{BatchID: batch, NewFence: fmt.Sprint(i)})
		if r.Queue.Active != nil && r.Queue.Active.Phase == queue.Landing {
			t.Fatal("fixture failed to abandon")
		}
		if len(r.Provenance.Records()) > 1 {
			t.Fatalf("abandoned records accumulated at %d: %d", i, len(r.Provenance.Records()))
		}
		if i%1000 != 999 {
			continue
		}
		raw, err := r.CursorSnapshot()
		must(t, err)
		next := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
		must(t, next.LoadSnapshot(raw, id))
		r = next
		reused := l
		reused.Members[0].Request = h.Request
		if _, err := r.Apply(id+1, encode(t, reused)); err == nil {
			t.Fatal("retired attempt resurrected")
		}
		t.Logf("%d abandonments: %d retained records, %d checkpoint bytes", i+1, len(r.Provenance.Records()), len(raw))
	}
}

func TestTenThousandDroppedChangesRetireDurably(t *testing.T) {
	for _, kind := range []string{"closed", "dequeued", "head", "change-retired", "queue-retired"} {
		t.Run(kind, func(t *testing.T) {
			records := map[uint64][]byte{}
			r := New(queue.State{BaseBranch: "main", BaseSHA: "base"}, dependency.JournalInitialized{}, lookup(records))
			var id uint64
			add := func(e any) {
				t.Helper()
				id++
				raw := encode(t, e)
				records[id] = raw
				_, err := r.Apply(id, raw)
				must(t, err)
			}
			at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			h := provenanceFixture(t)
			for i := 1; i <= 10000; i++ {
				h.Request.ChangeID = fmt.Sprintf("chg1_%064x", i)
				h.Request.Generation = fmt.Sprint(i)
				h.Digest = pj.InventoryDigest(h, h.Commits)
				add(queue.ChangeRevisionObserved{PR: i, ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, HeadSHA: h.Request.Head, Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: at}})
				add(queue.PRObserved{PR: i, HeadSHA: h.Request.Head, Trusted: true, Eligible: true})
				add(queue.EnqueueRequested{PR: i, At: at})
				add(h)
				switch kind {
				case "closed":
					add(queue.PRClosed{PR: i})
				case "dequeued":
					entry := r.Queue.Entries[0]
					add(queue.Dequeued{PR: i, AdmissionID: entry.AdmissionID, Reason: queue.Requested})
				case "head":
					add(queue.PRObserved{PR: i, HeadSHA: "new-head"})
					if len(r.Provenance.Records()) != 0 {
						t.Fatal("head supersession retained inventory")
					}
					add(queue.PRClosed{PR: i})
				case "queue-retired":
					add(queue.LifecycleChanged{State: "retired", Detail: "removed"})
					if len(r.Provenance.Records()) != 0 {
						t.Fatal("queue retirement retained inventory")
					}
					add(queue.PRClosed{PR: i})
					add(queue.LifecycleChanged{State: "active"})
				case "change-retired":
					add(queue.ChangeRevisionObserved{PR: i, ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 2, Predecessor: 1, HeadSHA: "new-head", Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: at}})
					if len(r.Provenance.Records()) != 0 {
						t.Fatal("revision retirement retained inventory")
					}
					add(queue.PRClosed{PR: i})
				}
				if len(r.Provenance.Records()) != 0 {
					t.Fatalf("dropped change retained inventory at %d", i)
				}
				if i%1000 != 0 {
					continue
				}
				raw, err := r.CursorSnapshot()
				must(t, err)
				next := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
				must(t, next.LoadSnapshot(raw, id))
				r = next
				if _, err := r.Apply(id+1, encode(t, h)); err == nil {
					t.Fatal("retired generation resurrected")
				}
				t.Logf("%d dropped changes: 0 retained records, %d checkpoint bytes", i, len(raw))
			}
		})
	}
}

func TestSnapshotRejectsRetiredWatermarkBeyondContainingCursor(t *testing.T) {
	h := provenanceFixture(t)
	r := New(queue.State{}, dependency.JournalInitialized{}, nil)
	apply(t, &r, 3, encode(t, h))
	h.Request.Generation = "new"
	h.PreviousObservationID = 3
	h.Digest = pj.InventoryDigest(h, h.Commits)
	apply(t, &r, 5, encode(t, h))
	// Retire the active generation too, leaving no Records IDs to detect a future
	// checkpoint watermark. The containing cursor must still fence Through.
	r.Provenance = pj.RetireOwned(r.Provenance, 100, "", func(p.InventoryRequest) bool { return true })
	raw, err := r.CursorSnapshot()
	must(t, err)
	next := New(queue.State{}, dependency.JournalInitialized{}, nil)
	if err := next.LoadSnapshot(raw, 5); err == nil {
		t.Fatal("accepted future retired watermark")
	}
}

func TestStaleDequeueCannotRetireNewInventoryWithoutAdmission(t *testing.T) {
	h := provenanceFixture(t)
	r := New(queue.State{}, dependency.JournalInitialized{}, nil)
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	apply(t, &r, 1, encode(t, queue.ChangeRevisionObserved{PR: 1, ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, HeadSHA: h.Request.Head, Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: at}}))
	apply(t, &r, 2, encode(t, h))
	apply(t, &r, 3, encode(t, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.Requested}))
	if len(r.Provenance.Records()) != 1 {
		t.Fatal("no-op dequeue retired evidence")
	}
}

func TestUncertainLandingRetainsEvidenceUntilDefinitiveOutcome(t *testing.T) {
	for _, terminal := range []string{"closed", "head", "retired"} {
		t.Run(terminal, func(t *testing.T) {
			records := map[uint64][]byte{}
			r := New(queue.State{BaseBranch: "main", BaseSHA: "base"}, dependency.JournalInitialized{}, lookup(records))
			var id uint64
			add := func(e any) {
				t.Helper()
				id++
				raw := encode(t, e)
				records[id] = raw
				_, err := r.Apply(id, raw)
				must(t, err)
			}
			h := provenanceFixture(t)
			at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			add(queue.ChangeRevisionObserved{PR: 1, ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, HeadSHA: h.Request.Head, Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: at}})
			add(queue.PRObserved{PR: 1, HeadSHA: h.Request.Head, Trusted: true, Eligible: true})
			add(queue.EnqueueRequested{PR: 1, At: at})
			add(queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.Request.Head}}, At: at})
			add(queue.TestRefBuilt{BatchID: "b1", TestedSHA: h.Request.Head, Method: queue.Merge, At: at})
			add(h)
			observationID := id
			d := decisionFixture(h)
			d.BatchID = "b1"
			add(d)
			decisionID := id
			l := landingFixture(t, h)
			l.BatchID = "b1"
			l.DecisionID = decisionID
			l.Members[0].ObservationIDs = []uint64{observationID}
			for i := range l.Members[0].Layers {
				l.Members[0].Layers[i].References = []uint64{decisionID}
			}
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			add(l)
			add(queue.LandingStarted{BatchID: l.BatchID, AttemptID: l.AttemptID, FenceToken: "f", At: at})
			if r.Queue.Active == nil || r.Queue.Active.Phase != queue.Landing {
				t.Fatal("fixture did not start landing")
			}
			switch terminal {
			case "closed":
				add(queue.PRClosed{PR: 1})
			case "head":
				add(queue.PRObserved{PR: 1, HeadSHA: "new-head"})
			case "retired":
				add(queue.LifecycleChanged{State: "retired", Detail: "removed"})
			}
			if len(r.Provenance.Records()) != 2 {
				t.Fatal("unresolved landing lost evidence")
			}
			add(queue.Landed{BatchID: l.BatchID, NewBaseSHA: h.Request.Head, NewFence: "f2"})
			l.Stage = "landed"
			l.Members[0].Mapping.Build.Stage = "landed"
			l.Members[0].Mapping.Build.ID = "composition-L"
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			add(l)
			if len(r.Provenance.Records()) != 0 {
				t.Fatal("resolved landing retained records")
			}
		})
	}
}

func TestCloseRetiresPinnedAndPartialGenerationsAndTheirAttempt(t *testing.T) {
	records := map[uint64][]byte{}
	r := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
	add := func(id uint64, e any) {
		t.Helper()
		raw := encode(t, e)
		records[id] = raw
		_, err := r.Apply(id, raw)
		must(t, err)
	}
	h := provenanceFixture(t)
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	add(1, queue.ChangeRevisionObserved{PR: 1, ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, HeadSHA: h.Request.Head, Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: at}})
	add(5, h)
	add(10, decisionFixture(h))
	l := landingFixture(t, h)
	l.Members[0].ObservationIDs = []uint64{5}
	l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
	add(11, l)
	partial := h
	partial.Request.Generation = "partial"
	partial.PreviousObservationID = 5
	partial.Chunks = 2
	partial.Digest = pj.InventoryDigest(partial, partial.Commits)
	partial.Commits = partial.Commits[:1]
	add(13, partial)
	add(14, queue.PRClosed{PR: 1})
	if len(r.Provenance.Records()) != 0 {
		t.Fatal("close retained a pinned generation, partial generation or companion")
	}
	raw, err := r.CursorSnapshot()
	must(t, err)
	next := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
	must(t, next.LoadSnapshot(raw, 14))
	r = next
	fresh := h
	fresh.Request.Generation = "fresh"
	fresh.PreviousObservationID = 13
	fresh.Digest = pj.InventoryDigest(fresh, fresh.Commits)
	add(15, fresh)
	l = landingFixture(t, fresh)
	l.Members[0].ObservationIDs = []uint64{15}
	l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
	if _, err := r.Apply(16, encode(t, l)); err == nil {
		t.Fatal("dropped attempt was reused against fresh inventory")
	}
	h.PreviousObservationID = 15
	h.Digest = pj.InventoryDigest(h, h.Commits)
	if _, err := r.Apply(16, encode(t, h)); err == nil {
		t.Fatal("dropped generation reused against current predecessor")
	}
}
