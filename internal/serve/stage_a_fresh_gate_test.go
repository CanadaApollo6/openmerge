package serve

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Exercise both the last forge read and a journal write racing atomic start.
// Neither can authorize a decision against an older positive stance.
func TestStageAFreshNegativeStancesBeforeStart(t *testing.T) {
	for _, timing := range []string{"final-read", "before-start", "delayed-response"} {
		for _, kind := range []string{"request-changes", "dismissal", "unapprove", "withdrawal", "revoked-writer", "team-removal", "grant-aba"} {
			t.Run(timing+"/"+kind, func(t *testing.T) {
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				team := forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}
				if kind == "team-removal" {
					f.SetTeam(team, []forge.UserIdentity{{ID: 21, Login: "writer", Kind: forge.UserAccount}})
					w.stageA.repository.Groups = map[string][]string{"reviewers": {"org/reviewers"}}
					w.stageA.candidate.Approvals.Default = cp.Requirement{Selector: cp.Group, Group: "reviewers", Count: 1}
					must(t, s.reconcile(t.Context(), w, true))
				}
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				review := forge.NativeReview{ID: 100, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}
				native := forge.NativeReviews{Reviews: []forge.NativeReview{review}, Comments: []forge.NativeComment{shadowComment(1000, "@openmerge queue")}}
				f.SetNativeReviews(repo, 1, native)
				must(t, s.cycle(t.Context(), w))
				batch := shadowState(t, s, w).BatchForPR(1)
				if batch == nil || batch.TestedSHA == "" {
					t.Fatal("missing tested batch")
				}
				shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
				must(t, err)
				cfg := w.plannerConfig(s.options.PollInterval)
				_, err = s.store.Plan(t.Context(), w.id, cfg, time.Now())
				must(t, err)
				claims, err := s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, time.Now())
				must(t, err)
				var claim store.ClaimedAction
				for _, c := range claims {
					if _, ok := c.Action.(queue.Land); ok {
						claim = c
					}
				}
				if claim.Key == "" {
					t.Fatal("missing landing claim")
				}
				land := claim.Action.(queue.Land)
				must(t, s.trustGate(t.Context(), w, land.PRs))
				proceed, err := s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
				must(t, err)
				if !proceed {
					t.Fatal("claim did not begin")
				}
				beforeNegative, err := s.store.Watermark(t.Context(), w.id)
				must(t, err)
				inject := func() {
					switch kind {
					case "withdrawal":
						native.Reviews = nil
					case "revoked-writer", "grant-aba":
						f.SetWriteGrant(repo, 21, "read")
					case "team-removal":
						f.SetTeam(team, nil)
					case "request-changes":
						negative := review
						negative.ID = 101
						negative.State = forge.ReviewChangesRequested
						negative.Submitted.Time = review.Submitted.Time.Add(time.Minute)
						native.Reviews = append(native.Reviews, negative)
					case "dismissal":
						native.Reviews[0].State = forge.ReviewDismissed
						native.Reviews[0].Dismissal = &forge.NativeDismissal{ReviewID: 100, EventID: 101, Actor: review.Author, Time: forge.NativeTime{Time: review.Submitted.Time.Add(time.Minute), Source: forge.ReviewDismissalTime, Precision: time.Second}}
					case "unapprove":
						native.Comments = append(native.Comments, shadowComment(1001, "@openmerge unapprove"))
					}
					f.SetNativeReviews(repo, 1, native)
				}
				gate := s.stageAGate(w, claim, cfg)
				switch timing {
				case "final-read":
					load := gate.Load
					injected := false
					gate.Load = func(ctx context.Context) (queue.State, error) {
						state, err := load(ctx)
						if !injected {
							inject()
							injected = true
						}
						return state, err
					}
				case "delayed-response":
					readInputs := gate.ReadInputs
					gate.ReadInputs = func(ctx context.Context, land queue.Land, state queue.State) ([]queue.ApprovalEligibilityObserved, error) {
						defer func() { w.Driver = f }()
						w.Driver = &delayedStageANative{Forge: f, after: func() {
							inject()
							other := worker{Target: w.Target, id: w.id, stageA: w.stageA, cfg: w.cfg, configTip: w.configTip, configDigest: w.configDigest, prs: maps.Clone(w.prs), summaries: maps.Clone(w.summaries)}
							other.Driver = f
							done := make(chan error, 1)
							go func() {
								if kind == "unapprove" {
									done <- s.commands(t.Context(), &other, 1)
									return
								}
								_, err := s.observe(t.Context(), &other, 1)
								done <- err
							}()
							must(t, <-done)
							// ABA restores the forge view too: timestamps and semantic
							// fingerprints alone cannot detect the intervening denial.
							if kind == "grant-aba" {
								f.SetWriteGrant(repo, 21, "write")
								go func() { _, err := s.observe(t.Context(), &other, 1); done <- err }()
								must(t, <-done)
							}
						}}
						return readInputs(ctx, land, state)
					}
				default:
					commit := gate.Commit
					gate.Commit = func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
						inject()
						if kind == "unapprove" {
							must(t, s.commands(ctx, w, 1))
						} else {
							// A second worker's poll commits after the gate read, while
							// the first worker is paused immediately before atomic start.
							other := worker{Target: w.Target, id: w.id, stageA: w.stageA, cfg: w.cfg, configTip: w.configTip, configDigest: w.configDigest, prs: maps.Clone(w.prs), summaries: maps.Clone(w.summaries)}
							done := make(chan error, 1)
							go func() { _, err := s.observe(ctx, &other, 1); done <- err }()
							must(t, <-done)
							if kind == "grant-aba" {
								f.SetWriteGrant(repo, 21, "write")
								go func() { _, err := s.observe(ctx, &other, 1); done <- err }()
								must(t, <-done)
							}
						}
						return commit(ctx, fresh, decisions, start)
					}
				}
				x := executor.Executor{Driver: w.Driver, Git: w.Git, Repo: w.repo(), Base: w.Key.BaseBranch, LandingDecisions: gate, CheckPolicy: func([]forge.CheckRun) (bool, bool, error) { return true, true, nil }, BeforeLand: func(ctx context.Context, land queue.Land) error { return s.trustGate(ctx, w, land.PRs) }}
				err = x.Execute(t.Context(), land, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy))
				if err == nil {
					t.Fatal("negative stance authorized landing")
				}
				if timing == "final-read" && !errors.Is(err, executor.ErrLandingApprovals) {
					t.Fatalf("expected durable approval wait: %v", err)
				}
				if timing != "final-read" && !errors.Is(err, store.ErrLandingDecisionStale) {
					t.Fatalf("expected atomic stale rejection: %v", err)
				}
				records, err := s.store.EventsAfter(t.Context(), w.id, 0)
				must(t, err)
				found := false
				for _, record := range records {
					switch e := record.Event.(type) {
					case queue.LandingStarted, queue.Landed:
						t.Fatalf("negative stance permitted %T", e)
					case queue.ApprovalEligibilityObserved:
						if record.ID <= beforeNegative {
							continue
						}
						actor := e.Actors["github.com:user:21"]
						switch kind {
						case "withdrawal":
							found = found || !actor.Grant.Fresh
						case "revoked-writer", "grant-aba":
							found = found || actor.Grant.Write == cp.False
						case "team-removal":
							found = found || len(e.Groups["reviewers"].IDs) == 0
						}
					case queue.ReviewStanceObserved:
						if record.ID <= beforeNegative {
							continue
						}
						if e.Stance.Kind != "approve" {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("negative source attribution was not journaled")
				}
				if timing == "final-read" && len(shadowState(t, s, w).ApprovalWaits()) != 1 {
					t.Fatal("missing approval wait")
				}
			})
		}
	}
}

func TestStageARebaseGeneratedMembersAfterBaseAdvance(t *testing.T) {
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	_, err := f.PushPR(repo, 1, "Document OpenMerge-Change trailer format")
	must(t, err)
	_, err = f.PushBase(repo, "main", "base advanced past contributor branch", map[string]string{"unrelated.txt": "new base"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	w.cfg.MergeMethod = queue.Rebase
	must(t, s.configuration(t.Context(), w))
	f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue"), shadowComment(101, "@openmerge approve "+shadowDigest(t, s, w))))
	for range 5 {
		must(t, s.cycle(t.Context(), w))
		if batch := shadowState(t, s, w).BatchForPR(1); batch != nil && batch.TestedSHA != "" {
			shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
		}
	}
	records, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	starts := 0
	for _, record := range records {
		if _, ok := record.Event.(queue.LandingStarted); ok {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("advanced-base rebase starts=%d, want one", starts)
	}
}

// Capture the response first, then hold A while worker B commits to Postgres.
type delayedStageANative struct {
	*fake.Forge
	after func()
}

func (d *delayedStageANative) NativeReviews(ctx context.Context, repo forge.RepoRef, number int) (forge.NativeReviews, error) {
	value, err := d.Forge.NativeReviews(ctx, repo, number)
	if d.after != nil {
		run := d.after
		d.after = nil
		run()
	}
	return value, err
}
