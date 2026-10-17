package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func ledgerChain() []queue.Event {
	events := []queue.Event{queue.BaseMoved{SHA: "base"}}
	for i := 1; i <= 3; i++ {
		events = append(events, queue.PRObserved{PR: i, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: i, At: at})
	}
	for i, id := range []string{"b1", "b2"} {
		parent, base := "", "base"
		if i == 1 {
			parent, base = "b1", "tested-b1"
		}
		events = append(events, queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, PRs: []queue.BatchPR{{PR: i + 1, HeadSHA: "head"}}, At: at, ParentBatchID: parent, Position: uint64(i + 1)}, queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: "tested-" + id, LandingSHA: "tested-" + id, ParentBatchID: parent, Position: uint64(i + 1), Method: queue.Merge, At: at})
	}
	return events
}

func identified(batch string, id, attempt uint64, state string) queue.ChecksObserved {
	return queue.ChecksObserved{BatchID: batch, TestedSHA: "tested-" + batch, Terminal: state != "pending", Success: state == "success", OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour, Runs: []queue.CheckRun{{ID: id, Attempt: attempt, WorkflowRunID: 100 + id, Name: "ci", Source: "app:1", State: state}}}
}

func TestRunLedgerIncludesSpeculativeAncestryRerunsAndLateResults(t *testing.T) {
	original := identified("b1", 1, 1, "failure")
	tail := identified("b2", 2, 1, "pending")
	events := append(ledgerChain(), original, tail, tail,
		queue.CheckRerunRequested{BatchID: "b1", TestedSHA: "tested-b1", Runs: original.Runs, At: at, OriginalDeadline: at.Add(time.Hour), Deadline: at.Add(time.Hour), Timeout: time.Hour},
		identified("b1", 1, 2, "success"),
		queue.DequeueRequested{PR: 2},
		queue.BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "change-left-queue"},
		queue.CheckCancellationRequested{BatchID: "b2", TestedSHA: "tested-b2", TestRef: "openmerge/queue/main/b2", Runs: tail.Runs, Reason: "change-left-queue"},
	)
	j := journal(events...)
	_, first, _ := Project(j, 1, 1, nil)
	_, second, _ := Project(j, 2, 1, nil)
	if len(first.RunLedger) != 3 || len(second.RunLedger) != 1 {
		t.Fatalf("ancestry/rerun history: first=%+v second=%+v", first.RunLedger, second.RunLedger)
	}
	if first.RunLedger[0].WastedReason != "rerun" || first.RunLedger[1].Outcome != "success" || first.RunLedger[1].Identity.Attempt != 2 {
		t.Fatalf("rerun history=%+v", first.RunLedger)
	}
	row := second.RunLedger[0]
	if row.Cancellation != "requested" || !strings.Contains(row.WastedReason, "dequeue of PR #2") || row.Outcome != "pending" {
		t.Fatalf("requested=%+v", row)
	}
	// Cancellation acceptance is not completion. A later attributed observation
	// updates historical evidence but cannot revive the discarded batch.
	j.Events = append(j.Events, store.JournalEvent{ID: 100, Event: identified("b2", 2, 1, "success")})
	q, second, _ := Project(j, 2, 1, nil)
	if len(q.Chain) != 1 || second.RunLedger[0].Outcome != "success" || second.RunLedger[0].Cancellation != "requested" {
		t.Fatalf("late run revived or lost: %+v %+v", q, second)
	}
}

func TestRunLedgerCancellationOutcomes(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "unsupported"}[unsupported], func(t *testing.T) {
			tail := identified("b2", 2, 1, "pending")
			confirmed := tail.Runs[0]
			confirmed.State = "cancelled"
			report := queue.CheckCancellationReported{BatchID: "b2", TestedSHA: "tested-b2", Unsupported: unsupported, Detail: "actions permission unavailable"}
			if !unsupported {
				report.Confirmed = []queue.CheckRun{confirmed}
			}
			events := append(ledgerChain(), tail, queue.PRObserved{PR: 2, HeadSHA: "new", Eligible: true, Trusted: true}, queue.BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "change-left-queue"}, queue.CheckCancellationRequested{BatchID: "b2", TestedSHA: "tested-b2", TestRef: "openmerge/queue/main/b2", Runs: tail.Runs, Reason: "change-left-queue"}, report)
			_, entry, _ := Project(journal(events...), 2, 1, nil)
			row := entry.RunLedger[0]
			if row.Cancellation != map[bool]string{false: "confirmed", true: "unsupported"}[unsupported] || !strings.Contains(row.WastedReason, "superseded by a new head") {
				t.Fatalf("row=%+v", row)
			}
			if !unsupported && row.Outcome != "cancelled" {
				t.Fatal(row)
			}
			data, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{`"run_ledger"`, `"batch_id":"b2"`, `"tested_sha":"tested-b2"`, `"id":2`, `"attempt":1`, `"cancellation"`, `"wasted_reason"`} {
				if !strings.Contains(string(data), field) {
					t.Fatalf("missing %s in %s", field, data)
				}
			}
			if unsupported {
				events = append(events, identified("b2", 2, 1, "success"))
				_, entry, _ = Project(journal(events...), 2, 1, nil)
				row = entry.RunLedger[0]
				if row.Outcome != "success" || row.Cancellation != "unsupported" || row.WastedReason == "" {
					t.Fatalf("late unsupported outcome=%+v", row)
				}
			}
		})
	}
}

func TestRunLedgerLegacyAggregateAndEarlyObservation(t *testing.T) {
	events := testingEvents()
	events = append(events, queue.ChecksCompleted{TestedSHA: "tested", Success: true})
	_, entry, _ := Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 1 || entry.RunLedger[0].Identity != nil || entry.RunLedger[0].Outcome != "success" {
		t.Fatal(entry.RunLedger)
	}
	// Legacy checks retain honest aggregate evidence when producer identity was
	// never recorded. Identified checks can arrive before the ref-built event.
	early := queue.ChecksObserved{BatchID: "b1", TestedSHA: "tested", Runs: []queue.CheckRun{{Name: "ci", Source: "fake", ID: 42, Attempt: 1, State: "success"}}, Terminal: true, Success: true, Timeout: time.Hour, OriginalDeadline: at.Add(time.Hour)}
	events = testingEvents()
	events = append(events[:len(events)-1], early, events[len(events)-1])
	_, entry, _ = Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 1 || entry.RunLedger[0].Identity == nil || entry.RunLedger[0].Identity.ID != 42 {
		t.Fatal(entry.RunLedger)
	}
}

func TestStatusPolicyGates(t *testing.T) {
	t.Run("window", func(t *testing.T) {
		q, entry, _ := Project(journal(ledgerChain()...), 3, 1, nil)
		ApplyPolicy(&q, entry, queue.Config{SpeculationMaxDepth: 2, BatchMaxSize: 8}, at)
		if q.Window != 2 || q.BatchSize != 8 || entry.Gate != "window-full" || q.Entries[0].Gate != "checks" {
			t.Fatalf("queue=%+v entry=%+v", q, entry)
		}
	})
	t.Run("max_wait", func(t *testing.T) {
		events := []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at}}
		q, entry, _ := Project(journal(events...), 1, 1, nil)
		ApplyPolicy(&q, entry, queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8, BatchMaxWait: time.Minute}, at.Add(time.Second))
		if entry.Gate != "max_wait" {
			t.Fatal(entry.Gate)
		}
		ApplyPolicy(&q, entry, queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8, BatchMaxWait: time.Minute}, at.Add(time.Minute))
		if entry.Gate != "batch-admission" {
			t.Fatal(entry.Gate)
		}
	})
	t.Run("bisection", func(t *testing.T) {
		// Use a legacy two-change failure to create pending halves ahead of PR #3.
		events := []queue.Event{queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at}, queue.PRObserved{PR: 2, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: at}, queue.PRObserved{PR: 3, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 3, At: at}, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", At: at, PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}, {PR: 2, HeadSHA: "head"}}}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Merge}, queue.ChecksCompleted{TestedSHA: "tested", Success: false}}
		q, entry, _ := Project(journal(events...), 3, 1, nil)
		ApplyPolicy(&q, entry, queue.Config{SpeculationMaxDepth: 4}, at)
		if entry.Gate != "bisection" {
			t.Fatal(entry.Gate)
		}
	})
}

func TestLedgerSummaryBoundsHistory(t *testing.T) {
	entry := &Entry{Gate: "checks"}
	for i := 0; i < 100; i++ {
		entry.RunLedger = append(entry.RunLedger, Run{BatchID: "b", TestedSHA: "tested", Identity: &queue.CheckRun{Name: strings.Repeat("x", 1000)}, Outcome: "pending"})
	}
	text := LedgerSummary(entry)
	if !strings.Contains(text, "92 earlier run(s)") || !strings.Contains(text, "gate: checks") || len([]rune(text)) > 1500 {
		t.Fatalf("summary length=%d", len([]rune(text)))
	}
}

type policySource struct {
	source
	policy queue.Config
}

func (s *policySource) QueuePolicy(store.QueueKey) (queue.Config, bool) { return s.policy, true }

func TestHTTPAddsCompleteRunLedgerAndConfiguredWindow(t *testing.T) {
	events := append(ledgerChain(), identified("b1", 1, 1, "success"), identified("b2", 2, 1, "pending"))
	src := &policySource{source: source{journals: []store.Journal{journal(events...)}}, policy: queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8}}
	handler := Handler(src, []store.QueueKey{key}, "", nil)
	for _, path := range []string{"/v1/queues", "/v1/queues/owner/repo/main/prs/1?events=1"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		q := response.Queue
		if q == nil {
			q = &response.Queues[0]
		}
		if q.Window != 4 || q.BatchSize != 8 || len(q.Chain) != 2 || len(q.Entries[0].RunLedger) != 2 {
			t.Fatalf("queue=%+v", q)
		}
		if response.PR != nil && (len(response.PR.RunLedger) != 2 || response.PR.Gate != "landing-preflight" || len(response.Why.Events) != 1) {
			t.Fatalf("response=%+v", response)
		}
	}
}

func TestLegacyAggregateLedgerUpdatesOutcome(t *testing.T) {
	events := append(testingEvents(), queue.ChecksCompleted{TestedSHA: "tested", Success: true}, queue.ChecksCompleted{TestedSHA: "tested", Success: false})
	_, entry, _ := Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 1 || entry.RunLedger[0].Outcome != "failure" {
		t.Fatalf("ledger=%+v", entry.RunLedger)
	}
}

func TestRunLedgerPreservesCompletedAttemptAgainstStalePending(t *testing.T) {
	events := append(ledgerChain(), identified("b1", 1, 1, "success"), identified("b1", 1, 1, "pending"))
	_, entry, _ := Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 1 || entry.RunLedger[0].Outcome != "success" {
		t.Fatalf("ledger=%+v", entry.RunLedger)
	}
}

func TestRunLedgerRetainedLegacyFailureAndFastForwardSelection(t *testing.T) {
	events := []queue.Event{queue.BaseMoved{SHA: "base"}}
	for i := 1; i <= 2; i++ {
		events = append(events, queue.PRObserved{PR: i, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: i, At: at})
	}
	events = append(events, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", At: at, PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}, {PR: 2, HeadSHA: "head"}}}, queue.FastForwardBatchSelected{BatchID: "b1", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}}, queue.ChecksCompleted{TestedSHA: "marker", Success: false}, queue.FastForwardRefBuilt{BatchID: "b1", TestedSHA: "marker", HeadSHA: "head", At: at})
	_, first, _ := Project(journal(events...), 1, 1, nil)
	_, second, _ := Project(journal(events...), 2, 1, nil)
	if len(first.RunLedger) != 1 || first.RunLedger[0].TestedSHA != "marker" || len(second.RunLedger) != 0 {
		t.Fatalf("first=%+v second=%+v", first.RunLedger, second.RunLedger)
	}
}

func TestRunLedgerIncludesNonRequiredWorkflowsAlongsideRetrySubset(t *testing.T) {
	required := identified("b1", 1, 1, "failure")
	optional := queue.CheckRun{ID: 2, WorkflowRunID: 102, Attempt: 1, Name: "documentation", Source: "app:1", State: "pending"}
	events := append(ledgerChain(), queue.BatchRunsObserved{BatchID: "b1", TestedSHA: "tested-b1", Runs: append(append([]queue.CheckRun(nil), required.Runs...), optional)}, required)
	_, entry, _ := Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 2 || entry.RunLedger[1].Identity.Name != "documentation" {
		t.Fatalf("non-required workflow lost: %+v", entry.RunLedger)
	}
	events = append(events, queue.CheckRerunRequested{BatchID: "b1", TestedSHA: "tested-b1", Runs: required.Runs, At: at, OriginalDeadline: at.Add(time.Hour), Deadline: at.Add(time.Hour), Timeout: time.Hour}, identified("b1", 1, 2, "success"))
	_, entry, _ = Project(journal(events...), 1, 1, nil)
	if len(entry.RunLedger) != 3 || entry.RunLedger[0].WastedReason != "rerun" || entry.RunLedger[1].Identity.Name != "documentation" || entry.RunLedger[1].WastedReason != "" || entry.RunLedger[2].Identity.Attempt != 2 {
		t.Fatalf("retry subset changed other workflow evidence: %+v", entry.RunLedger)
	}
}

func TestRunLedgerMarksManualRerunsWithoutQueueRequest(t *testing.T) {
	original := identified("b1", 1, 1, "success")
	rerun := identified("b1", 2, 1, "success")
	for _, lateOriginal := range []bool{false, true} {
		events := ledgerChain()
		if lateOriginal {
			events = append(events, rerun, original)
		} else {
			events = append(events, original, rerun, original)
		}
		_, entry, _ := Project(journal(events...), 1, 1, nil)
		if len(entry.RunLedger) != 2 {
			t.Fatalf("missing manual rerun: %+v", entry.RunLedger)
		}
		for _, run := range entry.RunLedger {
			if run.Identity.ID == 1 && run.WastedReason != "rerun" {
				t.Fatal("superseded manual attempt has no waste reason")
			}
			if run.Identity.ID == 2 && run.WastedReason != "" {
				t.Fatal("late original marked newer attempt wasted")
			}
		}
	}
}

func TestRunLedgerCanonicalizesZeroAndOneAttempts(t *testing.T) {
	events := ledgerChain()
	events = append(events, identified("b2", 2, 0, "pending"), identified("b2", 2, 1, "success"))
	_, entry, _ := Project(journal(events...), 2, 1, nil)
	if len(entry.RunLedger) != 1 || entry.RunLedger[0].Identity.Attempt != 1 || entry.RunLedger[0].Outcome != "success" {
		t.Fatalf("duplicate canonical attempt: %+v", entry.RunLedger)
	}
}

func TestRunLedgerCancellationExhaustionIsVisible(t *testing.T) {
	events := ledgerChain()
	events = append(events, identified("b2", 2, 1, "pending"), queue.CheckCancellationRequested{BatchID: "b2", TestedSHA: "tested-b2", TestRef: "ref", Reason: "closed", Runs: []queue.CheckRun{{ID: 2, Attempt: 1, WorkflowRunID: 102, Name: "ci", Source: "app:1", State: "pending"}}}, queue.CheckCancellationDeferred{BatchID: "b2", TestedSHA: "tested-b2", Key: "batch/b2/cancel/retry/7", At: at, Detail: "404 Not Found", Abandoned: true})
	_, entry, _ := Project(journal(events...), 2, 1, nil)
	if entry.RunLedger[0].Cancellation != "abandoned" || entry.RunLedger[0].CancellationDetail != "404 Not Found" {
		t.Fatalf("exhaustion not visible: %+v", entry.RunLedger)
	}
}
