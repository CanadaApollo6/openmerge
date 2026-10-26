package main

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/google/go-github/v93/github"
)

func TestCohortDrainOutcomesAndHeldArrivals(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	f.enqueue(5, 1, "h1")
	f.enqueue(6, 2, "h2")
	id := f.start(10, 1, "tested", queue.Merge, false)
	// PR 3 is first eligible in the drain, and waits unqueued for B.
	f.emit(65, queue.PRObserved{PR: 3, HeadSHA: "h3", Eligible: true, Trusted: true})
	f.begin(70, id)
	landed := f.land(80, id, "")
	f.emit(90, queue.Dequeued{PR: 2, AdmissionID: entry(f.s, 2).AdmissionID, Reason: queue.Requested})
	// A second held arrival is still unadmitted at report time.
	f.emit(95, queue.PRObserved{PR: 4, HeadSHA: "h4", Eligible: true, Trusted: true})
	f.config(120, 1, "b")
	f.enqueue(125, 3, "h3")
	b := f.start(130, 3, "tested-b", queue.Merge, false)
	if b != "b2" {
		t.Fatal("ConfigApplied reset nextBatch", b)
	}
	f.begin(140, b)
	f.land(150, b, "")
	r, err := reportJournalAt(f.j, testWindow(0, 60), epoch.Add(200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	runs := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 100, "tested")}}
	r.Runs, err = countRuns(t.Context(), nil, runs, "main", r.Batches)
	if err != nil {
		t.Fatal(err)
	}
	finishDrain(&r, f.j)
	if !r.DrainComplete || r.QueueDrainedAt != epoch.Add(90*time.Second) || r.DrainEnd != epoch.Add(100*time.Second) || r.DrainSeconds != 40 {
		t.Fatalf("drain: %+v", r)
	}
	if r.Landed != 1 || r.Dequeued != 1 || r.Pending != 0 || r.LandedAtClose != 0 || r.Changes[0].Seconds != 75 || len(r.Admissions) != 2 {
		t.Fatalf("cohort: %+v", r)
	}
	if len(r.HeldArrivals) != 2 || r.HeldArrivals[0].PR != 3 || r.HeldArrivals[0].AdmittedAt != epoch.Add(125*time.Second) || !r.HeldArrivals[1].AdmittedAt.IsZero() {
		t.Fatal(r.HeldArrivals)
	}
	// The landing at t=80 is outside the admission window but inside the drain.
	audit := AuditResult{Version: 2, Queue: r.Queue, Since: epoch, Until: epoch.Add(101 * time.Second)}
	if err := attachAudit(&r, f.j, audit); err == nil {
		t.Fatal("drain landing escaped audit")
	}
	audit.CheckedEventIDs = []int64{landed.ID}
	audit.Violations = []AuditViolation{{EventID: landed.ID, At: landed.RecordedAt, Check: "tested_commit"}}
	if err := attachAudit(&r, f.j, audit); err != nil || len(r.Audit.Violations) != 1 {
		t.Fatal("lost drain violation", err)
	}
}

func TestIncompleteDrainsAndLateDiscardedAttempt(t *testing.T) {
	for _, mode := range []string{"cohort outstanding", "CI queued", "CI in progress", "late discarded attempt"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.config(0, 4, "a")
			f.enqueue(5, 1, "h")
			id := f.start(10, 1, "discarded", queue.Merge, false)
			if mode != "cohort outstanding" {
				f.emit(80, queue.Dequeued{PR: 1, AdmissionID: entry(f.s, 1).AdmissionID, Reason: queue.Requested})
			}
			f.config(100, 1, "b")
			run := runFixture(1, 1, 10, 11, 90, "discarded")
			switch mode {
			case "CI queued":
				run.Status = new("queued")
				run.RunStartedAt = nil
			case "CI in progress":
				run.Status = new("in_progress")
			case "late discarded attempt":
				run = runFixture(1, 2, 10, 150, 160, "discarded")
			}
			a := &fakeActions{runs: []*github.WorkflowRun{run}, historical: map[[2]int64]*github.WorkflowRun{{1, 1}: runFixture(1, 1, 10, 11, 90, "discarded")}}
			if mode != "late discarded attempt" {
				a.historical = nil
			}
			r, err := reportJournalAt(f.j, testWindow(0, 60), epoch.Add(200*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			r.Runs, err = countRuns(t.Context(), nil, a, "main", r.Batches)
			if err != nil {
				t.Fatal(err)
			}
			finishDrain(&r, f.j)
			if id != "b1" || len(r.Batches) != 1 {
				t.Fatal(r.Batches)
			}
			if mode == "late discarded attempt" {
				if !r.DrainComplete || r.DrainEnd != epoch.Add(160*time.Second) || r.RunAttempts != 2 || !strings.Contains(strings.Join(r.Warnings, " "), "before period drain") {
					t.Fatalf("late attempt not attributed to A: %+v", r)
				}
			} else if r.DrainComplete || !strings.Contains(strings.Join(r.Warnings, " "), "drain incomplete") {
				t.Fatalf("accepted incomplete drain: %+v", r)
			}
		})
	}
}

func TestEngagementUsesTestedLandingBatchNotDiscardedBatch(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	for pr := 1; pr <= 3; pr++ {
		f.enqueue(5, pr, "head")
	}
	b1 := f.start(10, 1, "t1", queue.Merge, true)
	b2 := f.start(15, 2, "t2", queue.Merge, true)
	b3 := f.start(20, 3, "t3", queue.Merge, true)
	f.begin(30, b1)
	f.land(35, b1, "")
	f.begin(40, b2)
	f.land(45, b2, "")
	f.begin(50, b3)
	f.land(55, b3, "")
	f.enqueue(60, 4, "h4")
	f.enqueue(60, 5, "h5")
	f.start(65, 4, "t4", queue.Merge, true)
	b5 := f.start(70, 5, "t5", queue.Merge, true)
	f.emit(75, queue.ChecksCompleted{TestedSHA: "t5", Success: true})
	// Remove the predecessor and discard its tested speculative suffix.
	f.emit(80, queue.Dequeued{PR: 4, AdmissionID: entry(f.s, 4).AdmissionID, Reason: queue.Requested})
	f.emit(81, queue.BatchesInvalidated{BatchIDs: []string{b5}, Reason: "predecessor-invalidated"})
	if f.s.Batch(b5) != nil {
		t.Fatal("fixture suffix not discarded")
	}
	b6 := f.start(85, 5, "t6", queue.Merge, true)
	f.begin(90, b6)
	f.land(95, b6, "")
	r := mustReport(t, f, testWindow(0, 100))
	if r.Landed != 4 || r.Engaged != 2 || r.DeepEngaged != 1 || r.Changes[3].PR != 5 || r.Changes[3].Position != 1 {
		t.Fatalf("wrong engagement: %+v", r)
	}
}

func TestHoldoverSensitivity(t *testing.T) {
	a, b := comparableReports()
	a.HeldArrivals = []HeldArrival{{PR: 1, EligibleAt: a.Window.To.Time}}
	b.Changes = []Change{{PR: 1, Seconds: 1000}, {PR: 2, Seconds: 10}, {PR: 3, Seconds: 20}}
	c := compare(a, b)
	if c.BLatency.With.Median != 20 || c.BLatency.With.P95 != 1000 || c.BLatency.Without.Median != 10 || c.BLatency.Without.P95 != 20 || c.BLatency.HoldoverLanded != 1 || c.BLatency.WithoutCount != 2 {
		t.Fatal(c.BLatency)
	}
}

func TestIncompleteDrainRetainsAuditViolationsAfterClose(t *testing.T) {
	f := newFixture(t)
	f.config(0, 4, "a")
	f.enqueue(5, 1, "h1")
	f.enqueue(6, 2, "h2")
	id := f.start(10, 1, "t1", queue.Merge, false)
	f.begin(70, id)
	landed := f.land(80, id, "")
	r, err := reportJournalAt(f.j, testWindow(0, 60), epoch.Add(100*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	finishDrain(&r, f.j)
	if r.DrainComplete {
		t.Fatal("second change still queued")
	}
	a := AuditResult{Version: 2, Queue: r.Queue, Since: epoch, Until: epoch.Add(101 * time.Second), CheckedEventIDs: []int64{landed.ID}, Violations: []AuditViolation{{EventID: landed.ID, At: landed.RecordedAt, Check: "tested_commit"}}}
	if err := attachAudit(&r, f.j, a); err != nil || len(r.Audit.Violations) != 1 {
		t.Fatal("provisional drain lost violation", err)
	}
	if c := compare(r, Report{}); c.Status != "aborted" {
		t.Fatal(c)
	}
}
