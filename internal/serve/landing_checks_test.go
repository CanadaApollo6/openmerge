package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestLandingCheckEvidenceUsesExactPolicyInputs(t *testing.T) {
	success, err := condition.Parse("check-success = ci")
	must(t, err)
	failure, err := condition.Parse("check-failure = lint")
	must(t, err)
	expr := condition.All(success, condition.Not(failure))
	checks := []forge.CheckRun{{Name: "ci", Source: "app:1", ID: 10, Attempt: 1, State: forge.Failure}, {Name: "ci", Source: "app:1", ID: 11, Attempt: 2, WorkflowRunID: 100, State: forge.Success, HeadBranch: "queue"}, {Name: "lint", Source: "status:bot", ID: 12, State: forge.Success}, {Name: "unused", Attempt: 1, AttemptInferred: true, State: forge.Pending}}
	values, err := checkSnapshot(expr, checks)
	must(t, err)
	met, _ := expr.Evaluate(values)
	if !met {
		t.Fatal("nested negative policy did not pass")
	}
	land := queue.Land{BatchID: "batch", TestedSHA: strings.Repeat("a", 40), Method: queue.FastForward, Ref: "queue", PRs: []queue.BatchPR{{PR: 1, HeadSHA: strings.Repeat("b", 40)}}}
	basis := queue.State{ConfigCommit: "config", ConfigDigest: strings.Repeat("c", 64)}
	builder, err := landingCheckEvidence(basis, strings.Repeat("d", 64), land, checks, time.Now(), "attempt")
	must(t, err)
	must(t, builder.RecordMember(1, land.PRs[0].HeadSHA, expr, values))
	e := builder.Event()
	must(t, e.Validate())
	if len(e.Checks) != 4 || *e.Checks[1].Attempt != 2 || *e.Checks[1].RunID != 11 || e.Checks[2].Attempt != nil || e.Checks[3].ProducerIdentity != nil || e.Checks[3].RunID != nil || e.Checks[3].Attempt != nil || e.LandedSHA == e.TestedSHA {
		t.Fatal("read identities/absence/marker lost", e)
	}
	checks[1].Attempt = 99
	if *e.Checks[1].Attempt != 2 {
		t.Fatal("evidence shares input memory")
	}
	collision := append(checks, forge.CheckRun{Name: "ci", Source: "app:2", ID: 999, State: forge.Success})
	if _, err := checkSnapshot(expr, collision); err == nil {
		t.Fatal("producer collision authorized")
	}
	if _, err := checkSnapshot(condition.Not(success), nil); err != nil {
		t.Fatal(err)
	}
	missing, _ := checkSnapshot(condition.Not(success), nil)
	if ok, _ := condition.Not(success).Evaluate(missing); ok {
		t.Fatal("absent check passed under NOT")
	}
}

// Exercise the production gate and atomic store using generated fake commits.
// Push authority activation is PR 9's separate seam; this fixture invokes the
// executor directly so it does not pretend the test pin is a verified fence.
func TestLandingChecksAuthorityMethodsAndChainPositions(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		for _, depth := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s/depth-%d", method, depth), func(t *testing.T) {
				s, w, f := shadowFixtureForMethod(t, depth, "main", "7", false, method)
				must(t, s.configuration(t.Context(), w))
				must(t, s.reconcile(t.Context(), w, true))
				base := shadowState(t, s, w).BaseSHA
				for n := 1; n <= depth; n++ {
					if n > 1 {
						_, err := f.OpenPR(repo, n, base, true)
						must(t, err)
						must(t, f.SetPRMetadata(repo, n, "main", false, "contributor"))
					}
					pr, err := f.PullRequest(t.Context(), repo, n)
					must(t, err)
					f.SetNativeReviews(repo, n, forge.NativeReviews{Reviews: []forge.NativeReview{{ID: int64(200 + n), CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}, Comments: []forge.NativeComment{shadowComment(int64(100+n), "@openmerge queue")}})
					if method == queue.FastForward {
						base = pr.HeadSHA
					}
				}
				batches := make([]*queue.Batch, depth)
				for n := 1; n <= depth; n++ {
					must(t, s.reconcile(t.Context(), w, false))
					cfg := w.plannerConfig(s.options.PollInterval)
					build := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool {
						switch a.(type) {
						case queue.BuildTestRef, queue.BuildChainedTestRef:
							return true
						}
						return false
					})
					must(t, s.execute(t.Context(), w, build))
					batches[n-1] = shadowState(t, s, w).BatchForPR(n)
					if batches[n-1] == nil {
						t.Fatal("missing built chain member", n)
					}
					b := batches[n-1]
					f.SetCheckRuns(repo, b.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: uint64(700 + n), Attempt: 2, WorkflowRunID: uint64(800 + n), State: forge.Success, HeadBranch: b.TestRef}})
					refresh := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { r, ok := a.(queue.RefreshChecks); return ok && r.BatchID == b.ID })
					must(t, s.execute(t.Context(), w, refresh))
				}
				state := shadowState(t, s, w)
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.GoverningCommit = state.ConfigCommit
				pin.EffectiveDigest = state.ConfigDigest
				pin.OpenMerge.Binding.EffectiveDigest = state.ConfigDigest
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
				must(t, err)
				for n := 1; n <= depth; n++ {
					// Trust refresh can reopen CI when a speculative member reaches
					// the head. Follow that existing refresh before claiming Land.
					must(t, s.trustGate(t.Context(), w, batches[n-1].PRs))
					cfg := w.plannerConfig(s.options.PollInterval)
					for _, a := range queue.Plan(cfg, shadowState(t, s, w), time.Now()) {
						if r, ok := a.(queue.RefreshChecks); ok && r.BatchID == batches[n-1].ID {
							refresh := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { v, ok := a.(queue.RefreshChecks); return ok && v.BatchID == r.BatchID })
							must(t, s.execute(t.Context(), w, refresh))
						}
					}
					claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
					land := claim.Action.(queue.Land)
					if land.BatchID != batches[n-1].ID {
						t.Fatal("wrong chain landing")
					}
					ok, err := s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
					must(t, err)
					if !ok {
						t.Fatal("claim did not begin")
					}
					basis := shadowState(t, s, w)
					x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Speculative: depth > 1, LandingDecisions: s.stageAGate(w, claim, cfg, basis)}
					must(t, x.Execute(t.Context(), land, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy)))
					must(t, s.store.Complete(t.Context(), claim, store.Done))
				}
				events, err := s.store.EventsAfter(t.Context(), w.id, 0)
				must(t, err)
				recorded := []queue.LandingChecksRecorded{}
				for i, r := range events {
					if e, ok := r.Event.(queue.LandingChecksRecorded); ok {
						recorded = append(recorded, e)
						start, ok := events[i+1].Event.(queue.LandingStarted)
						if !ok || start.AttemptID != e.AttemptID {
							t.Fatal("checks/start not atomic neighbors")
						}
					}
				}
				if len(recorded) != depth {
					t.Fatal("missing per-position checks", len(recorded))
				}
				for i, e := range recorded {
					if e.BatchID != batches[i].ID || e.TestedSHA != batches[i].TestedSHA || *e.Checks[0].RunID != uint64(701+i) || *e.Checks[0].Attempt != 2 || e.Expressions[e.Members[0].Conditions.Expression] != w.cfg.MergeWhen.String() {
						t.Fatal("landing reused another batch's checks", e)
					}
					if method == queue.FastForward && e.LandedSHA == e.TestedSHA {
						t.Fatal("marker collapsed into contributor head")
					}
				}
			})
		}
	}
}

// Claim only the action being driven. The older single-batch helper leases all
// planned work, which hides later speculative builds behind unrelated leases.
func claimLandingChecksAction(t *testing.T, s *Server, w *worker, cfg queue.Config, want func(queue.Action) bool) store.ClaimedAction {
	t.Helper()
	planned, err := s.store.Plan(t.Context(), w.id, cfg, time.Now())
	must(t, err)
	skip := []string{}
	for _, p := range planned {
		if !want(p.Action) {
			skip = append(skip, p.Key)
		}
	}
	claims, err := s.store.ClaimQueue(t.Context(), w.id, 1, time.Minute, time.Now(), skip...)
	must(t, err)
	if len(claims) != 1 || !want(claims[0].Action) {
		t.Fatalf("requested check-evidence action missing: planned=%+v claims=%+v", planned, claims)
	}
	return claims[0]
}

func TestLandingChecksLegacyGateAndPinRace(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprintf("pin-race-%t", race), func(t *testing.T) {
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			pr := w.prs[1]
			f.SetNativeReviews(repo, 1, forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}, Comments: []forge.NativeComment{shadowComment(100, "@openmerge queue")}})
			batch := shadowBuildBatch(t, s, w, 1)
			shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", State: forge.Success, HeadBranch: batch.TestRef}})
			_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
			must(t, err)
			cfg := w.plannerConfig(s.options.PollInterval)
			claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
			proceed, err := s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
			must(t, err)
			if !proceed {
				t.Fatal("claim did not begin")
			}
			basis := shadowState(t, s, w)
			gate := s.stageAGate(w, claim, cfg, basis)
			if gate.ReadInputs == nil || gate.ReadInputsWithChecks != nil {
				t.Fatal("legacy gate opted into evidence read")
			}
			// This extra unused check exceeds the new evidence string cap. Legacy
			// condition evaluation still accepts it without constructing evidence.
			f.SetCheckRuns(repo, batch.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", State: forge.Success, HeadBranch: batch.TestRef}, {Name: strings.Repeat("x", 4097), Source: "app:15368", State: forge.Success, HeadBranch: batch.TestRef}})
			if race {
				commit := gate.Commit
				gate.Commit = func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
					raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
					must(t, err)
					event, err := queue.DecodeEvent(raw)
					must(t, err)
					pin := event.(queue.PolicyAuthorityApplied)
					pin.GoverningCommit = basis.ConfigCommit
					pin.EffectiveDigest = basis.ConfigDigest
					pin.OpenMerge.Binding.EffectiveDigest = basis.ConfigDigest
					_, err = s.store.Append(ctx, w.id, []queue.Event{pin}, "")
					must(t, err)
					return commit(ctx, fresh, decisions, start)
				}
			}
			x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, LandingDecisions: gate}
			err = x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy))
			if race {
				var required store.LandingChecksRequiredError
				if !errors.As(err, &required) || errors.Is(err, store.ErrLandingDecisionStale) || errors.Is(err, store.ErrStale) {
					t.Fatalf("pin race classification: %v", err)
				}
			} else {
				must(t, err)
			}
			records, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			starts, checks := 0, 0
			for _, record := range records {
				switch record.Event.(type) {
				case queue.LandingStarted:
					starts++
				case queue.LandingChecksRecorded:
					checks++
				}
			}
			if checks != 0 || race && starts != 0 || !race && starts != 1 {
				t.Fatalf("legacy/racing gate recorded starts=%d checks=%d", starts, checks)
			}
		})
	}
}

func TestLandingChecksGateOverflowPrecedesUnmetMembers(t *testing.T) {
	for _, oversizedMember := range []int{1, 2} {
		t.Run(fmt.Sprintf("member-%d", oversizedMember), func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				base := shadowState(t, s, w).BaseSHA
				_, err := f.OpenPR(repo, 2, base, true)
				must(t, err)
				for n := 1; n <= 2; n++ {
					f.SetNativeReviews(repo, n, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(int64(100+n), "@openmerge queue")}})
				}
				must(t, s.reconcile(t.Context(), w, false))
				basis := shadowState(t, s, w)
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.GoverningCommit, pin.EffectiveDigest = basis.ConfigCommit, basis.ConfigDigest
				pin.OpenMerge.Binding.EffectiveDigest = basis.ConfigDigest
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
				must(t, err)
				basis = shadowState(t, s, w)
				w.cfg.MergeWhen, err = condition.Parse("title = ready")
				must(t, err)
				land := queue.Land{BatchID: "gate-precedence", TestedSHA: strings.Repeat("e", 40), Ref: "queue", Method: queue.Merge}
				for n := 1; n <= 2; n++ {
					pr := w.prs[n]
					pr.Title = "unmet"
					if n == oversizedMember {
						pr.Title = strings.Repeat("x", condition.MaxEvidenceStringBytes+1)
					}
					w.prs[n] = pr
					land.PRs = append(land.PRs, queue.BatchPR{PR: n, HeadSHA: pr.HeadSHA})
				}
				cfg := w.plannerConfig(s.options.PollInterval)
				gate := s.stageAGate(w, store.ClaimedAction{}, cfg, basis)
				_, err = gate.ReadInputsWithChecks(t.Context(), land, basis, nil, time.Now(), "attempt")
				if !errors.Is(err, queue.ErrLandingChecksTooLarge) || errors.Is(err, executor.ErrChecksNotSuccessful) {
					t.Fatalf("overflow lost to unmet condition: %v", err)
				}
				for n := 1; n <= 2; n++ {
					pr := w.prs[n]
					pr.Title = "unmet"
					w.prs[n] = pr
				}
				_, err = gate.ReadInputsWithChecks(t.Context(), land, basis, nil, time.Now(), "attempt")
				if !errors.Is(err, executor.ErrChecksNotSuccessful) {
					t.Fatalf("bounded unmet policy changed outcome: %v", err)
				}
				records, err := s.store.EventsAfter(t.Context(), w.id, 0)
				must(t, err)
				for _, record := range records {
					switch record.Event.(type) {
					case queue.LandingStarted, queue.LandingChecksRecorded:
						t.Fatal("rejected preflight emitted start evidence")
					}
				}
			})
		})
	}
}
