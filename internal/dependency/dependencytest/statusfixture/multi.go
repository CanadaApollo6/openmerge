package statusfixture

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// MultiJournal surrounds B with independent changes, optionally co-batches them,
// and at depth four appends three real speculative suffix compositions.
func MultiJournal(t testing.TB, code, phase string, depth int, cobatch bool) (store.Journal, dependency.JournalInitialized, string, dependency.Grant, []int) {
	t.Helper()
	initial := code
	if phase != "queued" {
		initial = "dependency-landed"
	}
	j, binding, ref, grant := StatusJournal(t, initial, 1)
	// Delay B's admission so an independent change is genuinely ahead of it.
	_, j.Events[1].Payload, _ = queue.EncodeEvent(queue.RefreshRequested{})
	j.Events[1].Event = queue.RefreshRequested{}
	records := map[uint64][]byte{}
	reader := journalreader.New(j.Seed, binding, func(id uint64) ([]byte, error) { return records[id], nil })
	reader.WithChangeIdentity(func(pr int) (queue.ChangeID, error) { return j.Key.ChangeID(pr) })
	var observed dependency.Observed
	var observedID uint64
	for _, record := range j.Events {
		records[uint64(record.ID)] = record.Payload
		d, err := reader.Apply(uint64(record.ID), record.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if o, ok := d.Dependency.(dependency.Observed); ok {
			observed, observedID = o, uint64(record.ID)
		}
	}
	at := j.Events[0].RecordedAt
	appendRecord := func(event any) uint64 {
		t.Helper()
		var raw []byte
		var qe queue.Event
		var err error
		switch e := event.(type) {
		case queue.Event:
			_, raw, err = queue.EncodeEvent(e)
			qe = e
		case dependency.Event:
			_, raw, err = dependency.EncodeEvent(e)
		default:
			t.Fatalf("unsupported %T", event)
		}
		if err != nil {
			t.Fatal(err)
		}
		id := uint64(len(j.Events) + 1)
		records[id] = raw
		if _, err := reader.Apply(id, raw); err != nil {
			t.Fatal(err)
		}
		j.Events = append(j.Events, store.JournalEvent{ID: int64(id), RecordedAt: at, Payload: raw, Event: qe})
		return id
	}
	prs := []int{1, 2, 3}
	if depth == 4 {
		prs = append(prs, 4, 5, 6)
	}
	head := func(pr int) string {
		if pr == 2 {
			return strings.Repeat("b", 40)
		}
		return fmt.Sprintf("%040x", pr+10)
	}
	for _, pr := range prs {
		if pr != 2 {
			appendRecord(queue.PRObserved{PR: pr, HeadSHA: head(pr), Eligible: true, Trusted: true})
			decl := dependencytest.Request().Consumer
			id, err := j.Key.ChangeID(pr)
			if err != nil {
				t.Fatal(err)
			}
			decl.ChangeID = string(id)
			decl.Alias.Number = uint64(pr)
			decl.Edges = []dependency.DeclaredEdge{}
			decl.Digest = dependency.DeclarationDigest(decl.Edges)
			appendRecord(decl)
		}
		appendRecord(queue.EnqueueRequested{PR: pr, At: at})
	}
	if phase == "queued" {
		return j, binding, ref, grant, prs
	}
	groups := [][]int{{2}}
	if cobatch {
		groups = [][]int{{1, 2, 3}}
	}
	if depth == 4 {
		groups = append(groups, []int{4}, []int{5}, []int{6})
	}
	var batchIDs []string
	var compositionIDs []uint64
	parent := ""
	base := j.Seed.BaseSHA
	for n, group := range groups {
		id := "b" + string(rune('1'+n))
		pins := []queue.BatchPR{}
		members := []dependency.CompositionMember{}
		state, err := reader.Dependencies()
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range group {
			pins = append(pins, queue.BatchPR{PR: pr, HeadSHA: head(pr)})
			cid, err := j.Key.ChangeID(pr)
			if err != nil {
				t.Fatal(err)
			}
			member, err := state.CompositionMember(string(cid), 1, head(pr))
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, member)
		}
		var start uint64
		if depth == 4 {
			start = appendRecord(queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, ParentBatchID: parent, Position: uint64(n + 1), PRs: pins, At: at})
		} else {
			start = appendRecord(queue.BatchStarted{BatchID: id, BaseSHA: base, PRs: pins, At: at})
		}
		composition := appendRecord(dependency.CompositionRecorded{Namespace: binding.Namespace, BatchID: id, StartEventID: start, BaseSHA: base, Members: members})
		if reader.Queue.Batch(id) == nil {
			t.Fatalf("missing real batch %s", id)
		}
		batchIDs = append(batchIDs, id)
		compositionIDs = append(compositionIDs, composition)
		if phase != "building" || depth == 4 && n < len(groups)-1 {
			base = fmt.Sprintf("%040x", n+100)
			if depth == 4 {
				appendRecord(queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: base, LandingSHA: base, ParentBatchID: parent, Position: uint64(n + 1), Method: queue.Merge, At: at})
			} else {
				appendRecord(queue.TestRefBuilt{BatchID: id, TestedSHA: base, Method: queue.Merge, At: at})
			}
		}
		expected := queue.Testing
		if phase == "building" && n == len(groups)-1 {
			expected = queue.Building
		}
		if got := reader.Queue.Batch(id); got == nil || got.Phase != expected {
			t.Fatalf("batch %s did not reach %s: %+v", id, expected, got)
		}
		parent = id
	}
	if code != "dependency-landed" || phase == "invalidating" || phase == "discarded" {
		observed.ExpectedPrior = observedID
		observed.ReadSequence++
		observed.Result = "unavailable"
		switch code {
		case "dependency-waiting":
			observed.Result = "waiting"
		case "dependency-stale":
			observed.Result = "stale"
		case "dependency-landed":
			observed.Result = "waiting"
		}
		cause := appendRecord(observed)
		diagnostic := code
		if diagnostic == "dependency-landed" {
			diagnostic = "dependency-waiting"
		}
		if code != "missing" {
			appendRecord(dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: cause, Source: dependencytest.Request().Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: observed.Declaration, Grant: grant, Observation: observed, Code: diagnostic}})
		}
		for _, id := range batchIDs {
			if b := reader.Queue.Batch(id); b == nil || b.InvalidationReason != "dependency-changed" {
				t.Fatalf("%s did not invalidate: %+v", id, b)
			}
		}
		if phase == "discarded" {
			causes := make([]uint64, len(batchIDs))
			for i := range causes {
				causes[i] = cause
			}
			appendRecord(queue.DependencyCompositionsInvalidated{BatchIDs: batchIDs, CompositionIDs: compositionIDs, CauseEventIDs: causes, Reason: "dependency-changed"})
			if len(reader.Queue.Batches()) != 0 {
				t.Fatal("discard failed")
			}
		}
	}
	return j, binding, ref, grant, prs
}
