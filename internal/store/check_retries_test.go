package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func readyCheckRerun(t *testing.T, s *Store) (QueueID, ClaimedAction, queue.Config) {
	t.Helper()
	q := ensureQueue(t, s, "")
	cfg := config
	cfg.ChecksRetryFailed = 1
	cfg.CheckTimeout = time.Minute
	appendEvents(t, s, q,
		queue.BaseMoved{SHA: "base"},
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: epoch},
		queue.ChecksObserved{BatchID: "b1", TestedSHA: "tested", Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "failure"}}, Terminal: true, Success: false, OriginalDeadline: epoch.Add(time.Minute), Timeout: time.Minute})
	_, err := s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("claims=%+v", claims)
	}
	if _, ok := claims[0].Action.(queue.RecordCheckRerun); !ok {
		t.Fatalf("expected rerun request, got %T", claims[0].Action)
	}
	return q, claims[0], cfg
}

func TestCheckRerunRequestBothStartGates(t *testing.T) {
	for _, window := range []string{"before-begin", "before-append", "duplicate-request"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q, claim, cfg := readyCheckRerun(t, s)
			a := claim.Action.(queue.RecordCheckRerun)
			e := queue.CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, Timeout: a.Timeout, At: epoch, Deadline: epoch.Add(time.Minute)}
			if window == "before-begin" {
				appendEvents(t, s, q, queue.ChecksReopened{TestedSHA: "tested", At: epoch})
				proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
				must(t, err)
				if proceed {
					t.Fatal("stale request passed first gate")
				}
				return
			}
			proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("current request declined")
			}
			if window == "duplicate-request" {
				must(t, s.Emit(t.Context(), claim, cfg)(e))
			} else {
				appendEvents(t, s, q, queue.ChecksReopened{TestedSHA: "tested", At: epoch})
			}
			_, before, err := s.Load(t.Context(), q)
			must(t, err)
			err = s.Emit(t.Context(), claim, cfg)(e)
			if !errors.Is(err, ErrStale) {
				t.Fatalf("stale request append=%v", err)
			}
			state, after, err := s.Load(t.Context(), q)
			must(t, err)
			if after != before {
				t.Fatal("rejected request appended an event")
			}
			if window == "duplicate-request" && (!state.Active.CheckRetry.Requested || !state.Active.CheckRetry.Deadline.Equal(e.Deadline)) {
				t.Fatal("lost durable request")
			}
		})
	}
}

func TestCheckRetryLandingGatesRejectOldGeneration(t *testing.T) {
	for _, window := range []string{"before-begin", "before-append"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q, request, cfg := readyCheckRerun(t, s)
			a := request.Action.(queue.RecordCheckRerun)
			must(t, s.Complete(t.Context(), request, Done))
			appendEvents(t, s, q, queue.FenceInitialized{Token: "fence"})
			appendEvents(t, s, q,
				queue.CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, At: epoch, Deadline: epoch.Add(time.Minute), Timeout: time.Minute},
				queue.CheckRerunDispatched{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation},
				queue.ChecksObserved{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Runs: []queue.CheckRun{{ID: 2, Attempt: 1, Name: "ci", Source: "app:1", State: "success"}}, Terminal: true, Success: true, OriginalDeadline: epoch.Add(time.Minute), Timeout: time.Minute})
			_, err := s.Plan(t.Context(), q, cfg, epoch)
			must(t, err)
			claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if len(claims) != 1 {
				t.Fatalf("claims=%+v", claims)
			}
			claim := claims[0]
			land, ok := claim.Action.(queue.Land)
			if !ok {
				t.Fatalf("wanted Land, got %T", claim.Action)
			}
			reopenAndPass := func() {
				appendEvents(t, s, q,
					queue.ChecksReopened{TestedSHA: "tested", At: epoch},
					queue.ChecksObserved{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Runs: []queue.CheckRun{{ID: 2, Attempt: 1, Name: "ci", Source: "app:1", State: "success"}}, Terminal: true, Success: true, OriginalDeadline: epoch.Add(time.Minute), Timeout: time.Minute})
			}
			if window == "before-begin" {
				reopenAndPass()
				proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
				must(t, err)
				if proceed {
					t.Fatal("reopened generation passed BeginConsequential")
				}
				return
			}
			proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("current landing rejected")
			}
			reopenAndPass()
			err = s.Emit(t.Context(), claim, cfg)(queue.LandingStarted{BatchID: land.BatchID, AttemptID: "old", FenceToken: land.FenceToken, At: epoch})
			if !errors.Is(err, ErrStale) {
				t.Fatalf("old landing append: %v", err)
			}
		})
	}
}

func TestCheckRetryProgressFencesDuplicateAndExpiredClaims(t *testing.T) {
	for _, window := range []string{"duplicate-intent", "expired-claim"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q, request, cfg := readyCheckRerun(t, s)
			a := request.Action.(queue.RecordCheckRerun)
			must(t, s.Complete(t.Context(), request, Done))
			appendEvents(t, s, q, queue.CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, At: epoch, Deadline: epoch.Add(time.Minute), Timeout: time.Minute})
			_, err := s.Plan(t.Context(), q, cfg, epoch)
			must(t, err)
			claims, err := s.ClaimQueue(t.Context(), q, 1, time.Second, epoch)
			must(t, err)
			if len(claims) != 1 {
				t.Fatalf("claims=%+v", claims)
			}
			claim := claims[0]
			proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("dispatch declined")
			}
			intent := queue.CheckRerunDispatchProgress{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Target: queue.CheckRerunTarget{WorkflowRunID: 700, Attempt: 1, At: epoch}}
			must(t, s.Emit(t.Context(), claim, cfg)(intent))
			if window == "duplicate-intent" {
				if err := s.Emit(t.Context(), claim, cfg)(intent); !errors.Is(err, ErrStale) {
					t.Fatalf("duplicate intent=%v", err)
				}
				return
			}
			now := epoch.Add(2 * time.Second)
			claims, err = s.ClaimQueue(t.Context(), q, 1, time.Minute, now)
			must(t, err)
			if len(claims) != 1 {
				t.Fatalf("replacement claims=%+v", claims)
			}
			proceed, err = s.BeginConsequential(t.Context(), claims[0], cfg, now)
			must(t, err)
			if !proceed {
				t.Fatal("replacement dispatch declined")
			}
			accepted := intent
			accepted.Target.Accepted = true
			accepted.Target.At = now
			if err := s.Emit(t.Context(), claim, cfg)(accepted); !errors.Is(err, ErrClaimLost) {
				t.Fatalf("expired progress=%v", err)
			}
			must(t, s.Emit(t.Context(), claims[0], cfg)(accepted))
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			if len(state.Active.CheckRetry.Dispatches) != 1 || !state.Active.CheckRetry.Dispatches[0].Accepted {
				t.Fatalf("progress=%+v", state.Active.CheckRetry)
			}
		})
	}
}

func TestRefusedCheckRetryRequiresFreshFencedIntent(t *testing.T) {
	s := newStore(t)
	q, request, cfg := readyCheckRerun(t, s)
	a := request.Action.(queue.RecordCheckRerun)
	must(t, s.Complete(t.Context(), request, Done))
	appendEvents(t, s, q, queue.CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, At: epoch, Deadline: epoch.Add(time.Minute), Timeout: time.Minute})
	_, err := s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	claims, err := s.ClaimQueue(t.Context(), q, 1, time.Second, epoch)
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("claims=%+v", claims)
	}
	original := claims[0]
	proceed, err := s.BeginConsequential(t.Context(), original, cfg, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("dispatch declined")
	}
	progress := queue.CheckRerunDispatchProgress{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Target: queue.CheckRerunTarget{WorkflowRunID: 700, Attempt: 1, At: epoch}}
	must(t, s.Emit(t.Context(), original, cfg)(progress))
	progress.Target.NotDispatched = true
	must(t, s.Emit(t.Context(), original, cfg)(progress))
	// Restart/reclaim after the definite refusal. Only the new lease can consume
	// that proof and authorize another POST, after recording another intent.
	now := epoch.Add(2 * time.Second)
	claims, err = s.ClaimQueue(t.Context(), q, 1, time.Minute, now)
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("replacement=%+v", claims)
	}
	replacement := claims[0]
	proceed, err = s.BeginConsequential(t.Context(), replacement, cfg, now)
	must(t, err)
	if !proceed {
		t.Fatal("definite refusal could not resume")
	}
	progress.Target.NotDispatched = false
	progress.Target.At = now
	if err := s.Emit(t.Context(), original, cfg)(progress); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expired intent=%v", err)
	}
	must(t, s.Emit(t.Context(), replacement, cfg)(progress))
	progress.Target.Accepted = true
	must(t, s.Emit(t.Context(), replacement, cfg)(progress))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.Active.CheckRetry.Dispatches) != 1 || !state.Active.CheckRetry.Dispatches[0].Accepted || state.Active.CheckRetry.Dispatches[0].NotDispatched {
		t.Fatalf("progress=%+v", state.Active.CheckRetry)
	}
}
