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
