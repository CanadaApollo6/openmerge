package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type fixture struct {
	t    *testing.T
	j    store.Journal
	s    queue.State
	next int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	seed := queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	return &fixture{t: t, j: store.Journal{Key: store.QueueKey{Installation: "1", Repository: "CanadaApollo6/openmerge", BaseBranch: "main"}, Seed: seed}, s: seed}
}
func (f *fixture) emit(seconds int, e queue.Event) store.JournalEvent {
	f.t.Helper()
	_, payload, err := queue.EncodeEvent(e)
	if err != nil {
		f.t.Fatal(err)
	}
	decoded, err := queue.DecodeEvent(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	r := store.JournalEvent{ID: int64(len(f.j.Events) + 1), RecordedAt: epoch.Add(time.Duration(seconds) * time.Second), Payload: payload, Event: decoded}
	f.j.Events = append(f.j.Events, r)
	f.s = queue.Apply(f.s, decoded)
	return r
}
func (f *fixture) config(seconds, depth int, digit string) store.JournalEvent {
	f.t.Helper()
	cfg := queue.Config{SpeculationMaxDepth: depth}
	return f.emit(seconds, cfg.Applied("policy-"+digit, strings.Repeat(digit, 64)))
}
func (f *fixture) enqueue(seconds, pr int, head string) {
	f.t.Helper()
	f.emit(seconds, queue.PRObserved{PR: pr, HeadSHA: head, Eligible: true, Trusted: true})
	f.emit(seconds, queue.EnqueueRequested{PR: pr, At: epoch.Add(time.Duration(seconds) * time.Second)})
	if entry(f.s, pr) == nil {
		f.t.Fatalf("PR %d was not admitted", pr)
	}
}
func (f *fixture) start(seconds, pr int, tested string, method queue.MergeMethod, chained bool) string {
	f.t.Helper()
	f.next++
	id := fmt.Sprintf("b%d", f.next)
	en := entry(f.s, pr)
	if en == nil {
		f.t.Fatal("missing admission")
	}
	prs := []queue.BatchPR{{PR: pr, HeadSHA: en.HeadSHA}}
	at := epoch.Add(time.Duration(seconds) * time.Second)
	if chained {
		parent, base, pos := "", f.s.BaseSHA, uint64(1)
		bs := f.s.Batches()
		if len(bs) > 0 {
			b := bs[len(bs)-1]
			parent, base, pos = b.ID, b.LandingCommit(), b.ChainPosition+1
		}
		f.emit(seconds, queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, PRs: prs, At: at, ParentBatchID: parent, Position: pos})
		landing := tested
		if method == queue.FastForward {
			landing = en.HeadSHA
		}
		f.emit(seconds, queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: tested, LandingSHA: landing, At: at, Method: method, ParentBatchID: parent, Position: pos})
	} else {
		f.emit(seconds, queue.BatchStarted{BatchID: id, BaseSHA: f.s.BaseSHA, PRs: prs, At: at})
		if method == queue.FastForward {
			f.emit(seconds, queue.FastForwardBatchSelected{BatchID: id, PRs: prs})
			f.emit(seconds, queue.FastForwardRefBuilt{BatchID: id, TestedSHA: tested, HeadSHA: en.HeadSHA, At: at})
		} else {
			f.emit(seconds, queue.TestRefBuilt{BatchID: id, TestedSHA: tested, At: at, Method: method})
		}
	}
	if b := f.s.Batch(id); b == nil || b.TestedSHA != tested {
		f.t.Fatalf("batch %s not built: %+v", id, b)
	}
	return id
}
func (f *fixture) begin(seconds int, id string) {
	f.t.Helper()
	b := f.s.Batch(id)
	f.emit(seconds, queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	f.emit(seconds, queue.LandingStarted{BatchID: id, AttemptID: "attempt-" + id, FenceToken: f.s.Fence, At: epoch.Add(time.Duration(seconds) * time.Second)})
	if f.s.Batch(id).Phase != queue.Landing {
		f.t.Fatal("landing did not start")
	}
}
func (f *fixture) land(seconds int, id, head string) store.JournalEvent {
	f.t.Helper()
	if head == "" {
		head = f.s.Batch(id).LandingCommit()
	}
	return f.emit(seconds, queue.Landed{BatchID: id, NewBaseSHA: head, NewFence: "fence-" + id})
}
func testWindow(from, to int) Window {
	return Window{From: Boundary{Time: epoch.Add(time.Duration(from) * time.Second)}, To: Boundary{Time: epoch.Add(time.Duration(to) * time.Second)}}
}
func mustReport(t *testing.T, f *fixture, w Window) Report {
	t.Helper()
	r, err := reportJournal(f.j, w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestReportReenqueueAndRejectedDuplicate(t *testing.T) {
	f := newFixture(t)
	f.emit(-10, queue.EnqueueRequested{PR: 1, At: epoch.Add(-time.Hour)}) // rejected, no trusted observation
	f.enqueue(-5, 1, "head")
	f.emit(5, queue.Dequeued{PR: 1, AdmissionID: entry(f.s, 1).AdmissionID, Reason: queue.Requested})
	f.enqueue(10, 1, "head")
	f.emit(15, queue.EnqueueRequested{PR: 1, At: epoch.Add(15 * time.Second)}) // already queued
	id := f.start(20, 1, "tested", queue.Merge, false)
	f.begin(30, id)
	f.land(40, id, "")
	r := mustReport(t, f, testWindow(0, 50))
	if r.Landed != 1 || len(r.Admissions) != 1 || r.Changes[0].Seconds != 30 || r.Changes[0].FirstSeconds != 45 {
		t.Fatalf("report=%+v", r)
	}
	if r.Admissions[0].QueueLength != 1 || r.TimeToMerge.Median != 30 || r.FirstEnqueue.P95 != 45 {
		t.Fatalf("wrong metrics: %+v", r)
	}
}
func TestCohortsAcrossConfigBoundary(t *testing.T) {
	f := newFixture(t)
	a := f.config(0, 4, "a")
	f.enqueue(10, 1, "h1")
	b1 := f.start(20, 1, "t1", queue.Merge, false)
	f.begin(30, b1)
	f.land(40, b1, "")
	f.enqueue(50, 2, "h2")
	b2 := f.start(60, 2, "t2", queue.Merge, false)
	f.begin(90, b2)
	b := f.config(100, 1, "b")
	f.land(110, b2, "")
	f.enqueue(120, 3, "h3")
	b3 := f.start(130, 3, "t3", queue.Merge, false)
	f.begin(140, b3)
	f.land(150, b3, "")
	start, err := boundary(f.j, fmt.Sprint(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	middle, err := boundary(f.j, "commit:policy-b")
	if err != nil {
		t.Fatal(err)
	}
	if middle.EventID != b.ID {
		t.Fatal("wrong boundary")
	}
	ra := mustReport(t, f, Window{From: start, To: middle})
	rb := mustReport(t, f, Window{From: middle, To: Boundary{Time: epoch.Add(200 * time.Second)}})
	if ra.Landed != 1 || rb.Landed != 1 || ra.Changes[0].PR != 1 || rb.Changes[0].PR != 3 || len(ra.Admissions) != 2 || len(rb.Admissions) != 1 {
		t.Fatalf("A=%+v B=%+v", ra, rb)
	}
}
func TestEngagementAndTimeWeightedOpenBatches(t *testing.T) {
	f := newFixture(t)
	f.config(-10, 1, "a")
	f.config(0, 4, "b")
	f.enqueue(5, 1, "h1")
	f.enqueue(5, 2, "h2")
	b1 := f.start(10, 1, "t1", queue.Merge, true)
	b2 := f.start(20, 2, "t2", queue.Merge, true)
	f.begin(35, b1)
	f.land(40, b1, "")
	f.begin(45, b2)
	f.land(50, b2, "")
	r := mustReport(t, f, testWindow(0, 100))
	if r.Engaged != 1 || r.Changes[0].Position != 1 || r.Changes[1].Position != 2 {
		t.Fatalf("wrong engagement: %+v", r.Changes)
	}
	if r.NonIdleSeconds != 45 || r.MultipleBatchSeconds != 20 || r.MultipleBatchShare != 20.0/45 {
		t.Fatalf("wrong durations: %+v", r)
	}
	if r.WindowHistory[len(r.WindowHistory)-1].W != 3 {
		t.Fatalf("W did not carry 1 -> 2 -> 3: %+v", r.WindowHistory)
	}
	if r.Admissions[1].QueueLength != 2 {
		t.Fatal("queue length must include waiting/batched admissions")
	}
}
func TestWindowHalvingUsesRealFoldAndSuppressesDuplicates(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	f.enqueue(5, 1, "h")
	id := f.start(10, 1, "t", queue.Merge, true)
	failure := queue.ChecksFailureConfirmed{BatchID: id, TestedSHA: "t", Generation: 0, Reason: queue.ChecksFailed}
	f.emit(20, failure)
	f.emit(21, failure)
	r := mustReport(t, f, testWindow(0, 100))
	if len(r.Halvings) != 1 || r.Halvings[0].Before != 4 || r.Halvings[0].After != 2 || r.Halvings[0].At != epoch.Add(20*time.Second) {
		t.Fatalf("halvings=%+v", r.Halvings)
	}
	if r.WindowHistory[len(r.WindowHistory)-1].W != 2 {
		t.Fatal("W not replayed")
	}
}
func TestNearestRank(t *testing.T) {
	var values []float64
	for i := 20; i >= 1; i-- {
		values = append(values, float64(i))
	}
	if p := percentiles(values); p.Median != 10 || p.P95 != 19 {
		t.Fatalf("percentiles=%+v", p)
	}
	if p := percentiles([]float64{42}); p.Median != 42 || p.P95 != 42 {
		t.Fatal(p)
	}
}
func TestBoundarySameTimestampAndErrors(t *testing.T) {
	f := newFixture(t)
	f.enqueue(0, 1, "h1")
	c := f.config(0, 4, "a")
	f.enqueue(0, 2, "h2")
	b, err := boundary(f.j, fmt.Sprintf("event:%d", c.ID))
	if err != nil {
		t.Fatal(err)
	}
	r := mustReport(t, f, Window{From: b, To: Boundary{Time: epoch.Add(time.Hour)}})
	if len(r.Admissions) != 1 || r.Admissions[0].PR != 2 {
		t.Fatal(r.Admissions)
	}
	if _, err := boundary(f.j, "missing"); err == nil {
		t.Fatal("missing boundary accepted")
	}
	f.config(1, 4, "a")
	if _, err := boundary(f.j, "policy-a"); err == nil {
		t.Fatal("ambiguous commit accepted")
	}
	if _, err := reportJournal(f.j, testWindow(2, 1)); err == nil {
		t.Fatal("reversed window accepted")
	}
}

func TestReplayDwellWithOutOfOrderTransactionTimestamps(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	f.enqueue(5, 1, "h1")
	f.enqueue(5, 2, "h2")
	b1 := f.start(20, 1, "t1", queue.Merge, true)
	b2 := f.start(15, 2, "t2", queue.Merge, true) // transaction started before b1 obtained the queue lock
	f.begin(35, b1)
	f.land(40, b1, "")
	f.begin(45, b2)
	f.land(50, b2, "")
	r := mustReport(t, f, testWindow(0, 100))
	if r.MultipleBatchSeconds != 20 || r.NonIdleSeconds != 45 {
		t.Fatalf("dwell double counted: %+v", r)
	}
	for i, p := range r.WindowHistory {
		if i > 0 && p.At.Before(r.WindowHistory[i-1].At) {
			t.Fatal("timeline moved backward")
		}
	}
}
func TestDuplicateConfigCannotOverrideReplayedBounds(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	f.config(10, 1, "a") // same digest: fold ignores the new bounds
	r := mustReport(t, f, testWindow(0, 100))
	if !depthThroughout(r, 4) {
		t.Fatal(r.WindowHistory)
	}
}
