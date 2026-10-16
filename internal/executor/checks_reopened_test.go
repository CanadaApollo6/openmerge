package executor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecordedSuccessIsRechecked(t *testing.T) {
	for _, operation := range []string{"refresh", "land"} {
		for _, mode := range []string{"pending", "missing", "ambiguous", "failed", "read-error"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				h := setup(t)
				s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
				checks := []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}}
				switch mode {
				case "pending":
					checks[0].State = forge.Pending
				case "missing":
					checks = checks[1:]
				case "ambiguous":
					checks = append(checks, forge.CheckRun{Name: "ci", Source: "other", State: forge.Success})
				case "failed":
					checks[0].State = forge.Failure
				case "read-error":
					h.f.FailNext("CheckRuns", errIO)
				}
				h.f.SetCheckRuns(repo, land.TestedSHA, checks)
				var action queue.Action = land
				if operation == "refresh" {
					action = queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
				}
				var events []queue.Event
				ids := 0
				h.x.NewID = func() (string, error) { ids++; return "attempt", nil }
				err := h.x.Execute(context.Background(), action, func(e queue.Event) error {
					events = append(events, e)
					s = queue.Apply(s, e)
					return nil
				})
				if ids != 0 || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence || s.Active.Phase != queue.Testing {
					t.Fatalf("unsuccessful preflight started landing: events=%+v ids=%d", events, ids)
				}
				if mode == "read-error" {
					if !errors.Is(err, errIO) || len(events) != 0 {
						t.Fatalf("read failure emitted events: %+v %v", events, err)
					}
					return
				}
				var ambiguity *executor.CheckSourceAmbiguityError
				if mode == "ambiguous" {
					if !errors.As(err, &ambiguity) {
						t.Fatalf("missing ambiguity: %v", err)
					}
				} else if operation == "land" {
					if !errors.Is(err, executor.ErrChecksNotSuccessful) {
						t.Fatalf("declined Land must remain retryable: %v", err)
					}
				} else {
					must(t, err)
				}
				var want queue.Event = queue.ChecksReopened{TestedSHA: land.TestedSHA, At: epoch}
				if mode == "failed" {
					want = queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: false}
				}
				wantEvents := []queue.Event{want}
				if operation == "refresh" || mode != "failed" {
					wantEvents = append(wantEvents, queue.ChecksRefreshed{BatchID: land.BatchID, TestedSHA: land.TestedSHA, At: epoch})
				}
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatalf("wrong check observation sequence: %+v, want %+v", events, wantEvents)
				}
				for _, a := range queue.Plan(queue.Config{MergeMethod: queue.Merge}, s, epoch) {
					if _, ok := a.(queue.Land); ok {
						t.Fatal("freshly observed checks still plan Land")
					}
				}
				if mode != "failed" {
					h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
					must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
					must(t, h.x.Execute(context.Background(), land, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
					if h.read(t, "main") != land.TestedSHA || s.Active != nil {
						t.Fatal("passing rerun could not land")
					}
				}
			})
		}
	}
}

func TestLandCheckRevocationMustBeDurable(t *testing.T) {
	h := setup(t)
	_, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	h.f.SetCheckRuns(repo, land.TestedSHA, nil)
	err := h.x.Execute(context.Background(), land, func(queue.Event) error { return errLog })
	if !errors.Is(err, errLog) || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence {
		t.Fatalf("failed revocation allowed landing: %v", err)
	}
}

func TestDelayedRefreshCannotTimeoutNewerPassingChecks(t *testing.T) {
	h := setup(t)
	s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute}
	deadline := s.Active.StartedTestingAt.Add(cfg.CheckTimeout)
	h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}})
	h.x.Now = func() time.Time { return epoch.Add(time.Second) }
	delayed := true
	a := queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
	must(t, h.x.Execute(context.Background(), a, func(e queue.Event) error {
		if _, ok := e.(queue.ChecksReopened); ok && delayed {
			delayed = false
			// The pending reader stalls before persistence; another reader passes.
			h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			newer := *h.x
			newer.Now = func() time.Time { return deadline.Add(time.Second) }
			must(t, newer.Execute(context.Background(), a, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
		}
		_, wire, err := queue.EncodeEvent(e)
		must(t, err)
		replayed, err := queue.DecodeEvent(wire)
		must(t, err)
		s = queue.Apply(s, replayed)
		return nil
	}))
	now := deadline.Add(2 * time.Second)
	actions := queue.Plan(cfg, s, now)
	if len(actions) != 1 {
		t.Fatalf("expected another read, got %+v", actions)
	}
	refresh, ok := actions[0].(queue.RefreshChecks)
	if !ok {
		t.Fatalf("delayed pending must request another read: %T", actions[0])
	}
	h.x.Now = func() time.Time { return now }
	must(t, h.x.Execute(context.Background(), refresh, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
	actions = queue.Plan(cfg, s, now)
	if len(actions) != 1 {
		t.Fatalf("expected landing after fresh pass: %+v", actions)
	}
	nextLand, ok := actions[0].(queue.Land)
	if !ok {
		t.Fatalf("fresh passing read did not enable landing: %T", actions[0])
	}
	must(t, h.x.Execute(context.Background(), nextLand, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
	if h.read(t, "main") != land.TestedSHA || s.Active != nil {
		t.Fatal("fresh passing checks did not land")
	}
}

// These are the reviewer's two worker-clock reproductions, with events replayed
// through the wire format before they enter the projection.
func TestFastPendingCannotSuppressLaterPass(t *testing.T) {
	h := setup(t)
	s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute}
	deadline := s.Active.StartedTestingAt.Add(cfg.CheckTimeout)
	fast, slow := *h.x, *h.x
	fast.Now = func() time.Time { return deadline.Add(2 * time.Second) }
	slow.Now = func() time.Time { return deadline.Add(time.Second) }
	refresh := queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
	emit := replayChecks(t, &s)
	h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}})
	must(t, fast.Execute(context.Background(), refresh, emit))
	h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
	must(t, slow.Execute(context.Background(), refresh, emit))
	actions := queue.Plan(cfg, s, deadline.Add(3*time.Second))
	if len(actions) != 1 {
		t.Fatalf("expected Land, got %+v", actions)
	}
	if _, ok := actions[0].(queue.Land); !ok {
		t.Fatalf("fast pending suppressed later pass: %T", actions[0])
	}
	must(t, slow.Execute(t.Context(), actions[0], emit))
	if h.read(t, "main") != land.TestedSHA || s.Active != nil {
		t.Fatal("post-deadline passing refresh and preflight did not land")
	}
}

func TestFastPassCannotHideReopenBeforeLandingStarted(t *testing.T) {
	h := setup(t)
	s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	cfg := queue.Config{MergeMethod: queue.Merge}
	fast, slow := *h.x, *h.x
	fast.Now = func() time.Time { return epoch.Add(5 * time.Second) }
	slow.Now = func() time.Time { return epoch.Add(time.Second) }
	refresh := queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
	emit := replayChecks(t, &s)
	must(t, fast.Execute(context.Background(), refresh, emit))
	h.x.NewID = func() (string, error) {
		// Land's preflight has passed, but the reopen commits before its start.
		h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}})
		must(t, slow.Execute(context.Background(), refresh, emit))
		return "durable-reopen", nil
	}
	blocked := false
	err := h.x.Execute(context.Background(), land, func(e queue.Event) error {
		if _, ok := e.(queue.LandingStarted); ok && !queue.CanStart(cfg, s, land, e) {
			blocked = true
			return errLog
		}
		return emit(e)
	})
	if !blocked || !errors.Is(err, errLog) || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence || s.Active.Phase != queue.Testing {
		t.Fatalf("durable reopen failed to block landing: blocked=%v err=%v state=%+v", blocked, err, s.Active)
	}
}

func replayChecks(t *testing.T, s *queue.State) func(queue.Event) error {
	t.Helper()
	return func(e queue.Event) error {
		_, wire, err := queue.EncodeEvent(e)
		if err != nil {
			return err
		}
		decoded, err := queue.DecodeEvent(wire)
		if err != nil {
			return err
		}
		*s = queue.Apply(*s, decoded)
		return nil
	}
}

func TestDelayedPassCannotReviveInvalidatedLand(t *testing.T) {
	h := setup(t)
	s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	cfg := queue.Config{MergeMethod: queue.Merge}
	emit := replayChecks(t, &s)
	refresh := queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
	landNow := epoch.Add(time.Second)
	h.x.Now = func() time.Time { return landNow }
	oldReader := *h.x
	oldReader.Now = func() time.Time { return epoch }
	pendingReader := *h.x
	pendingReader.Now = func() time.Time { return epoch.Add(2 * time.Second) }
	blocked := false
	var landErr error
	// The old reader has read success but stalls before committing its completion.
	must(t, oldReader.Execute(t.Context(), refresh, func(old queue.Event) error {
		if _, ok := old.(queue.ChecksCompleted); !ok {
			return emit(old)
		}
		h.x.NewID = func() (string, error) {
			// The landing preflight has read success. CI now reopens and is persisted.
			h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}})
			must(t, pendingReader.Execute(t.Context(), refresh, emit))
			start := queue.LandingStarted{BatchID: land.BatchID, AttemptID: "round5", FenceToken: land.FenceToken, At: epoch}
			if queue.CanStart(cfg, s, land, start) {
				t.Fatal("fixture: reopen did not initially revoke start")
			}
			// The stalled older pass now commits, without another CI read.
			must(t, emit(old))
			landNow = epoch.Add(3 * time.Second)
			return "round5", nil
		}
		landErr = h.x.Execute(t.Context(), land, func(e queue.Event) error {
			if _, ok := e.(queue.LandingStarted); ok && !queue.CanStart(cfg, s, land, e) {
				blocked = true
				return errLog
			}
			return emit(e)
		})
		return nil
	}))
	if !blocked || !errors.Is(landErr, errLog) || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence || s.Active.Phase != queue.Testing {
		t.Fatalf("delayed pass revived invalidated landing: blocked=%v err=%v state=%+v", blocked, landErr, s.Active)
	}
}

func TestFlappingLandPreflightTimesOut(t *testing.T) {
	for _, offset := range []time.Duration{0, 2 * time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			h := setup(t)
			s, initialLand := reviewReady(t, h, h.base, h.fence, h.prs[0])
			cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute}
			started := s.Active.StartedTestingAt
			deadline := started.Add(cfg.CheckTimeout)
			now := started.Add(offset)
			h.x.Now = func() time.Time { return now }
			emit := replayChecks(t, &s)
			refresh := queue.RefreshChecks{BatchID: initialLand.BatchID, TestedSHA: initialLand.TestedSHA}
			for range 100 {
				now = now.Add(time.Second)
				h.f.SetCheckRuns(repo, initialLand.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
				must(t, h.x.Execute(t.Context(), refresh, emit))
				actions := queue.Plan(cfg, s, now)
				if len(actions) != 1 {
					t.Fatalf("passing refresh did not plan Land: %+v", actions)
				}
				land, ok := actions[0].(queue.Land)
				if !ok {
					t.Fatalf("passing refresh planned %T", actions[0])
				}
				h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}})
				if err := h.x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
					t.Fatalf("pending preflight result: %v", err)
				}
				if !s.Active.StartedTestingAt.Equal(started) {
					t.Fatal("reopen moved deadline")
				}
				// Even a pending read exactly at the deadline qualifies once time advances.
				actions = queue.Plan(cfg, s, now.Add(time.Nanosecond))
				if !now.Before(deadline) {
					found := false
					for _, a := range actions {
						if d, ok := a.(queue.Dequeue); ok && d.Reason == queue.ChecksTimeout {
							found = true
							must(t, h.x.Execute(t.Context(), d, emit))
						}
					}
					if !found || len(s.Entries) != 0 || s.Active != nil {
						t.Fatalf("post-deadline preflight did not terminate batch: %+v", actions)
					}
					if h.read(t, "main") != h.base {
						t.Fatal("pending CI landed")
					}
					return
				}
			}
			t.Fatal("100 passing-refresh/pending-preflight flips escaped timeout")
		})
	}
}

func TestLandPreflightRefreshMustBeDurable(t *testing.T) {
	h := setup(t)
	s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	h.f.SetCheckRuns(repo, land.TestedSHA, nil)
	emit := replayChecks(t, &s)
	err := h.x.Execute(t.Context(), land, func(e queue.Event) error {
		if _, ok := e.(queue.ChecksRefreshed); ok {
			return errLog
		}
		return emit(e)
	})
	if !errors.Is(err, errLog) {
		t.Fatalf("refresh persistence failure lost: %v", err)
	}
	actions := queue.Plan(queue.Config{MergeMethod: queue.Merge}, s, epoch)
	if len(actions) != 1 {
		t.Fatalf("missing recovery read: %+v", actions)
	}
	if _, ok := actions[0].(queue.RefreshChecks); !ok {
		t.Fatalf("failed refresh persisted timeout evidence: %T", actions[0])
	}
}
