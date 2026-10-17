package status

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRound2StaleFailureDoesNotAbandonReadOnlyPhase(t *testing.T) {
	tail := identified("b2", 2, 1, "pending")
	events := append(ledgerChain(), tail,
		queue.PRObserved{PR: 2, HeadSHA: "new", Eligible: true, Trusted: true},
		queue.BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "change-left-queue"},
	)
	j := journal(events...)
	s := j.Seed
	for _, event := range events {
		s = queue.Apply(s, event)
	}
	cfg := queue.Config{SpeculationMaxDepth: 4, CheckTimeout: time.Hour, MergeMethod: queue.Merge}
	now := at
	for _, a := range queue.Plan(cfg, s, now) {
		if a, ok := a.(queue.RecordCheckCancellation); ok {
			e := queue.CheckCancellationRequested(a)
			events = append(events, e)
			s = queue.Apply(s, e)
		}
	}
	var action queue.Action
	for attempt := 0; attempt < queue.CancellationAttemptLimit; attempt++ {
		action = nil
		for _, a := range queue.Plan(cfg, s, now) {
			switch a.(type) {
			case queue.CancelChecks, queue.RetryCheckCancellation:
				action = a
			}
		}
		if action == nil {
			t.Fatalf("missing cancellation attempt %d", attempt)
		}
		if attempt == queue.CancellationAttemptLimit-1 {
			break
		}
		deferred, _ := queue.DeferCheckCancellation(action, now, "transient error")
		events = append(events, deferred)
		s = queue.Apply(s, deferred)
		now = now.Add(min(30*time.Second*time.Duration(1<<attempt), 30*time.Minute))
	}
	// The replacement worker reports unsupported on attempt 7. Its stale
	// predecessor then persists a timeout from the same claimed action key.
	unsupported := queue.CheckCancellationReported{BatchID: "b2", TestedSHA: "tested-b2", Unsupported: true, Detail: "permission revoked"}
	events = append(events, unsupported)
	s = queue.Apply(s, unsupported)
	stale, _ := queue.DeferCheckCancellation(action, now, "stale worker timeout")
	if !stale.Abandoned {
		t.Fatal("expected exhausted old mutation phase")
	}
	events = append(events, stale)
	s = queue.Apply(s, stale)
	found := false
	for _, a := range queue.Plan(cfg, s, now) {
		if _, ok := a.(queue.ObserveRetiredChecks); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("read-only phase unexpectedly stopped")
	}
	_, entry, _ := Project(journal(events...), 2, 0, nil)
	if entry.RunLedger[0].Cancellation != "unsupported" {
		t.Fatalf("read-only observation is still scheduled, but ledger says %+v", entry.RunLedger[0])
	}
}
