package capacity

import (
	"reflect"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Seed: 42, Changes: 32, CI: time.Minute, CheckTimeout: 90 * time.Second, MaxSize: 8, MinDepth: 1, MaxDepth: 4, Runners: 50, Cancellation: true, Retries: true, Target: .8}
}
func mustRun(t *testing.T, c Config) Result {
	t.Helper()
	r, err := Run(c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWindowAndAllOpenBatches(t *testing.T) {
	c := testConfig()
	c.Changes = 64
	r := mustRun(t, c)
	if r.MaxOpen != 4 || r.FinalWindow != 4 || r.Runs != 8 || r.Landings != 8 || len(r.Merges) != 64 || r.MaxRunning != 4 {
		t.Fatalf("unexpected result: %+v", r)
	}
	// Four passed batches occupy the window until landed. Shrink does not cancel
	// admitted work; only confirmation invalidates a suffix.
	m := &model{cfg: c, window: 4, chain: []*batch{{state: passed}, {state: passed}, {state: passed}, {state: passed}}}
	m.window = 1
	m.admit()
	if len(m.chain) != 4 {
		t.Fatal("shrinking window discarded open batches")
	}
}
func TestOriginalFailureHalvesOnlyOnce(t *testing.T) {
	c := testConfig()
	c.Changes = 8
	c.BrokenRate = 1
	r := mustRun(t, c)
	if r.OriginalFailures != 1 || r.BisectionFailures != 14 || r.FinalWindow != 2 || r.Dequeued != 8 || r.Runs != 41 || r.Reruns != 15 {
		t.Fatalf("bisection/window mismatch: %+v", r)
	}
}
func TestProvisionalFailureDoesNotRetryOrBlame(t *testing.T) {
	c := testConfig()
	m := &model{cfg: c, window: 4, ci: nil}
	m.chain = []*batch{{state: runningChecks}, {state: provisional}}
	m.resolve()
	if m.chain[1].state != provisional || m.result.Reruns != 0 || m.result.Dequeued != 0 {
		t.Fatal("later failure resolved before oldest")
	}
}
func TestSuffixReturnsInOrderAndHalvesBeforeFresh(t *testing.T) {
	c := testConfig()
	m := &model{cfg: c, window: 4}
	m.chain = []*batch{{members: []int{0, 1, 2, 3}, state: confirmed}, {members: []int{4, 5}, half: true}, {members: []int{6, 7}}}
	m.halves = [][]int{{8, 9}}
	m.waiting = []int{10, 11}
	m.resolve()
	if !reflect.DeepEqual(m.halves, [][]int{{0, 1}, {2, 3}, {4, 5}, {8, 9}}) || !reflect.DeepEqual(m.waiting, []int{6, 7, 10, 11}) || m.window != 2 {
		t.Fatalf("order lost: halves %v, waiting %v W%d", m.halves, m.waiting, m.window)
	}
}
func TestRunnerQueueAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		c := testConfig()
		c.Runners = 2
		c.ArrivalWindow = 5 * time.Minute
		c.MaxWait = 30 * time.Second
		c.CheckTimeout = 0
		c.BrokenRate = .2
		c.Cancellation = cancel
		r := mustRun(t, c)
		if r.MaxRunning != 2 || r.CancelRequested == 0 || r.Dequeued != r.Broken || len(r.Merges)+r.Dequeued != c.Changes {
			t.Fatalf("runner accounting: %+v", r)
		}
		if cancel && r.CancelConfirmed == 0 || !cancel && r.CancelUnsupported == 0 {
			t.Fatalf("cancellation accounting: %+v", r)
		}
		if !cancel && r.RunnerMinutes != float64(r.Runs) {
			t.Fatal("unsupported cancellation lost service cost")
		}
		if !reflect.DeepEqual(r, mustRun(t, c)) {
			t.Fatal("nondeterministic run")
		}
	}
}
func TestTimeoutsAndLongCI(t *testing.T) {
	c := testConfig()
	c.CI = 3 * time.Hour
	c.CheckTimeout = c.CI * 3 / 2
	r := mustRun(t, c)
	if r.Timeouts != 0 || len(r.Merges) != c.Changes {
		t.Fatalf("long CI killed: %+v", r)
	}
	c.CheckTimeout = time.Hour
	c.Changes = 8
	r = mustRun(t, c)
	if r.OriginalFailures != 1 || r.BisectionFailures != 14 || r.FinalWindow != 2 || r.Dequeued != 8 || r.Reruns != 0 {
		t.Fatalf("timeout semantics: %+v", r)
	}
	c = testConfig()
	c.CheckTimeout = c.CI
	r = mustRun(t, c)
	if r.Timeouts != 0 {
		t.Fatal("equal-time CI completion lost to timeout")
	}
}
func TestAdaptiveEstimator(t *testing.T) {
	history := make([]bool, 200)
	for i := 0; i < 10; i++ {
		history[i] = true
	}
	if AdaptiveCap(history[:19], 8, .8) != 8 || AdaptiveCap(history, 8, .8) != 4 || AdaptiveCap(append([]bool{true, true}, history...), 8, .8) != 4 {
		t.Fatal("estimator cold start, cap, or ring incorrect")
	}
	if AdaptiveCap(make([]bool, 200), 8, .8) != 8 || AdaptiveCap([]bool{true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true}, 8, .8) != 1 {
		t.Fatal("estimator endpoints")
	}
}
func TestIsolationRetestsPassers(t *testing.T) {
	c := testConfig()
	c.Isolation = true
	c.BrokenRate = .2
	r := mustRun(t, c)
	if r.Dequeued != r.Broken || len(r.Merges) != c.Changes-r.Broken || r.BisectionFailures != 0 || r.Runs <= r.Landings {
		t.Fatalf("isolation lost or directly landed members: %+v", r)
	}
}
func TestInteractingChanges(t *testing.T) {
	c := testConfig()
	c.InteractionRate = 1
	c.Changes = 8
	r := mustRun(t, c)
	if r.Broken != 0 || r.Dequeued != 4 || len(r.Merges) != 4 {
		t.Fatalf("pair should fail only together: %+v", r)
	}
}
func TestFiniteQueueTimeout(t *testing.T) {
	c := testConfig()
	c.Runners = 1
	r := mustRun(t, c)
	if r.Timeouts == 0 || r.MaxRunning != 1 {
		t.Fatal("runner wait not included in deadline")
	}
}
func TestMaxWait(t *testing.T) {
	c := testConfig()
	c.Changes = 1
	c.MaxWait = 5 * time.Minute
	r := mustRun(t, c)
	if r.Merges[0].TTM != 6*time.Minute {
		t.Fatal("partial batch did not wait")
	}
	c.MaxSize = 1
	r = mustRun(t, c)
	if r.Merges[0].TTM != time.Minute {
		t.Fatal("singleton cap waited")
	}
}
func TestInvalidConfig(t *testing.T) {
	c := testConfig()
	c.Runners = 0
	if _, err := Run(c); err == nil {
		t.Fatal("accepted zero runners")
	}
}

func TestHalfLandingGrowsAndMinimumDepth(t *testing.T) {
	c := testConfig()
	m := &model{cfg: c, window: 1, chain: []*batch{{half: true, state: passed}}}
	m.resolve()
	if m.window != 2 || m.result.Landings != 1 {
		t.Fatal("half landing did not grow W")
	}
	c.MinDepth = 3
	c.BrokenRate = 1
	c.Changes = 8
	r := mustRun(t, c)
	if r.FinalWindow != 3 {
		t.Fatal("failure shrank below min_depth")
	}
}
func TestRerunConsumesFullService(t *testing.T) {
	c := testConfig()
	c.Changes = 1
	c.BrokenRate = 1
	r := mustRun(t, c)
	if r.Runs != 2 || r.Reruns != 1 || r.Span != 2*c.CI || r.RunnerMinutes != 2*c.CI.Minutes() {
		t.Fatalf("rerun skipped service: %+v", r)
	}
}
func TestQueuedCompletionAtDeadlineWins(t *testing.T) {
	c := testConfig()
	c.Runners = 1
	c.Changes = 16
	c.CheckTimeout = 2 * c.CI
	r := mustRun(t, c)
	if r.Timeouts != 0 || len(r.Merges) != 16 {
		t.Fatalf("queued run finishing at deadline was killed: %+v", r)
	}
}
