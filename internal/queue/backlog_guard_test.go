package queue

import (
	"fmt"
	"testing"
	"time"
)

func TestBacklogGuardSizeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		adaptive, maxSize, waiting, want int
	}{
		{4, 8, 0, 4},   // empty queue: adaptive size stands
		{4, 8, 4, 4},   // backlog the adaptive batch absorbs
		{4, 8, 5, 5},   // one more: grow to the backlog
		{4, 8, 8, 8},   // exactly a full batch
		{4, 8, 30, 8},  // never above max_size
		{8, 8, 100, 8}, // adaptive already at max
		{1, 0, 10, 1},  // zero max_size means one
	} {
		if got := BacklogGuardSize(tc.adaptive, tc.maxSize, tc.waiting); got != tc.want {
			t.Errorf("BacklogGuardSize(%d, %d, %d) = %d, want %d", tc.adaptive, tc.maxSize, tc.waiting, got, tc.want)
		}
	}
}

// guardState is a queue with waiting changes and an outcome history of one
// failure in 20, so a 0.8 target sizes fresh batches at 4 of a maximum of 8.
func guardState(t *testing.T, depth, waiting int) (Config, State, time.Time) {
	t.Helper()
	cfg, s, now := adaptiveState(t, depth, waiting)
	cfg.BatchMaxSize, cfg.BatchMaxWait, cfg.BatchTargetPassRate = 8, time.Minute, .8
	for i := range 20 {
		s.outcomes = append(s.outcomes, changeOutcome{Revision: uint64(i + 1), AdmissionID: uint64(1000 + i), PR: 1000 + i, Failed: i == 0})
	}
	if s.BatchSize(cfg) != 4 {
		t.Fatalf("adaptive size %d, want 4", s.BatchSize(cfg))
	}
	return cfg, s, now
}

func freshBatch(cfg Config, s State, now time.Time) []BatchPR {
	for _, action := range Plan(cfg, s, now) {
		switch a := action.(type) {
		case BuildTestRef:
			return a.PRs
		case BuildChainedTestRef:
			return a.PRs
		}
	}
	return nil
}

func TestBacklogGuardFloorsFreshBatches(t *testing.T) {
	for _, tc := range []struct {
		depth, waiting            int
		guarded, unguarded, fixed int
	}{
		{1, 3, 0, 0, 0}, // below the adaptive size: all wait on max_wait alike
		{1, 4, 4, 4, 0},
		{1, 6, 6, 4, 0},
		{1, 8, 8, 4, 8},
		{1, 12, 8, 4, 8},
		// At a full depth-4 window the guard does not apply.
		{4, 16, 4, 4, 8},
		{4, 17, 4, 4, 8},
		{4, 40, 4, 4, 8},
	} {
		t.Run(fmt.Sprintf("depth%d/waiting%d", tc.depth, tc.waiting), func(t *testing.T) {
			cfg, s, now := guardState(t, tc.depth, tc.waiting)
			if got := len(freshBatch(cfg, s, now)); got != tc.guarded {
				t.Errorf("guarded batch %d, want %d", got, tc.guarded)
			}
			off := cfg
			off.BatchBacklogGuardOff = true
			if got := len(freshBatch(off, s, now)); got != tc.unguarded {
				t.Errorf("unguarded batch %d, want %d", got, tc.unguarded)
			}
			fixed := cfg
			fixed.BatchTargetPassRate = 0
			if got := len(freshBatch(fixed, s, now)); got != tc.fixed {
				t.Errorf("fixed batch %d, want %d", got, tc.fixed)
			}
			// The guard never adds a max_wait deadline the adaptive size lacked.
			_, guardedWake := s.batchCandidates(cfg, now)
			_, unguardedWake := s.batchCandidates(off, now)
			if !guardedWake.Equal(unguardedWake) {
				t.Errorf("guard changed the formation deadline: %s vs %s", guardedWake, unguardedWake)
			}
		})
	}
}

// Only changes that may join a fresh batch count toward the backlog: untrusted
// or otherwise unrunnable entries do not inflate it.
func TestBacklogGuardCountsOnlyFreshCandidates(t *testing.T) {
	cfg, s, now := guardState(t, 1, 8)
	for _, pr := range []int{2, 4, 6} {
		s = Apply(s, PRObserved{PR: pr, HeadSHA: fmt.Sprint("h", pr), Eligible: true, Trusted: false})
	}
	got := freshBatch(cfg, s, now)
	if len(got) != 5 {
		t.Fatalf("batch %v, want the 5 runnable changes", got)
	}
	for _, pr := range got {
		if pr.PR%2 == 0 && pr.PR <= 6 {
			t.Fatalf("untrusted PR %d batched", pr.PR)
		}
	}
}

// The guard applies only at an admission window of 1: at depth 4 it stays off
// while the window is 4 or 2 and takes the backlog once failures halve it to 1.
func TestBacklogGuardOnlyAtWindowOne(t *testing.T) {
	cfg, s, now := guardState(t, 4, 18)
	for _, tc := range []struct{ window, want int }{{4, 4}, {2, 4}, {1, 8}} {
		s.window.Current = tc.window
		if s.Window(cfg) != tc.window {
			t.Fatalf("window %d", s.Window(cfg))
		}
		if got := len(freshBatch(cfg, s, now)); got != tc.want {
			t.Errorf("window %d batch %d, want %d", tc.window, got, tc.want)
		}
		off := cfg
		off.BatchBacklogGuardOff = true
		if got := len(freshBatch(off, s, now)); got != 4 {
			t.Errorf("window %d unguarded batch %d, want the adaptive 4", tc.window, got)
		}
	}
}
