package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type round2RecoveryDriver struct {
	forge.Driver
	mode string
	runs []forge.CheckRun
}

func (d round2RecoveryDriver) CancelChecks(_ context.Context, _ forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	for i := range req.Runs {
		req.Runs[i].State = forge.Cancelled
	}
	switch d.mode {
	case "remaining":
		req.Runs = req.Runs[len(req.Runs)-1:]
	case "unsupported":
		return forge.CancelChecksResult{Confirmed: req.Runs[:1], Unsupported: "permission revoked"}, nil
	case "foreign":
		req.Runs[0].ID = 999
	}
	return forge.CancelChecksResult{Confirmed: req.Runs}, nil
}

func (d round2RecoveryDriver) CheckRuns(context.Context, forge.RepoRef, string) ([]forge.CheckRun, error) {
	return slices.Clone(d.runs), nil
}

func TestCancellationCrashPointsAlwaysSettleOrKeepRunnableSuccessor(t *testing.T) {
	for attempt := range queue.CancellationAttemptLimit {
		for _, crash := range []string{"before-report", "after-report", "after-deferral", "after-completion"} {
			for _, mode := range []string{"full", "remaining", "unsupported", "foreign"} {
				t.Run(fmt.Sprintf("attempt-%d/%s/%s", attempt, crash, mode), func(t *testing.T) {
					writer, url, q, cfg, cancel := round2CancellationFixture(t)
					var action queue.Action = cancel
					at := epoch
					for range attempt {
						deferred, _ := queue.DeferCheckCancellation(action, at, "pending")
						appendEvents(t, writer, q, deferred)
						state, _, err := writer.Load(t.Context(), q)
						must(t, err)
						at, _ = queue.NextWake(cfg, state, at)
						for _, a := range queue.Plan(cfg, state, at) {
							if _, ok := a.(queue.RetryCheckCancellation); ok {
								action = a
							}
						}
					}
					_, err := writer.Plan(t.Context(), q, cfg, at)
					must(t, err)
					claim := round2ClaimKey(t, writer, q, at, action.Key())
					x := executor.Executor{Driver: round2CompletedDriver{partial: true}, AllowNoRequiredChecks: true}
					if crash != "before-report" {
						err = x.Execute(t.Context(), claim.Action, writer.Emit(t.Context(), claim, cfg))
						if !errors.Is(err, executor.ErrCancellationPending) {
							t.Fatalf("partial report: %v", err)
						}
					}
					if crash == "after-deferral" || crash == "after-completion" {
						deferred, _ := queue.DeferCheckCancellation(claim.Action, at, "pending")
						must(t, writer.Emit(t.Context(), claim, cfg)(deferred))
					}
					if crash == "after-completion" {
						must(t, writer.Complete(t.Context(), claim, Done))
					}
					reader := openTestStore(t, url)
					driver := round2RecoveryDriver{mode: mode}
					for _, run := range cancel.Runs {
						driver.runs = append(driver.runs, forge.CheckRun{ID: run.ID, Attempt: run.Attempt, Name: run.Name, Source: run.Source, State: forge.Cancelled, HeadBranch: cancel.TestRef})
					}
					x.Driver = driver
					for step := 0; step <= 2*queue.CancellationAttemptLimit; step++ {
						at = at.Add(24 * time.Hour)
						_, err = reader.Plan(t.Context(), q, cfg, at)
						must(t, err)
						state, _, err := reader.Load(t.Context(), q)
						must(t, err)
						var pending queue.Action
						for _, a := range queue.Plan(cfg, state, at) {
							if _, ok := queue.DeferCheckCancellation(a, at, "probe"); !ok {
								continue
							}
							var status string
							must(t, reader.pool.QueryRow(t.Context(), "SELECT status FROM actions WHERE queue_id=$1 AND key=$2", q, a.Key()).Scan(&status))
							if status == "done" {
								t.Fatalf("pending effect stranded behind done row %s", a.Key())
							}
							pending = a
						}
						if pending == nil {
							return
						}
						claim = round2ClaimKey(t, reader, q, at, pending.Key())
						if err = x.Execute(t.Context(), claim.Action, reader.Emit(t.Context(), claim, cfg)); err != nil {
							deferred, ok := queue.DeferCheckCancellation(claim.Action, at, err.Error())
							if !ok {
								t.Fatal(err)
							}
							must(t, reader.Emit(t.Context(), claim, cfg)(deferred))
						}
						must(t, reader.Complete(t.Context(), claim, Done))
					}
					t.Fatal("cancellation exceeded both phase budgets")
				})
			}
		}
	}
}

type round2CompletedDriver struct {
	forge.Driver
	partial bool
}

func (d round2CompletedDriver) CancelChecks(_ context.Context, _ forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	for i := range req.Runs {
		req.Runs[i].State = forge.Cancelled
	}
	if d.partial {
		req.Runs = req.Runs[:1]
	}
	return forge.CancelChecksResult{Confirmed: req.Runs}, nil
}

func round2CancellationFixture(t *testing.T) (*Store, string, QueueID, queue.Config, queue.CancelChecks) {
	t.Helper()
	url := databaseURL(t)
	s := openTestStore(t, url)
	q := ensureQueue(t, s, "")
	cfg := config
	cfg.SpeculationMaxDepth = 4
	appendEvents(t, s, q, seedEvents()...)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	var build queue.BuildChainedTestRef
	for _, a := range queue.Plan(cfg, state, epoch) {
		if a, ok := a.(queue.BuildChainedTestRef); ok {
			build = a
		}
	}
	if build.BatchID == "" {
		t.Fatal("missing build")
	}
	appendEvents(t, s, q, chainStart(build), queue.ChainedTestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested", LandingSHA: "tested", ParentBatchID: build.ParentBatchID, Position: build.Position, Method: queue.Squash, At: epoch}, queue.ChecksObserved{BatchID: build.BatchID, TestedSHA: "tested", Runs: []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}, {ID: 8, Attempt: 1, Name: "optional", Source: "app:1", State: "pending"}}}, queue.PRClosed{PR: 1})
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	for _, a := range queue.Plan(cfg, state, epoch) {
		if a, ok := a.(queue.InvalidateBatches); ok {
			appendEvents(t, s, q, queue.BatchesInvalidated(a))
		}
	}
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	for _, a := range queue.Plan(cfg, state, epoch) {
		if a, ok := a.(queue.RecordCheckCancellation); ok {
			appendEvents(t, s, q, queue.CheckCancellationRequested(a))
			return s, url, q, cfg, queue.CancelChecks(a)
		}
	}
	t.Fatal("missing cancellation")
	return nil, "", 0, cfg, queue.CancelChecks{}
}

func round2ClaimKey(t *testing.T, s *Store, q QueueID, at time.Time, key string) ClaimedAction {
	t.Helper()
	claims, err := s.ClaimQueue(t.Context(), q, 100, time.Second, at)
	must(t, err)
	for _, c := range claims {
		if c.Key == key {
			return c
		}
		must(t, s.Complete(t.Context(), c, Done))
	}
	t.Fatalf("missing claim %s", key)
	return ClaimedAction{}
}

func TestRound2PartialConfirmationCrashStrandsEffect(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "successor"}[retry], func(t *testing.T) {
			writer, url, q, cfg, cancel := round2CancellationFixture(t)
			var action queue.Action = cancel
			at := epoch
			if retry {
				e, _ := queue.DeferCheckCancellation(action, at, "pending")
				appendEvents(t, writer, q, e)
				at = at.Add(30 * time.Second)
				state, _, err := writer.Load(t.Context(), q)
				must(t, err)
				for _, a := range queue.Plan(cfg, state, at) {
					if a, ok := a.(queue.RetryCheckCancellation); ok {
						action = a
					}
				}
			}
			_, err := writer.Plan(t.Context(), q, cfg, at)
			must(t, err)
			claim := round2ClaimKey(t, writer, q, at, action.Key())
			x := executor.Executor{Driver: round2CompletedDriver{partial: true}, AllowNoRequiredChecks: true}
			err = x.Execute(t.Context(), claim.Action, writer.Emit(t.Context(), claim, cfg))
			if !errors.Is(err, executor.ErrCancellationPending) {
				t.Fatalf("expected first partial confirmation: %v", err)
			}
			// Crash after the report committed, before deferral or Complete.
			reader := openTestStore(t, url)
			_, err = reader.Plan(t.Context(), q, cfg, at.Add(time.Second))
			must(t, err)
			claim = round2ClaimKey(t, reader, q, at.Add(time.Second), action.Key())
			x.Driver = round2CompletedDriver{}
			must(t, x.Execute(t.Context(), claim.Action, reader.Emit(t.Context(), claim, cfg)))
			must(t, reader.Complete(t.Context(), claim, Done))
			state, _, err := reader.Load(t.Context(), q)
			must(t, err)
			pending := false
			for _, a := range queue.Plan(cfg, state, at.Add(24*time.Hour)) {
				if a.Key() == action.Key() {
					pending = true
				}
			}
			_, err = reader.Plan(t.Context(), q, cfg, at.Add(24*time.Hour))
			must(t, err)
			claims, err := reader.ClaimQueue(t.Context(), q, 100, time.Minute, at.Add(24*time.Hour))
			must(t, err)
			var status string
			must(t, reader.pool.QueryRow(t.Context(), "SELECT status FROM actions WHERE queue_id=$1 AND key=$2", q, action.Key()).Scan(&status))
			if pending {
				t.Fatalf("fully confirmed cancellation remains in projection after restart: key=%s row=%s claimable=%d", action.Key(), status, len(claims))
			}
		})
	}
}

func TestRound2RetryConcurrentPlanningAndExhaustion(t *testing.T) {
	writer, url, q, cfg, cancel := round2CancellationFixture(t)
	reader := openTestStore(t, url)
	var action queue.Action = cancel
	at := epoch
	for attempt := range queue.CancellationAttemptLimit {
		errs := make(chan error, 2)
		for _, s := range []*Store{writer, reader} {
			go func() {
				_, err := s.Plan(t.Context(), q, cfg, at)
				errs <- err
			}()
		}
		must(t, <-errs)
		must(t, <-errs)
		if n := count(t, writer, "SELECT count(*) FROM actions WHERE queue_id=$1 AND key=$2", q, action.Key()); n != 1 {
			t.Fatalf("attempt %d has %d action rows", attempt, n)
		}
		claim := round2ClaimKey(t, writer, q, at, action.Key())
		otherClaims, err := reader.ClaimQueue(t.Context(), q, 100, time.Second, at)
		must(t, err)
		for _, c := range otherClaims {
			if c.Key == claim.Key {
				t.Fatal("both workers own the cancellation")
			}
			must(t, reader.Complete(t.Context(), c, Done))
		}
		deferred, _ := queue.DeferCheckCancellation(action, at, "pending")
		for _, s := range []*Store{writer, reader} {
			go func() { errs <- s.Emit(t.Context(), claim, cfg)(deferred) }()
		}
		must(t, <-errs)
		must(t, <-errs)
		must(t, writer.Complete(t.Context(), claim, Done))
		reader = openTestStore(t, url)
		state, _, err := reader.Load(t.Context(), q)
		must(t, err)
		if attempt == queue.CancellationAttemptLimit-1 {
			for _, a := range queue.Plan(cfg, state, at.Add(24*time.Hour)) {
				switch a.(type) {
				case queue.CancelChecks, queue.RetryCheckCancellation:
					t.Fatal("exhausted cancellation still planned")
				}
			}
			break
		}
		delay := min(30*time.Second*time.Duration(1<<attempt), 30*time.Minute)
		wake, ok := queue.NextWake(cfg, state, at)
		if !ok || !wake.Equal(at.Add(delay)) {
			t.Fatalf("unexpected replayed wake %v", wake)
		}
		_, err = reader.Plan(t.Context(), q, cfg, wake.Add(-time.Nanosecond))
		must(t, err)
		if n := count(t, reader, "SELECT count(*) FROM actions WHERE queue_id=$1 AND type='retry_check_cancellation'", q); n != attempt {
			t.Fatalf("attempt %d persisted a premature or duplicate successor: %d rows", attempt, n)
		}
		for _, a := range queue.Plan(cfg, state, wake) {
			if a, ok := a.(queue.RetryCheckCancellation); ok {
				action = a
			}
		}
		at = wake
	}
	if n := count(t, reader, "SELECT count(*) FROM actions WHERE queue_id=$1 AND type IN ('cancel_checks','retry_check_cancellation')", q); n != queue.CancellationAttemptLimit {
		t.Fatalf("expected exactly eight bounded action rows, got %d", n)
	}
}

func TestRound2DueSuccessorYieldsToNewWork(t *testing.T) {
	s, _, q, cfg, cancel := round2CancellationFixture(t)
	_, err := s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	claim := round2ClaimKey(t, s, q, epoch, cancel.Key())
	deferred, _ := queue.DeferCheckCancellation(claim.Action, epoch, "pending")
	must(t, s.Emit(t.Context(), claim, cfg)(deferred))
	must(t, s.Complete(t.Context(), claim, Done))
	at := epoch.Add(30 * time.Second)
	_, err = s.Plan(t.Context(), q, cfg, at)
	must(t, err)
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "new", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: at})
	_, err = s.Plan(t.Context(), q, cfg, at)
	must(t, err)
	claims, err := s.ClaimQueue(t.Context(), q, 1, time.Minute, at)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("missing due successor")
	}
	claim = claims[0]
	if _, ok := claim.Action.(queue.RetryCheckCancellation); !ok {
		t.Fatalf("expected older due successor, got %T", claim.Action)
	}
	deferred, _ = queue.DeferCheckCancellation(claim.Action, at, "still pending")
	must(t, s.Emit(t.Context(), claim, cfg)(deferred))
	must(t, s.Complete(t.Context(), claim, Done))
	_, err = s.Plan(t.Context(), q, cfg, at)
	must(t, err)
	claims, err = s.ClaimQueue(t.Context(), q, 1, time.Minute, at)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("new queue work was not claimable")
	}
	if _, ok := claims[0].Action.(queue.BuildChainedTestRef); !ok {
		t.Fatalf("due successor monopolized next claim: %T", claims[0].Action)
	}
}
