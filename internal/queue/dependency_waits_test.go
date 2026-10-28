package queue

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func eligible(n int, waiting ...int) map[int]DependencyWait {
	out := map[int]DependencyWait{}
	for pr := 1; pr <= n; pr++ {
		out[pr] = DependencyWait{Head: fmt.Sprintf("head%d", pr), Waiting: slices.Contains(waiting, pr)}
	}
	return out
}

func builds(cfg Config, s State, now time.Time) [][]int {
	var out [][]int
	for _, action := range Plan(cfg, s, now) {
		switch a := action.(type) {
		case BuildTestRef:
			out = append(out, batchNumbers(a.PRs))
		case BuildChainedTestRef:
			out = append(out, batchNumbers(a.PRs))
		}
	}
	return out
}

func TestDependencyWaitsGatePlannerAtDepthsOneAndFour(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			cfg := batchConfig(2)
			cfg.SpeculationMaxDepth = depth
			s := batchQueue(4)
			if got, legacy := builds(cfg, s.WithDependencyWaits(nil), epoch), builds(cfg, s, epoch); !reflect.DeepEqual(got, legacy) || !reflect.DeepEqual(legacy, [][]int{{1, 2}}) {
				t.Fatalf("nil overlay changed legacy plan: %v vs %v", got, legacy)
			}
			gated := s.WithDependencyWaits(eligible(4, 1, 3))
			if got := builds(cfg, gated, epoch); !reflect.DeepEqual(got, [][]int{{2, 4}}) {
				t.Fatalf("waiting members consumed slots: %v", got)
			}
			// Missing or other-head values fail closed rather than meaning "no edges".
			partial := eligible(4)
			delete(partial, 2)
			partial[4] = DependencyWait{Head: "pushed"}
			if got := builds(cfg, s.WithDependencyWaits(partial), epoch); !reflect.DeepEqual(got, [][]int{{1, 3}}) {
				t.Fatalf("unknown admission treated as dependency-free: %v", got)
			}
			// Starting the gated plan is accepted only by the same gated view.
			action, start := Plan(cfg, gated, epoch)[0], Event(nil)
			switch a := action.(type) {
			case BuildTestRef:
				start = BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: epoch}
			case BuildChainedTestRef:
				start = ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, ParentBatchID: a.ParentBatchID, Position: a.Position, At: epoch}
			}
			if start == nil || !CanStart(cfg, gated, action, start) || CanStart(cfg, s, action, start) {
				t.Fatal("start revalidation ignored the dependency overlay", action)
			}
			if !reflect.DeepEqual(Apply(gated, start).Batches(), Apply(s, start).Batches()) {
				t.Fatal("overlay changed the fold")
			}
		})
	}
}

func TestDependencyWaitsNeverBisectedRetriedOrDequeued(t *testing.T) {
	cfg := batchConfig(4)
	s := batchQueue(4)
	s = startPlanned(t, cfg, s)
	s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "t1", Method: Merge, At: epoch})
	s = Apply(s, ChecksCompleted{TestedSHA: "t1", Success: false})
	if got := builds(cfg, s, epoch); !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Fatalf("legacy bisection %v", got)
	}
	// Member 1 started waiting after the failed composition: its bisection half
	// keeps only eligible members and the waiting change is never a suspect.
	gated := s.WithDependencyWaits(eligible(4, 1))
	if got := builds(cfg, gated, epoch); !reflect.DeepEqual(got, [][]int{{2}}) {
		t.Fatalf("waiting member entered a bisection group: %v", got)
	}
	for _, action := range Plan(cfg, s.WithDependencyWaits(eligible(4, 1, 2, 3, 4)), epoch.Add(24*time.Hour)) {
		if _, ok := action.(BuildTestRef); ok {
			t.Fatal("all-waiting queue built", action)
		}
		if report, ok := action.(ReportStatus); ok && report.Status == "dequeued" {
			t.Fatal("dependency wait dequeued", action)
		}
	}
	if len(Apply(s, BatchStarted{}).Entries) != 4 {
		t.Fatal("wait removed entries")
	}
}

func TestDependencyWaitSuspendsBatchMaxWait(t *testing.T) {
	cfg := batchConfig(3)
	cfg.BatchMaxWait = 10 * time.Minute
	s := batchQueue(2)
	waits := eligible(2, 2)
	waits[1] = DependencyWait{Head: "head1", EligibleAt: epoch.Add(30 * time.Minute)}
	gated := s.WithDependencyWaits(waits)
	if got := builds(cfg, gated, epoch.Add(35*time.Minute)); got != nil {
		t.Fatalf("waiting time charged against max_wait: %v", got)
	}
	if at, ok := NextWake(cfg, gated, epoch.Add(35*time.Minute)); !ok || !at.Equal(epoch.Add(40*time.Minute)) {
		t.Fatalf("resumed deadline %v %v", at, ok)
	}
	if got := builds(cfg, gated, epoch.Add(40*time.Minute)); !reflect.DeepEqual(got, [][]int{{1}}) {
		t.Fatalf("eligible-only max_wait %v", got)
	}
	if got := builds(cfg, s, epoch.Add(35*time.Minute)); !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Fatalf("legacy max_wait %v", got)
	}
}

// driveDependencyWaits journals exactly what the activated store does: on each
// start it first appends DependencyWaitsWithheld for the gated pending members,
// then the planned start. The journal is folded WITHOUT the overlay, as the
// store's replay does, so a planner/fold disagreement stalls here.
func driveDependencyWaits(t *testing.T, cfg Config, s State, waits map[int]DependencyWait, broken []int, once bool, journal *[]Event) (State, [][]int, []int) {
	t.Helper()
	apply := func(e Event) {
		*journal = append(*journal, e)
		s = Apply(s, e)
	}
	var tree [][]int
	var merged []int
	for step := 0; step < 200; step++ {
		gated := s.WithDependencyWaits(waits)
		progressed := false
		for _, action := range Plan(cfg, gated, epoch) {
			var start Event
			var id string
			var prs []BatchPR
			switch a := action.(type) {
			case BuildTestRef:
				start, id, prs = BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: epoch}, a.BatchID, a.PRs
			case BuildChainedTestRef:
				start, id, prs = ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, ParentBatchID: a.ParentBatchID, Position: a.Position, At: epoch}, a.BatchID, a.PRs
			case DiagnoseBatch:
				apply(BatchDiagnosed(a))
				progressed = true
			case Land:
				merged = append(merged, batchNumbers(a.PRs)...)
				apply(LandingStarted{BatchID: a.BatchID, AttemptID: a.BatchID, FenceToken: s.Fence, At: epoch})
				apply(Landed{BatchID: a.BatchID, NewBaseSHA: s.Batch(a.BatchID).LandingCommit(), NewFence: "f-" + a.BatchID})
				progressed = true
			case Dequeue:
				if w, ok := waits[a.PR]; ok && w.Waiting {
					t.Fatal("dependency wait dequeued", a)
				}
				apply(Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})
				progressed = true
			}
			if start == nil {
				if progressed {
					break
				}
				continue
			}
			if !CanStart(cfg, gated, action, start) {
				t.Fatal("planned start refused", action)
			}
			if withheld := gated.DependencyWithheld(); len(withheld) > 0 {
				apply(DependencyWaitsWithheld{PRs: withheld})
			}
			apply(start)
			b := s.Batch(id)
			if b == nil {
				t.Fatalf("fold refused planned start %v; pending=%v", batchNumbers(prs), s.pending)
			}
			tree = append(tree, batchNumbers(prs))
			failing := false
			for _, pr := range prs {
				if w := waits[pr.PR]; w.Waiting {
					t.Fatal("waiting member composed", pr)
				}
				failing = failing || slices.Contains(broken, pr.PR)
			}
			sha := "tested-" + id
			if a, ok := action.(BuildChainedTestRef); ok {
				apply(ChainedTestRefBuilt{BatchID: id, TestedSHA: sha, LandingSHA: sha, ParentBatchID: a.ParentBatchID, Position: a.Position, Method: Merge, At: epoch})
			} else {
				apply(TestRefBuilt{BatchID: id, TestedSHA: sha, Method: Merge, At: epoch})
			}
			apply(ChecksCompleted{TestedSHA: sha, Success: !failing})
			if b := s.Batch(id); b != nil && b.Speculative && failing {
				apply(ChecksFailureConfirmed{BatchID: id, TestedSHA: sha, Generation: b.checksGeneration, Reason: ChecksFailed})
			}
			progressed = true
			if once {
				return s, append(tree, nil), merged
			}
			break
		}
		if !progressed {
			return s, tree, merged
		}
	}
	t.Fatal("no fixpoint")
	return s, nil, nil
}

// Waiting members leave their bisection groups through the journaled record:
// the fold (and store replay) accepts every planned start at depths 1 and 4,
// including a fully gated first group, and released changes land afterwards.
func TestDependencyWaitsWithheldFoldAgreesWithPlanner(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, tc := range []struct {
			name    string
			waiting []int
			gated   [][]int
		}{
			{"partial first group", []int{1}, [][]int{{2}, {3, 4}, {3}, {4}}},
			{"fully gated first group", []int{1, 2}, [][]int{{3, 4}, {3}, {4}}},
			{"gated across groups", []int{2, 4}, [][]int{{1}, {3}}},
		} {
			t.Run(fmt.Sprint(depth, "/", tc.name), func(t *testing.T) {
				cfg := batchConfig(4)
				cfg.SpeculationMaxDepth = depth
				var journal []Event
				s, tree, _ := driveDependencyWaits(t, cfg, batchQueue(4), eligible(4), nil, false, &journal)
				if !reflect.DeepEqual(tree, [][]int{{1, 2, 3, 4}}) || len(s.Entries) != 0 {
					t.Fatal("legacy control", tree)
				}
				// A failed 4-batch leaves [1,2],[3,4]; the culprit is 3.
				journal = nil
				s = batchQueue(4)
				s, tree, _ = driveDependencyWaits(t, cfg, s, eligible(4), []int{1, 2, 3, 4}, true, &journal)
				if len(tree) != 2 || len(s.pending) != 2 {
					t.Fatal("setup", tree, s.pending)
				}
				s, tree, merged := driveDependencyWaits(t, cfg, s, eligible(4, tc.waiting...), []int{3}, false, &journal)
				if !reflect.DeepEqual(tree, tc.gated) {
					t.Fatalf("gated tree %v want %v", tree, tc.gated)
				}
				for _, pr := range tc.waiting {
					if slices.Contains(merged, pr) || s.entry(pr) == nil || s.inBatch(pr) {
						t.Fatal("waiting member lost or landed", pr, merged)
					}
				}
				// Released waits compose freshly in entry order and land.
				s, tree, _ = driveDependencyWaits(t, cfg, s, eligible(4), nil, false, &journal)
				if !reflect.DeepEqual(tree, [][]int{tc.waiting}) || len(s.Entries) != 0 {
					t.Fatal("released waits", tree, s.Entries)
				}
				// Store replay: the journal folds identically from scratch.
				replay := batchQueue(4)
				for _, e := range journal {
					replay = Apply(replay, e)
				}
				if len(replay.Entries) != 0 || replay.BaseSHA != s.BaseSHA {
					t.Fatal("replay diverged")
				}
			})
		}
	}
}

func TestDependencyWaitsWithheldFoldIsExact(t *testing.T) {
	cfg := batchConfig(4)
	s := startPlanned(t, cfg, batchQueue(4))
	s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "t1", Method: Merge, At: epoch})
	s = Apply(s, ChecksCompleted{TestedSHA: "t1", Success: false})
	for _, e := range []DependencyWaitsWithheld{
		{},
		{PRs: []BatchPR{{PR: 1, HeadSHA: "pushed"}}},
		{PRs: []BatchPR{{PR: 1, HeadSHA: "head1"}, {PR: 5, HeadSHA: "head5"}}},
		{PRs: []BatchPR{{PR: 1, HeadSHA: "head1"}, {PR: 1, HeadSHA: "head1"}}},
	} {
		if got := Apply(s, e); !reflect.DeepEqual(got.pending, s.pending) {
			t.Fatal("inexact record changed groups", e, got.pending)
		}
	}
	got := Apply(s, DependencyWaitsWithheld{PRs: []BatchPR{{PR: 1, HeadSHA: "head1"}, {PR: 2, HeadSHA: "head2"}}})
	if len(got.pending) != 1 || !reflect.DeepEqual(batchNumbers(got.pending[0].prs), []int{3, 4}) || len(got.Entries) != 4 {
		t.Fatal("withheld members", got.pending)
	}
	if s.DependencyWithheld() != nil || s.WithDependencyWaits(eligible(4)).DependencyWithheld() != nil {
		t.Fatal("ungated view withheld members")
	}
	if got := s.WithDependencyWaits(eligible(4, 2, 3)).DependencyWithheld(); !reflect.DeepEqual(batchNumbers(got), []int{2, 3}) {
		t.Fatal("withheld order", got)
	}
}
