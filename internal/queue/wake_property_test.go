package queue

import (
	"testing"
	"time"
)

func TestNextWakeMatchesPlanAcrossTimers(t *testing.T) {
	start := time.Unix(1000, 0)
	checked := 0
	for _, phase := range []Phase{Building, Testing, Landing} {
		for _, timeout := range []time.Duration{0, time.Nanosecond, 30 * time.Second, 2 * time.Minute} {
			for _, resolve := range []time.Duration{-time.Nanosecond, 0, 5 * time.Second} {
				for _, interval := range []time.Duration{0, time.Nanosecond, time.Minute} {
					for _, failures := range []int{0, 1, 2, 3} {
						for _, backoff := range []time.Duration{0, time.Second} {
							for _, elapsed := range []time.Duration{0, time.Nanosecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute} {
								cfg := Config{MergeMethod: Merge, CheckTimeout: timeout, ResolveAfter: resolve, RefreshInterval: interval, BuildBackoff: backoff}
								s := testingState()
								s.Active.Phase = phase
								s.Active.StartedTestingAt = start
								s.Active.LandingAttempt = LandingAttempt{ID: "attempt", FenceToken: s.Fence, StartedAt: start}
								s.Active.checkRefreshes = 1
								s.Active.checksRefreshedAt = start
								s.buildRetry = buildRetryState{failures: failures, failedAt: start, revision: s.revision}
								now := start.Add(elapsed)
								if len(Plan(cfg, s, now)) != 0 {
									continue
								}
								at, ok := NextWake(cfg, s, now)
								if ok {
									if !at.After(now) || len(Plan(cfg, s, at)) == 0 || len(Plan(cfg, s, at.Add(-time.Nanosecond))) != 0 {
										t.Fatalf("wake mismatch phase=%s cfg=%+v elapsed=%s at=%s", phase, cfg, elapsed, at.Sub(start))
									}
								} else if len(Plan(cfg, s, start.Add(24*time.Hour))) != 0 {
									t.Fatalf("missing wake phase=%s cfg=%+v elapsed=%s", phase, cfg, elapsed)
								}
								checked++
							}
						}
					}
				}
			}
		}
	}
	t.Logf("checked %d drained planner states", checked)
}
