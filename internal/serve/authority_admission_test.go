package serve

import (
	"context"
	"errors"
	"fmt"
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
						x.LandingDecisions = s.stageAGate(w, claim, cfg, basis)
						err = x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy))
						if err == nil || !strings.Contains(err.Error(), "opt-in/configuration unavailable") {
							t.Fatalf("missing resolver bypassed executor admission: %v", err)
						}
						w.resolveAuthorityLifecycle = resolver
						for _, failure := range []error{errors.New("permission denied"), &admissionQuota{until: time.Now().Add(time.Minute)}} {
							w.authorityObservation.Read = func(context.Context) (authority.Observation, error) {
								return authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}, failure
							}
							x.LandingDecisions = s.stageAGate(w, claim, cfg, basis)
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
					x.LandingDecisions = s.stageAGate(w, claim, cfg, basis)
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
		if _, err := s.readAuthorityAdmission(t.Context(), w, state); err == nil || !strings.Contains(err.Error(), "opt-in/configuration unavailable") {
			t.Fatal("pin-bearing admission bypassed worker opt-in", err)
		}
		w.AuthorityRequired = true
		w.resolveAuthorityLifecycle = nil
		if _, err := s.readAuthorityAdmission(t.Context(), w, state); err == nil {
			t.Fatal("flag alone enabled admission")
		}
	})
}
