package status

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestChainStatusAndInvalidationEvidence(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	j := store.Journal{Seed: queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}}
	for i := 1; i <= 2; i++ {
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Event: queue.PRObserved{PR: i, HeadSHA: "head", Eligible: true, Trusted: true}}, store.JournalEvent{ID: int64(len(j.Events) + 2), Event: queue.EnqueueRequested{PR: i, At: at}})
	}
	for i, id := range []string{"b1", "b2"} {
		parent, base := "", "base"
		if i == 1 {
			parent, base = "b1", "tested-b1"
		}
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Event: queue.ChainedBatchStarted{BatchID: id, BaseSHA: base, PRs: []queue.BatchPR{{PR: i + 1, HeadSHA: "head"}}, At: at, ParentBatchID: parent, Position: uint64(i + 1)}}, store.JournalEvent{ID: int64(len(j.Events) + 2), Event: queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: "tested-" + id, LandingSHA: "tested-" + id, ParentBatchID: parent, Position: uint64(i + 1), Method: queue.Merge, At: at}})
	}
	q, e, _ := Project(j, 2, 20, diagnostic.NewSanitizer())
	if len(q.Chain) != 2 || e.BatchID != "b2" || e.BatchesAhead != 1 || q.Chain[1].ParentBatchID != "b1" || q.Chain[1].LandingSHA != "tested-b2" {
		t.Fatalf("chain status=%+v entry=%+v", q.Chain, e)
	}
	j.Events = append(j.Events, store.JournalEvent{ID: 9, Event: queue.DequeueRequested{PR: 2}}, store.JournalEvent{ID: 10, Event: queue.BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "change-left-queue"}})
	q, _, why := Project(j, 2, 20, diagnostic.NewSanitizer())
	if len(q.Chain) != 1 || len(q.Invalidations) != 1 {
		t.Fatal("invalidation missing")
	}
	found := false
	for _, e := range why.Events {
		if e.Type == "batches_invalidated" {
			found = true
		}
	}
	if !found {
		t.Fatal("invalidation missing from change explanation")
	}
}
