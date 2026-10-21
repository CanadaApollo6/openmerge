package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAuthorityAdmissionNoApprovalsChains(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, reads := authorityWorkerFixture(t)
				if depth > 1 {
					_, err := f.PushBase(repo, "main", "speculation", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig) + "    speculation: {min_depth: 4, max_depth: 4}\n"})
					must(t, err)
				}
				must(t, s.reconcile(t.Context(), w, true))
				base := lifecycleState(t, s, w).BaseSHA
				for n := 1; n <= depth; n++ {
					if n > 1 {
						_, err := f.OpenPR(repo, n, base, true)
						must(t, err)
						must(t, f.SetPRMetadata(repo, n, "main", false, "contributor"))
					}
					f.SetNativeReviews(repo, n, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(int64(100+n), "@openmerge queue")}})
				}
				must(t, s.reconcile(t.Context(), w, true))
				cfg := w.plannerConfig(s.options.PollInterval)
				x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Speculative: depth > 1, BuildMetadataKey: w.Key, BuildPolicyDigest: w.stageA.policy.Digest}
				x.BuildMetadata = func(_ queue.State, key executor.BuildChangeKey, b queue.Batch, digest string) ([]*forge.ChangeMetadata, error) {
					current := lifecycleState(t, s, w)
					return executor.LoadBuildMetadataWithAncestry(current, key, *current.Batch(b.ID), digest, func(a, b string) (bool, error) { return f.IsAncestor(t.Context(), repo, a, b) })
				}
				for n := 1; n <= depth; n++ {
					build := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool {
						switch a.(type) {
						case queue.BuildTestRef, queue.BuildChainedTestRef:
							return true
						}
						return false
					})
					proceed, err := s.store.BeginConsequential(t.Context(), build, cfg, time.Now())
					must(t, err)
					if !proceed {
						t.Fatal("build did not begin")
					}
					must(t, x.Execute(t.Context(), build.Action, s.store.EmitWithPolicy(t.Context(), build, cfg, w.stageA.policy)))
					must(t, s.store.Complete(t.Context(), build, store.Done))
					batch := lifecycleState(t, s, w).BatchForPR(n)
					if batch == nil {
						t.Fatal("missing speculative batch", n)
					}
					f.SetCheckRuns(repo, batch.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: uint64(700 + n), Attempt: 1, State: forge.Success, HeadBranch: batch.TestRef}})
					_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
					must(t, err)
				}
				if w.stageA.policy.Approvals != nil {
					t.Fatal("fixture invented approvals")
				}
				for n := 1; n <= depth; n++ {
					// Refresh the head's eligibility using the existing trust/reconciliation
					// path, without rebuilding the already tested speculative suffix.
					must(t, s.trustGate(t.Context(), w, lifecycleState(t, s, w).BatchForPR(n).PRs))
					cfg = w.plannerConfig(s.options.PollInterval)
					for _, a := range queue.Plan(cfg, lifecycleState(t, s, w), time.Now()) {
						if refresh, ok := a.(queue.RefreshChecks); ok {
							claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { r, ok := a.(queue.RefreshChecks); return ok && r.BatchID == refresh.BatchID })
							must(t, s.execute(t.Context(), w, claim))
						}
					}
					claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
					proceed, err := s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
					must(t, err)
					if !proceed {
						t.Fatal("landing did not begin")
					}
					basis := lifecycleState(t, s, w)
					beforeReads := *reads
					saved := w.authorityObservation.Read
					if n == 1 {
						resolver := w.resolveAuthorityLifecycle
						w.resolveAuthorityLifecycle = nil
						x.LandingDecisions = s.stageAGate(t.Context(), w, claim, cfg, basis)
						err = x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy))
						if err == nil || !strings.Contains(err.Error(), "opt-in/configuration unavailable") {
							t.Fatalf("missing resolver bypassed executor admission: %v", err)
						}
						w.resolveAuthorityLifecycle = resolver
						for _, failure := range []error{errors.New("permission denied"), &admissionQuota{until: time.Now().Add(time.Minute)}} {
							w.authorityObservation.Read = func(context.Context) (authority.Observation, error) {
								return authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}, failure
							}
							x.LandingDecisions = s.stageAGate(t.Context(), w, claim, cfg, basis)
							err = x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy))
							if !errors.Is(err, failure) || !strings.Contains(err.Error(), "authority admission read failed") {
								t.Fatalf("read failure did not close admission: %v", err)
							}
							var quota *admissionQuota
							if errors.As(failure, &quota) {
								if !w.authorityNextCheck.Equal(quota.until) {
									t.Fatal("admission ignored quota retry deadline", w.authorityNextCheck, quota.until)
								}
							} else if !w.authorityNextCheck.Equal(time.Now().Add(time.Minute)) {
								t.Fatal("admission did not schedule failure retry", w.authorityNextCheck)
							}
							if lifecycleState(t, s, w).Active.Phase == queue.Landing {
								t.Fatal("failed read admitted start")
							}
						}
						w.authorityObservation.Read = saved
					}
					x.LandingDecisions = s.stageAGate(t.Context(), w, claim, cfg, basis)
					must(t, x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy)))
					if w.authorityFailures != 0 || !w.authorityNextCheck.Equal(time.Now().Add(s.options.RulesInterval)) {
						t.Fatal("admitted fresh read did not reset observation cadence")
					}
					if *reads != beforeReads+1 {
						t.Fatal("start skipped fresh authority read", *reads, beforeReads)
					}
					must(t, s.store.Complete(t.Context(), claim, store.Done))
				}
				rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
				must(t, err)
				starts := 0
				for i, row := range rows {
					if _, ok := row.Event.(queue.LandingStarted); ok {
						starts++
						if _, ok := rows[i-1].Event.(queue.LandingChecksRecorded); !ok {
							t.Fatal("start lacks atomic exact checks")
						}
					}
				}
				if starts != depth {
					t.Fatal("missing chain starts", starts)
				}
			})
		})
	}
}

type admissionQuota struct{ until time.Time }

func (*admissionQuota) Error() string           { return "quota exhausted" }
func (q *admissionQuota) RetryAfter() time.Time { return q.until }

func TestAuthorityBarrierPinBearingBriefAndAdmission(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := authorityWorkerFixture(t)
		must(t, s.reconcile(t.Context(), w, true))
		state := lifecycleState(t, s, w)
		var req status.BriefPolicyRequest
		req.Authority = state.AuthorityPin()
		rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		for _, row := range rows {
			if c, ok := row.Event.(queue.ConfigApplied); ok {
				req.Config = c
			}
		}
		w.AuthorityRequired = false
		if _, err := s.BriefPolicy(t.Context(), w.Key, req); err == nil || !strings.Contains(err.Error(), "opt-in required") {
			t.Fatal("pin-bearing brief bypassed worker opt-in", err)
		}
		if _, err := s.readAuthorityAdmission(t.Context(), t.Context(), w, state); err == nil || !strings.Contains(err.Error(), "opt-in/configuration unavailable") {
			t.Fatal("pin-bearing admission bypassed worker opt-in", err)
		}
		w.AuthorityRequired = true
		w.resolveAuthorityLifecycle = nil
		if _, err := s.readAuthorityAdmission(t.Context(), t.Context(), w, state); err == nil {
			t.Fatal("flag alone enabled admission")
		}
	})
}

func TestAuthorityAdmissionWorkerTimeout(t *testing.T) {
	for _, mode := range []string{"read_timeout", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				authorityAdmissionWorkerTimeout(t, mode)
			})
		})
	}
}

func TestAuthorityAdmissionReaderErrorsAreSanitized(t *testing.T) {
	for _, mode := range []string{"secret_execute", "secret_cycle", "secret_loop", "secret_persist", "missing_reader"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				authorityAdmissionWorkerTimeout(t, mode)
			})
		})
	}
}

func TestAuthorityAdmissionWorkerLockedAppend(t *testing.T) {
	// Real socket I/O prevents synctest's clock from advancing.
	// The reader confirms the lock before returning a synthetic read timeout;
	// keep it held until execute returns, with an independent real watchdog.
	authorityAdmissionWorkerTimeout(t, "locked_append")
}

func authorityAdmissionWorkerTimeout(t *testing.T, mode string) {
	t.Helper()
	db, conn := databaseConnection(t)
	s, w, f, _ := authorityWorkerWithStore(t, db)
	f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(100, "@openmerge queue")}})
	must(t, s.reconcile(t.Context(), w, true))
	cfg := w.plannerConfig(s.options.PollInterval)
	build := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
	proceed, err := s.store.BeginConsequential(t.Context(), build, cfg, time.Now())
	must(t, err)
	if !proceed {
		t.Fatal("build did not begin")
	}
	// Build via the existing executor seam; this regression exercises the
	// real serve landing path while the separate forge gate remains dormant.
	x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, BuildMetadataKey: w.Key, BuildPolicyDigest: w.stageA.policy.Digest}
	x.BuildMetadata = func(_ queue.State, key executor.BuildChangeKey, batch queue.Batch, digest string) ([]*forge.ChangeMetadata, error) {
		current := lifecycleState(t, s, w)
		return executor.LoadBuildMetadataWithAncestry(current, key, *current.Batch(batch.ID), digest, func(a, b string) (bool, error) { return f.IsAncestor(t.Context(), repo, a, b) })
	}
	must(t, x.Execute(t.Context(), build.Action, s.store.EmitWithPolicy(t.Context(), build, cfg, w.stageA.policy)))
	must(t, s.store.Complete(t.Context(), build, store.Done))
	batch := lifecycleState(t, s, w).BatchForPR(1)
	f.SetCheckRuns(repo, batch.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 1, State: forge.Success, HeadBranch: batch.TestRef}})
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
	must(t, err)
	var claim store.ClaimedAction
	if mode != "secret_cycle" && mode != "secret_loop" && mode != "secret_persist" {
		claim = claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
	}
	s.options.ActionTimeout = time.Second
	writeTimeout := completionWriteTimeout
	if mode == "locked_append" {
		// Exclude unrelated error-status writes from the held-lock measurement.
		w.reportingNext = time.Now().Add(time.Hour)
		s.options.ActionTimeout = 30 * time.Second
		s.authorityWriteTimeout = 100 * time.Millisecond
		writeTimeout = s.authorityWriteTimeout
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	read := false
	var lockHeldAt time.Time
	var logs bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	readerErr := fmt.Errorf("https://x-access-token:ghs_SECRET@example.test/repo: %w", forge.ErrRulesUnavailable)
	installWorkerAuthorityReader(t, w, func(ctx context.Context) (authority.Observation, error) {
		read = true
		if strings.HasPrefix(mode, "secret_") {
			if mode == "secret_persist" {
				// Store is concrete and has no append-failure injection seam. Closing
				// this test-owned pool after Load fails only the subsequent persistence.
				db.Close()
			}
			return forge.EmptyWriteRestrictionObservation(), readerErr
		}
		if mode == "locked_append" {
			_, err := conn.Exec(t.Context(), "BEGIN")
			must(t, err)
			_, err = conn.Exec(t.Context(), `SELECT pg_advisory_xact_lock(1330464081, hashtext(current_schema() || '/' || $1))`, fmt.Sprint(w.id))
			must(t, err)
			// Also release the transaction if an assertion aborts this test.
			t.Cleanup(func() {
				_, err := conn.Exec(context.Background(), "ROLLBACK")
				must(t, err)
			})
			// Exec returning confirms this separate connection holds the queue lock.
			lockHeldAt = time.Now()
			return forge.EmptyWriteRestrictionObservation(), context.DeadlineExceeded
		}
		if mode == "shutdown" {
			cancel()
		}
		<-ctx.Done()
		return forge.EmptyWriteRestrictionObservation(), ctx.Err()
	})
	before, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	started := time.Now()
	watchdogTimeout := s.options.ActionTimeout + writeTimeout + time.Second
	if mode == "locked_append" {
		watchdogTimeout = 45 * time.Second
	}
	watchdog, stop := context.WithTimeout(parent, watchdogTimeout)
	defer stop()
	if mode == "missing_reader" {
		installWorkerAuthorityReader(t, w, nil)
	}
	if mode == "secret_cycle" || mode == "secret_loop" || mode == "secret_persist" {
		// Skip the recurring read so the real cycle reaches fresh landing admission.
		w.authorityNextCheck = time.Now().Add(time.Hour)
		if mode == "secret_loop" {
			capture := &cancelOnAdmissionFailure{Handler: s.options.Logger.Handler(), cancel: cancel}
			s.options.Logger = slog.New(capture)
			s.loop(watchdog, w)
			err = capture.err
		} else {
			err = s.cycle(watchdog, w)
		}
	} else {
		err = s.execute(watchdog, w, claim)
	}
	if strings.HasPrefix(mode, "secret_") || mode == "missing_reader" {
		var classified *authorityObservationReadFailure
		if !errors.As(err, &classified) || !errors.Is(err, forge.ErrRulesUnavailable) {
			t.Fatalf("lost read classification: %v", err)
		}
		if mode != "missing_reader" && (!read || !errors.Is(err, readerErr)) {
			t.Fatalf("reader not reached or cause lost: %v", err)
		}
		if strings.Contains(err.Error(), "ghs_SECRET") || strings.Contains(logs.String(), "ghs_SECRET") || strings.Contains(err.Error(), "https://") || strings.Contains(logs.String(), "https://") {
			t.Fatalf("reader credentials escaped: error=%v logs=%s", err, &logs)
		}
		if !strings.Contains(err.Error(), "unavailable") || w.authorityFailures != 1 {
			t.Fatalf("missing sanitized class/retry: %v failures=%d", err, w.authorityFailures)
		}
		if mode == "secret_cycle" || mode == "secret_loop" || mode == "secret_persist" {
			if !strings.Contains(logs.String(), `"msg":"reconcile cycle finished"`) || !strings.Contains(logs.String(), "authority admission read failed") {
				t.Fatalf("cycle did not log admission failure: %s", &logs)
			}
		}
		if mode != "secret_persist" && !hasAuthorityReadError(lifecycleState(t, s, w)) {
			t.Fatal("read failure did not persist block")
		}
		return
	}
	finished := time.Now()
	if mode == "locked_append" {
		_, unlockErr := conn.Exec(t.Context(), "ROLLBACK")
		must(t, unlockErr)
	}
	wantErr := context.DeadlineExceeded
	if mode == "shutdown" {
		wantErr = context.Canceled
	}
	if !read || !errors.Is(err, wantErr) {
		t.Fatalf("worker did not time out admission read: read=%v err=%v", read, err)
	}
	elapsed := time.Since(started)
	if mode == "locked_append" {
		elapsed = finished.Sub(lockHeldAt)
		if elapsed < writeTimeout || elapsed > writeTimeout+5*time.Second {
			t.Fatalf("unbounded or skipped locked admission persistence: elapsed=%v bound=%v", elapsed, writeTimeout)
		}
	} else if elapsed > s.options.ActionTimeout+writeTimeout+time.Second/2 || (mode == "shutdown" && elapsed != 0) {
		t.Fatalf("unbounded admission persistence: elapsed=%v", elapsed)
	}
	if mode == "locked_append" {
		if w.authorityFailures != 1 || w.authorityNextCheck.Before(lockHeldAt.Add(time.Minute)) || w.authorityNextCheck.After(finished.Add(time.Minute)) {
			t.Fatal("locked append skipped recovery schedule")
		}
	} else if mode != "shutdown" && (w.authorityFailures != 1 || time.Until(w.authorityNextCheck) < time.Minute-time.Second/2 || time.Until(w.authorityNextCheck) > time.Minute) {
		t.Fatal("worker timeout skipped recovery schedule")
	}
	if got := hasAuthorityReadError(lifecycleState(t, s, w)); got != (mode == "read_timeout") {
		t.Fatalf("durable read_error=%v in %s", got, mode)
	}
	if mode == "shutdown" && w.authorityFailures != 0 {
		t.Fatal("shutdown scheduled another read")
	}
	rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	if mode != "read_timeout" && len(rows) != len(before) {
		t.Fatalf("failed persistence/shutdown appended events: %d -> %d", len(before), len(rows))
	}
	for _, row := range rows {
		switch row.Event.(type) {
		case queue.LandingStarted, queue.LandingChecksRecorded, queue.LandingDecisionRecorded:
			t.Fatal("timed-out worker committed admission evidence or start")
		}
	}
}

// Capture the actual loop error attribute and stop after its failure log.
type cancelOnAdmissionFailure struct {
	slog.Handler
	cancel context.CancelFunc
	err    error
}

func (h *cancelOnAdmissionFailure) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	if record.Message == "queue cycle failed" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "error" {
				h.err, _ = attr.Value.Any().(error)
			}
			return true
		})
		h.cancel()
	}
	return err
}
