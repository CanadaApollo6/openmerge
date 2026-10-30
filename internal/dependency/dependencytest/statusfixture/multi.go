package statusfixture

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	return multiJournal(t, code, phase, depth, cobatch, 100, false)
}

// multiJournal numbers tested commits from tested, so a later rebuilt
// composition can reuse the canonical commits of an equivalent public history.
// optIn governs the queue with the single-maintainer opt-in and two writers.
func multiJournal(t testing.TB, code, phase string, depth int, cobatch bool, tested int, optIn bool) (store.Journal, dependency.JournalInitialized, string, dependency.Grant, []int) {
	t.Helper()
	initial := code
	if phase != "queued" {
		initial = "dependency-landed"
	}
	j, binding, ref, grant := StatusJournal(t, initial, 1)
	// Delay B's admission so an independent change is genuinely ahead of it.
	_, j.Events[1].Payload, _ = queue.EncodeEvent(queue.RefreshRequested{})
	j.Events[1].Event = queue.RefreshRequested{}
	if optIn {
		e, err := dependency.DecodeEvent(j.Events[3].Payload)
		policy, ok := e.(dependency.PolicyApplied)
		if err != nil || !ok {
			t.Fatalf("fixture policy: %v", err)
		}
		policy.Policy.SingleMaintainer = true
		policy.Digest = dependency.PolicyDigest(policy.Policy, policy.Enabled)
		for _, writer := range []string{"11", "22"} {
			policy.Writers = append(policy.Writers, dependency.Grant{SubjectID: writer, ReturnedID: writer, RepositoryID: policy.Alias.RepositoryID, ReadID: "writers", Fresh: true, Allowed: true})
		}
		if _, j.Events[3].Payload, err = dependency.EncodeEvent(policy); err != nil {
			t.Fatal(err)
		}
	}
	b := replay(t, &j, binding)
	var observed dependency.Observed
	var observedID uint64
	for _, record := range j.Events {
		if record.Event != nil {
			continue
		}
		if o, err := dependency.DecodeEvent(record.Payload); err == nil {
			if o, ok := o.(dependency.Observed); ok {
				observed, observedID = o, uint64(record.ID)
			}
		}
	}
	at := j.Events[0].RecordedAt
	appendRecord := b.add
	prs := []int{1, 2, 3}
	if depth == 4 {
		prs = append(prs, 4, 5, 6)
	}
	for _, pr := range prs {
		if pr != 2 {
			appendRecord(queue.PRObserved{PR: pr, HeadSHA: fixtureHead(pr), Eligible: true, Trusted: true})
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
	batchIDs, compositionIDs := startBatches(b, depth, cobatch, phase == "building", 0, tested)
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
			if batch := b.reader.Queue.Batch(id); batch == nil || batch.InvalidationReason != "dependency-changed" {
				t.Fatalf("%s did not invalidate: %+v", id, batch)
			}
		}
		if phase == "discarded" {
			causes := make([]uint64, len(batchIDs))
			for i := range causes {
				causes[i] = cause
			}
			appendRecord(queue.DependencyCompositionsInvalidated{BatchIDs: batchIDs, CompositionIDs: compositionIDs, CauseEventIDs: causes, Reason: "dependency-changed"})
			if len(b.reader.Queue.Batches()) != 0 {
				t.Fatal("discard failed")
			}
		}
	}
	return j, binding, ref, grant, prs
}

func fixtureHead(pr int) string {
	if pr == 2 {
		return strings.Repeat("b", 40)
	}
	return fmt.Sprintf("%040x", pr+10)
}

// startBatches starts real compositions for B, optionally co-batched with the
// independent changes, plus three speculative suffixes at depth four. The last
// batch stays building when requested. offset continues batch numbering after
// discarded attempts; tested numbers the tested commits.
func startBatches(b *journalBuilder, depth int, cobatch, building bool, offset, tested int) ([]string, []uint64) {
	t := b.t
	t.Helper()
	j := b.j
	at := j.Events[0].RecordedAt
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
	base := b.reader.Queue.BaseSHA
	if base == "" {
		base = j.Seed.BaseSHA
	}
	for n, group := range groups {
		id := fmt.Sprintf("b%d", n+offset+1)
		pins := []queue.BatchPR{}
		members := []dependency.CompositionMember{}
		state, err := b.reader.Dependencies()
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range group {
			pins = append(pins, queue.BatchPR{PR: pr, HeadSHA: fixtureHead(pr)})
			cid, err := j.Key.ChangeID(pr)
			if err != nil {
				t.Fatal(err)
			}
			member, err := state.CompositionMember(string(cid), 1, fixtureHead(pr))
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, member)
		}
		var start uint64
		if depth == 4 {
			start = b.add(queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, ParentBatchID: parent, Position: uint64(n + 1), PRs: pins, At: at})
		} else {
			start = b.add(queue.BatchStarted{BatchID: id, BaseSHA: base, PRs: pins, At: at})
		}
		composition := b.add(dependency.CompositionRecorded{Namespace: b.binding.Namespace, BatchID: id, StartEventID: start, BaseSHA: base, Members: members})
		if b.reader.Queue.Batch(id) == nil {
			t.Fatalf("missing real batch %s", id)
		}
		batchIDs = append(batchIDs, id)
		compositionIDs = append(compositionIDs, composition)
		if !building || depth == 4 && n < len(groups)-1 {
			base = fmt.Sprintf("%040x", n+tested)
			if depth == 4 {
				b.add(queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: base, LandingSHA: base, ParentBatchID: parent, Position: uint64(n + 1), Method: queue.Merge, At: at})
			} else {
				b.add(queue.TestRefBuilt{BatchID: id, TestedSHA: base, Method: queue.Merge, At: at})
			}
		}
		expected := queue.Testing
		if building && n == len(groups)-1 {
			expected = queue.Building
		}
		if got := b.reader.Queue.Batch(id); got == nil || got.Phase != expected {
			t.Fatalf("batch %s did not reach %s: %+v", id, expected, got)
		}
		parent = id
	}
	return batchIDs, compositionIDs
}

// journalBuilder appends events only after the real reader accepts them.
// Records are stamped offset after the first record; delay, when set, is
// added to offset by each private observation.
type journalBuilder struct {
	offset  time.Duration
	delay   time.Duration
	t       testing.TB
	j       *store.Journal
	binding dependency.JournalInitialized
	grant   dependency.Grant
	reader  journalreader.Reader
	records map[uint64][]byte
}

func replay(t testing.TB, j *store.Journal, binding dependency.JournalInitialized) *journalBuilder {
	t.Helper()
	b := &journalBuilder{t: t, j: j, binding: binding, records: map[uint64][]byte{}}
	b.reader = journalreader.New(j.Seed, binding, func(id uint64) ([]byte, error) { return b.records[id], nil })
	b.reader.WithChangeIdentity(func(pr int) (queue.ChangeID, error) { return j.Key.ChangeID(pr) })
	for _, record := range j.Events {
		b.records[uint64(record.ID)] = record.Payload
		if _, err := b.reader.Apply(uint64(record.ID), record.Payload); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func (b *journalBuilder) add(event any) uint64 {
	t := b.t
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
	id := uint64(len(b.j.Events) + 1)
	b.records[id] = raw
	if _, err := b.reader.Apply(id, raw); err != nil {
		t.Fatalf("%T rejected: %v", event, err)
	}
	b.j.Events = append(b.j.Events, store.JournalEvent{ID: int64(id), RecordedAt: b.j.Events[0].RecordedAt.Add(b.offset), Payload: raw, Event: qe})
	return id
}
