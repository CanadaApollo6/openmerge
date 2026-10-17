package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAdaptiveStatusInputsAndBoundedSummary(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	cfg := queue.Config{SpeculationMaxDepth: 4, BatchMaxSize: 8, BatchTargetPassRate: .8, MergeMethod: queue.Merge, ConfigDigest: strings.Repeat("a", 64)}
	state := queue.Apply(queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}, cfg.Applied("policy", cfg.ConfigDigest))
	for pr := 1; pr <= 20; pr++ {
		state = queue.Apply(state, queue.PRObserved{PR: pr, HeadSHA: "head", Eligible: true, Trusted: true})
		state = queue.Apply(state, queue.EnqueueRequested{PR: pr, At: at})
		id := fmt.Sprintf("b%d", pr)
		sha := "tested-" + id
		state = queue.Apply(state, queue.ChainedBatchStarted{BatchID: id, BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: pr, HeadSHA: "head"}}, At: at, Position: 1})
		state = queue.Apply(state, queue.ChainedTestRefBuilt{BatchID: id, TestedSHA: sha, LandingSHA: sha, Method: queue.Merge, At: at, Position: 1})
		if pr <= 2 {
			state = queue.Apply(state, queue.ChecksFailureConfirmed{BatchID: id, TestedSHA: sha, Reason: queue.ChecksFailed})
			state = queue.Apply(state, queue.Dequeued{PR: pr, AdmissionID: uint64(pr), Reason: queue.ChecksFailed})
		} else {
			state = queue.Apply(state, queue.ChecksCompleted{TestedSHA: sha, Success: true})
			state = queue.Apply(state, queue.LandingStarted{BatchID: id, AttemptID: id, FenceToken: state.Fence, At: at})
			state = queue.Apply(state, queue.Landed{BatchID: id, NewBaseSHA: sha, NewFence: "next"})
		}
	}
	q, _, _ := Project(store.Journal{Seed: state}, 0, 0, nil)
	ApplyPolicy(&q, nil, cfg, at)
	if q.Window != 4 || q.BatchSize != 2 || q.FailureEstimate.Failures != 2 || q.FailureEstimate.Samples != 20 || q.FailureEstimate.Rate != .1 || q.LastWindowFailure.BatchID != "b2" {
		t.Fatalf("missing adaptive inputs: %+v", q)
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"window":4`, `"batch_size":2`, `"failure_estimate":{"failures":2,"samples":20,"rate":0.1,"target_pass_rate":0.8}`, `"last_window_failure"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("API missing %s", want)
		}
	}
	for i := 0; i < 32; i++ {
		q.Chain = append(q.Chain, &Batch{ID: strings.Repeat("b", 1000), Phase: strings.Repeat("testing", 1000)})
	}
	text := AdaptiveSummary(&q)
	if len([]rune(text)) > 1000 || !strings.Contains(text, "p̂: 0.1000 (2/20)") || !strings.Contains(text, "(+24)") || !strings.Contains(text, "last window failure:") {
		t.Fatalf("summary missing or unbounded: %s", text)
	}
}
