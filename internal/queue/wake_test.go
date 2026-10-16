package queue

import (
	"testing"
	"time"
)

func TestNextWake(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := Config{CheckTimeout: 2 * time.Minute, ResolveAfter: 5 * time.Second, BuildBackoff: time.Second}
	for _, scenario := range []struct {
		name  string
		state State
		want  time.Duration
	}{
		{name: "empty"},
		{name: "ready build", state: State{Active: &Batch{Phase: Building}}},
		{name: "build backoff", state: State{Active: &Batch{Phase: Building}, buildRetry: buildRetryState{failures: 2, failedAt: now}}, want: 2 * time.Second},
		{name: "paused build", state: State{Active: &Batch{Phase: Building}, buildRetry: buildRetryState{failures: 3, failedAt: now}}},
		{name: "landing resolution", state: State{Active: &Batch{Phase: Landing, LandingAttempt: LandingAttempt{StartedAt: now}}}, want: 5*time.Second + time.Nanosecond},
		{name: "checks first refresh ready", state: State{Active: &Batch{Phase: Testing, StartedTestingAt: now}}, want: 2*time.Minute + time.Nanosecond},
		{name: "default refresh interval", state: State{Active: &Batch{Phase: Testing, StartedTestingAt: now, checkRefreshes: 1, checksRefreshedAt: now}}, want: time.Minute},
		{name: "timeout before refresh", state: State{Active: &Batch{Phase: Testing, StartedTestingAt: now.Add(-90 * time.Second), checkRefreshes: 1, checksRefreshedAt: now}}, want: 30*time.Second + time.Nanosecond},
		{name: "passed checks ready to land", state: State{Active: &Batch{Phase: Testing, StartedTestingAt: now, checksPassed: true}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			at, ok := NextWake(cfg, scenario.state, now)
			if scenario.want == 0 {
				if ok {
					t.Fatalf("unexpected timer %s", at)
				}
				return
			}
			if !ok || at.Sub(now) != scenario.want {
				t.Fatalf("wake=%s, %v; want now+%s", at, ok, scenario.want)
			}
		})
	}
}
