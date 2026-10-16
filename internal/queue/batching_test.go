package queue

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func batchQueue(n int) State {
	s := State{BaseBranch: "main", BaseSHA: "base1", Fence: "f0"}
	for pr := 1; pr <= n; pr++ {
		s = Apply(s, PRObserved{PR: pr, HeadSHA: fmt.Sprintf("head%d", pr), Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: pr, At: epoch})
	}
	return s
}

func batchConfig(size int) Config {
	cfg := testConfig
	cfg.BatchMaxSize = size
	return cfg
}

func plannedBuild(t *testing.T, cfg Config, s State, now time.Time) BuildTestRef {
	t.Helper()
	for _, action := range Plan(cfg, s, now) {
		if build, ok := action.(BuildTestRef); ok {
			return build
		}
	}
	t.Fatalf("no build in %v", Plan(cfg, s, now))
	return BuildTestRef{}
}

func startPlanned(t *testing.T, cfg Config, s State) State {
	t.Helper()
	a := plannedBuild(t, cfg, s, epoch)
	return Apply(s, BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: epoch})
}

func batchNumbers(prs []BatchPR) []int {
	var numbers []int
	for _, pr := range prs {
		numbers = append(numbers, pr.PR)
	}
	return numbers
}

func TestBatchFormation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count, size   int
		wait, elapsed time.Duration
		want          []int
		wake          bool
	}{
		{name: "full", count: 10, size: 8, wait: time.Minute, want: []int{1, 2, 3, 4, 5, 6, 7, 8}},
		{name: "partial waits", count: 3, size: 8, wait: time.Minute, elapsed: time.Second, wake: true},
		{name: "deadline", count: 3, size: 8, wait: time.Minute, elapsed: time.Minute, want: []int{1, 2, 3}},
		{name: "zero wait", count: 3, size: 8, want: []int{1, 2, 3}},
		{name: "serial ignores wait", count: 3, size: 1, wait: time.Hour, want: []int{1}},
		{name: "default serial", count: 3, size: 0, want: []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, s := batchConfig(tc.size), batchQueue(tc.count)
			cfg.BatchMaxWait = tc.wait
			original := cloneState(s)
			if s.Active != nil {
				t.Fatal("admission created an unrecorded batch")
			}
			now := epoch.Add(tc.elapsed)
			var got []int
			for _, a := range Plan(cfg, s, now) {
				if build, ok := a.(BuildTestRef); ok {
					got = batchNumbers(build.PRs)
				}
			}
			if !slices.Equal(got, tc.want) || !reflect.DeepEqual(s, original) {
				t.Fatalf("composition=%v want=%v or planner mutated state", got, tc.want)
			}
			at, ok := NextWake(cfg, s, now)
			if ok != tc.wake || ok && !at.Equal(epoch.Add(tc.wait)) {
				t.Fatalf("wake=%v %v", at, ok)
			}
		})
	}
	t.Run("trust and eligibility apply to every entry", func(t *testing.T) {
		s := batchQueue(5)
		s = Apply(s, PRObserved{PR: 2, HeadSHA: "head2", Eligible: true})
		s = Apply(s, PRObserved{PR: 4, HeadSHA: "head4", Trusted: true})
		a := plannedBuild(t, batchConfig(3), s, epoch)
		if !slices.Equal(batchNumbers(a.PRs), []int{1, 3, 5}) {
			t.Fatal(a.PRs)
		}
	})
	t.Run("composition hash fences a stale proposal", func(t *testing.T) {
		cfg, s := batchConfig(8), batchQueue(2)
		a := plannedBuild(t, cfg, s, epoch)
		s = Apply(s, PRObserved{PR: 3, HeadSHA: "head3", Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: 3, At: epoch})
		b := plannedBuild(t, cfg, s, epoch)
		if a.BatchID != b.BatchID || a.Key() == b.Key() {
			t.Fatalf("stale composition retains authorization: %s %s", a.Key(), b.Key())
		}
		s = Apply(s, BatchStarted{BatchID: b.BatchID, BaseSHA: b.BaseSHA, PRs: b.PRs, At: epoch})
		s = Apply(s, PRObserved{PR: 4, HeadSHA: "head4", Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: 4, At: epoch})
		if got := plannedBuild(t, cfg, s, epoch); got.Key() != b.Key() {
			t.Fatal("recorded composition changed")
		}
	})
}

func TestBisectionTrees(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		broken                   []int
		conflict, timeout, flake bool
		want                     [][]int
	}{
		{name: "start", broken: []int{1}, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {1, 2}, {1}, {2}, {3, 4}, {5, 6, 7, 8}}},
		{name: "middle", broken: []int{4}, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {1, 2}, {3, 4}, {3}, {4}, {5, 6, 7, 8}}},
		{name: "end", broken: []int{8}, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {5, 6, 7, 8}, {5, 6}, {7, 8}, {7}, {8}}},
		{name: "two culprits", broken: []int{2, 7}, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {1, 2}, {1}, {2}, {3, 4}, {5, 6, 7, 8}, {5, 6}, {7, 8}, {7}, {8}}},
		{name: "flake passes on retest", flake: true, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {5, 6, 7, 8}}},
		{name: "conflict", broken: []int{5}, conflict: true, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {5, 6, 7, 8}, {5, 6}, {5}, {6}, {7, 8}}},
		{name: "timeout", broken: []int{5}, timeout: true, want: [][]int{{1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4}, {5, 6, 7, 8}, {5, 6}, {5}, {6}, {7, 8}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, s, now := batchConfig(8), batchQueue(8), epoch
			var tree [][]int
			var merged []int
			removed := map[int]DequeueReason{}
			for step := 0; step < 300 && (len(s.Entries) > 0 || len(s.effects) > 0); step++ {
				actions := Plan(cfg, s, now)
				if len(actions) == 0 {
					t.Fatalf("stuck: %+v", s)
				}
				switch a := actions[0].(type) {
				case BuildTestRef:
					tree = append(tree, batchNumbers(a.PRs))
					s = Apply(s, BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now})
					if s.Active.FromBisection != (len(tree) > 1) {
						t.Fatal("bisection provenance lost")
					}
					failing := tc.flake && len(tree) == 1
					for _, pr := range a.PRs {
						failing = failing || slices.Contains(tc.broken, pr.PR)
					}
					if failing && tc.conflict {
						s = Apply(s, TestRefBuildFailed{BatchID: a.BatchID, Conflict: true, At: now})
						continue
					}
					sha := "tested-" + a.BatchID
					s = Apply(s, TestRefBuilt{BatchID: a.BatchID, TestedSHA: sha, Method: Merge, At: now})
					if failing && tc.timeout {
						now = now.Add(cfg.CheckTimeout + time.Second)
						s = Apply(s, ChecksRefreshed{BatchID: a.BatchID, TestedSHA: sha, At: now})
					} else {
						s = Apply(s, ChecksCompleted{TestedSHA: sha, Success: !failing})
					}
				case FailBatch:
					s = Apply(s, BatchFailed(a))
				case Land:
					merged = append(merged, batchNumbers(a.PRs)...)
					s = Apply(s, LandingStarted{BatchID: a.BatchID, AttemptID: a.BatchID, FenceToken: s.Fence, At: now})
					s = Apply(s, Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: "f-" + a.BatchID})
				case Dequeue:
					removed[a.PR] = a.Reason
					s = Apply(s, Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})
				default:
					s = Apply(s, ActionCompleted{Key: a.Key()})
				}
			}
			if !reflect.DeepEqual(tree, tc.want) {
				t.Fatalf("tree=%v want=%v", tree, tc.want)
			}
			if len(merged) != 8-len(tc.broken) || len(removed) != len(tc.broken) || len(s.Entries) != 0 {
				t.Fatalf("merged=%v removed=%v queued=%v", merged, removed, s.Entries)
			}
			reason := ChecksFailed
			if tc.conflict {
				reason = Conflict
			}
			if tc.timeout {
				reason = ChecksTimeout
			}
			for _, pr := range tc.broken {
				if removed[pr] != reason {
					t.Fatalf("PR %d: %s", pr, removed[pr])
				}
			}
		})
	}
}

func bisecting(t *testing.T) State {
	t.Helper()
	s := startPlanned(t, batchConfig(8), batchQueue(8))
	s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "failed", Method: Merge, At: epoch})
	return Apply(s, ChecksCompleted{TestedSHA: "failed"})
}

func TestMidBisectionChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  Event
		reason DequeueReason
	}{
		{"dequeue", DequeueRequested{PR: 2}, Requested},
		{"head", PRObserved{PR: 2, HeadSHA: "new", Eligible: true, Trusted: true}, HeadChanged},
		{"close", PRClosed{PR: 2}, Closed},
		{"ineligible", PRObserved{PR: 2, HeadSHA: "head2", Trusted: true}, NotEligible},
		{"untrusted", PRObserved{PR: 2, HeadSHA: "head2", Eligible: true}, Untrusted},
	} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/active=%v", tc.name, active), func(t *testing.T) {
				s := bisecting(t)
				if active {
					s = startPlanned(t, batchConfig(8), s)
				}
				previous, original := s, cloneState(s)
				s = Apply(s, tc.event)
				if !reflect.DeepEqual(previous, original) {
					t.Fatal("aliased input")
				}
				if s.dequeues[2].Reason != tc.reason {
					t.Fatal(s.dequeues)
				}
				s = Apply(s, Dequeued{PR: 2, AdmissionID: 2, Reason: tc.reason})
				a := plannedBuild(t, batchConfig(8), s, epoch)
				if !slices.Equal(batchNumbers(a.PRs), []int{1, 3, 4}) {
					t.Fatalf("recombined groups: %v", a.PRs)
				}
				s = Apply(s, PRClosed{PR: 6})
				if !slices.Equal(batchNumbers(s.pending[len(s.pending)-1].prs), []int{5, 7, 8}) {
					t.Fatal(s.pending)
				}
			})
		}
	}
	for _, abandon := range []bool{false, true} {
		t.Run(fmt.Sprintf("rebuild/abandon=%v", abandon), func(t *testing.T) {
			s := startPlanned(t, batchConfig(8), bisecting(t))
			old := s.Active.ID
			if abandon {
				s = Apply(s, TestRefBuilt{BatchID: old, TestedSHA: "passing", Method: Squash, At: epoch})
				s = Apply(s, ChecksCompleted{TestedSHA: "passing", Success: true})
				s = Apply(s, LandingStarted{BatchID: old, AttemptID: "attempt", FenceToken: s.Fence, At: epoch})
			}
			s = Apply(s, BaseMoved{SHA: "external"})
			if abandon {
				s = Apply(s, LandingAbandoned{BatchID: old, NewFence: "new-fence"})
			}
			a := plannedBuild(t, batchConfig(8), s, epoch)
			if a.BaseSHA != "external" || a.BatchID == old || !slices.Equal(batchNumbers(a.PRs), []int{1, 2, 3, 4}) {
				t.Fatal(a)
			}
			s = startPlanned(t, batchConfig(8), s)
			if !s.Active.FromBisection {
				t.Fatal("rebuild lost provenance")
			}
		})
	}
}

func TestBatchStartedValidation(t *testing.T) {
	valid := BatchStarted{BatchID: "b1", BaseSHA: "base1", PRs: []BatchPR{{PR: 1, HeadSHA: "head1"}}, At: epoch}
	for _, mutate := range []func(*BatchStarted){
		func(e *BatchStarted) { e.BatchID = "" }, func(e *BatchStarted) { e.BaseSHA = "" }, func(e *BatchStarted) { e.At = time.Time{} },
		func(e *BatchStarted) { e.PRs = nil }, func(e *BatchStarted) { e.PRs = []BatchPR{{PR: 0, HeadSHA: "head"}} },
		func(e *BatchStarted) { e.PRs = []BatchPR{{PR: 1}} }, func(e *BatchStarted) { e.PRs = append(e.PRs, e.PRs[0]) },
	} {
		e := valid
		mutate(&e)
		if e.Validate() == nil {
			t.Fatalf("accepted %+v", e)
		}
		if _, _, err := EncodeEvent(e); err == nil {
			t.Fatal("encoded invalid start")
		}
	}
	s := batchQueue(1)
	if Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "unrecorded", Method: Merge, At: epoch}).Active != nil {
		t.Fatal("build allocated unrecorded batch")
	}
	s = Apply(s, valid)
	if s.Active == nil {
		t.Fatal("valid start ignored")
	}
	valid.PRs[0].HeadSHA = "caller mutation"
	if s.Active.PRs[0].HeadSHA != "head1" {
		t.Fatal("event aliases projection")
	}
}

func TestBisectionFiltersInvalidMembersBeforeLateFailure(t *testing.T) {
	s := startPlanned(t, batchConfig(8), batchQueue(7))
	id := s.Active.ID
	s = Apply(s, TestRefBuilt{BatchID: id, TestedSHA: "late-failure", Method: Merge, At: epoch})
	s = Apply(s, PRClosed{PR: 2})
	s = Apply(s, ChecksCompleted{TestedSHA: "late-failure"})
	if len(s.pending) != 2 || !slices.Equal(batchNumbers(s.pending[0].prs), []int{1, 3}) || !slices.Equal(batchNumbers(s.pending[1].prs), []int{4, 5, 6, 7}) {
		t.Fatal("late failure restored an invalid member", s.pending)
	}
	for _, pr := range []int{1, 3} {
		s = Apply(s, DequeueRequested{PR: pr})
	}
	a := plannedBuild(t, batchConfig(8), s, epoch)
	if !slices.Equal(batchNumbers(a.PRs), []int{4, 5, 6, 7}) {
		t.Fatal("empty first group blocked second", a)
	}
}

func TestRecordedBatchFreezesCompositionAcrossConfigurationChanges(t *testing.T) {
	s := startPlanned(t, batchConfig(8), batchQueue(8))
	cfg := batchConfig(1)
	a := plannedBuild(t, cfg, s, epoch)
	if len(a.PRs) != 8 {
		t.Fatal("configuration changed recorded composition", a)
	}
}
