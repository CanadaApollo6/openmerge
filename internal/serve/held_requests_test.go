package serve

import (
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestHeldQueueRequestPendingToPass(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig+"    enter_when: [check-success = sign-off]\n")
		pr, err := f.PullRequest(t.Context(), repo, 1)
		must(t, err)
		f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Pending}})
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.reconcile(t.Context(), w, true))
		if len(lifecycleState(t, s, w).Entries) != 0 {
			t.Fatal("pending entry admitted")
		}
		f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
		must(t, s.reconcile(t.Context(), w, true))
		state := lifecycleState(t, s, w)
		if len(state.Entries) != 1 || state.Entries[0].HeadSHA != pr.HeadSHA {
			t.Fatalf("pending request was dropped; pass did not admit exact head: %+v", state.Entries)
		}
	})
}

func TestHeldQueueRequestBlockedLifecycleStatus(t *testing.T) {
	t.Parallel()
	for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
		t.Run(lifecycle, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig+"    enter_when: [check-success = sign-off]\n")
				pr, err := f.PullRequest(t.Context(), repo, 1)
				must(t, err)
				f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Pending}})
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.reconcile(t.Context(), w, true))
				if _, held := lifecycleState(t, s, w).HeldRequest(1); !held {
					t.Fatal("request was not held")
				}
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LifecycleChanged{State: lifecycle, Detail: "base unavailable"}}, "")
				must(t, err)
				f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
				must(t, s.reconcile(t.Context(), w, false))
				state := lifecycleState(t, s, w)
				if _, held := state.HeldRequest(1); held || len(state.Entries) != 0 {
					t.Fatal("inactive queue retained or admitted held request")
				}
				cancelled := f.Statuses(repo, 1)[pr.HeadSHA]
				if cancelled.State != forge.Paused || cancelled.Reason != lifecycle || strings.Contains(cancelled.Summary, "Waiting for queue entry conditions:") {
					t.Fatalf("stale held status: %+v", cancelled)
				}
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.reconcile(t.Context(), w, true))
				fresh := f.Statuses(repo, 1)[pr.HeadSHA]
				if fresh.State != cancelled.State || fresh.Reason != cancelled.Reason || fresh.Summary != cancelled.Summary {
					t.Fatalf("cancelled hold differs from dropped command: held=%+v fresh=%+v", cancelled, fresh)
				}
			})
		})
	}
}

func TestHeldQueueRequestLifecycleReactivation(t *testing.T) {
	t.Parallel()
	for _, authorityMode := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
				t.Run(fmt.Sprintf("authority=%v/managed=%v/%s", authorityMode, managed, lifecycle), func(t *testing.T) {
					t.Parallel()
					testSynctest(t, func(t *testing.T) {
						raw := repositoryConfig + "    enter_when: [check-success = sign-off]\n"
						var s *Server
						var w *worker
						var f *fake.Forge
						if authorityMode {
							s, w, f, _ = authorityWorkerFixture(t)
							_, err := f.PushBase(repo, "main", "entry policy", map[string]string{".openmerge.yml": lifecycleConfig(raw)})
							must(t, err)
						} else {
							s, w, f, _ = fixture(t, raw)
						}
						pr, err := f.PullRequest(t.Context(), repo, 1)
						must(t, err)
						f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Pending}})
						if authorityMode {
							f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(101, "@openmerge queue")}})
						} else {
							f.AddComment(repo, 1, "writer", "@openmerge queue")
						}
						must(t, s.reconcile(t.Context(), w, true))
						if _, held := lifecycleState(t, s, w).HeldRequest(1); !held {
							t.Fatal("request was not held")
						}
						w.managed, w.ready = managed, true
						// No reconcile or PR observation between these store events:
						// managed workers skip inactive queues; ordinary polls can dedupe.
						_, err = s.store.Append(t.Context(), w.id, []queue.Event{
							queue.LifecycleChanged{State: lifecycle, Detail: "base unavailable"},
							queue.LifecycleChanged{State: "active"},
						}, "")
						must(t, err)
						f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
						must(t, s.reconcile(t.Context(), w, false))
						state := lifecycleState(t, s, w)
						if len(state.Entries) != 0 || len(state.HeldRequests()) != 0 {
							t.Fatal("reactivation reused old command", state.Entries, state.HeldRequests())
						}
						// Also exercise the next full observation after reactivation.
						must(t, s.reconcile(t.Context(), w, true))
						if len(lifecycleState(t, s, w).Entries) != 0 {
							t.Fatal("fresh observation reused old command")
						}
						if authorityMode {
							f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(101, "@openmerge queue"), shadowComment(102, "@openmerge queue")}})
						} else {
							f.AddComment(repo, 1, "writer", "@openmerge queue")
						}
						must(t, s.reconcile(t.Context(), w, true))
						if len(lifecycleState(t, s, w).Entries) != 1 {
							t.Fatal("fresh command did not admit after reactivation")
						}
					})
				})
			}
		}
	}
}

func TestHeldQueueRequestLifecycle(t *testing.T) {
	t.Parallel()
	for _, authorityMode := range []bool{false, true} {
		for _, outcome := range []string{"pass", "missing", "fail", "push", "dequeue", "closed", "draft", "restart", "restart-closed", "writer-revoked", "stale-approval", "already-true", "already-false", "unknown-and-false"} {
			t.Run(fmt.Sprintf("authority=%v/%s", authorityMode, outcome), func(t *testing.T) {
				t.Parallel()
				testSynctest(t, func(t *testing.T) {
					raw := repositoryConfig + "    enter_when: ['-draft', check-success = sign-off]\n"
					var s *Server
					var w *worker
					var f *fake.Forge
					if authorityMode {
						s, w, f, _ = authorityWorkerFixture(t)
						_, err := f.PushBase(repo, "main", "entry policy", map[string]string{".openmerge.yml": lifecycleConfig(raw)})
						must(t, err)
					} else {
						s, w, f, _ = fixture(t, raw)
					}
					must(t, s.configuration(t.Context(), w))
					pr, err := f.PullRequest(t.Context(), repo, 1)
					must(t, err)
					native := forge.NativeReviews{}
					if outcome == "writer-revoked" || outcome == "stale-approval" {
						base, err := f.ReadRef(t.Context(), repo, "main")
						must(t, err)
						pr, err = f.OpenPR(repo, 2, base, false)
						must(t, err)
						if authorityMode {
							f.SetNativeReviews(repo, 2, forge.NativeReviews{})
						}
						must(t, f.Approve(repo, 2, forge.Approval{User: "writer", SHA: pr.HeadSHA, CanWrite: true}))
						native.Reviews = []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}
					}
					initial := forge.Pending
					if outcome == "already-true" {
						initial = forge.Success
					}
					if outcome == "already-false" {
						initial = forge.Failure
					}
					if outcome == "unknown-and-false" {
						must(t, f.SetPRMetadata(repo, pr.Number, "main", true, "contributor"))
					}
					f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: initial}})
					if outcome == "missing" {
						f.SetCheckRuns(repo, pr.HeadSHA, nil)
					}
					if authorityMode {
						native.Comments = []forge.NativeComment{shadowComment(101, "@openmerge queue")}
						f.SetNativeReviews(repo, pr.Number, native)
					} else {
						f.AddComment(repo, pr.Number, "writer", "@openmerge queue")
					}
					must(t, s.reconcile(t.Context(), w, true))
					state := lifecycleState(t, s, w)
					if outcome == "already-true" {
						if len(state.Entries) != 1 {
							t.Fatal("true conditions did not admit immediately")
						}
						return
					}
					if outcome == "already-false" || outcome == "unknown-and-false" {
						if _, held := state.HeldRequest(pr.Number); held || len(state.Entries) != 0 {
							t.Fatal("definitely false conditions held or admitted")
						}
					} else {
						if _, held := state.HeldRequest(pr.Number); !held || len(state.Entries) != 0 {
							t.Fatal("pending request was not held before admission")
						}
						statuses := f.Statuses(repo, pr.Number)
						if !slices.ContainsFunc(slices.Collect(maps.Values(statuses)), func(s forge.Status) bool {
							return strings.Contains(s.Summary, `Waiting for queue entry conditions: check-success = "sign-off"`)
						}) {
							t.Fatalf("missing waiting status: %+v", statuses)
						}
						journals, err := s.Journals(t.Context(), []store.QueueKey{w.Key})
						must(t, err)
						_, entry, _ := status.Project(journals[0], pr.Number, 20, nil)
						if entry.WaitingReason.Code != status.WaitingEntry || entry.WaitingReason.Text != `Waiting for queue entry conditions: check-success = "sign-off"` {
							t.Fatalf("wrong waiting projection: %+v", entry)
						}
					}
					switch outcome {
					case "fail":
						f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Failure}})
					case "push":
						_, err = f.PushPR(repo, pr.Number, "cancel held head")
						must(t, err)
					case "dequeue":
						if authorityMode {
							f.SetNativeReviews(repo, pr.Number, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(101, "@openmerge queue"), shadowComment(102, "@openmerge dequeue")}})
						} else {
							f.AddComment(repo, pr.Number, "writer", "@openmerge dequeue")
						}
					case "closed":
						must(t, f.ClosePR(repo, pr.Number))
					case "draft":
						must(t, f.SetPRMetadata(repo, pr.Number, "main", true, "contributor"))
					case "writer-revoked":
						f.SetCanWrite(repo, "writer", false)
						if authorityMode {
							f.SetWriteGrant(repo, 21, "read")
						}
					case "stale-approval":
						must(t, f.Approve(repo, pr.Number, forge.Approval{User: "writer", SHA: "old-head", CanWrite: true}))
						if authorityMode {
							native.Reviews[0].CommitID = "old-head"
							f.SetNativeReviews(repo, pr.Number, native)
						}
					case "restart", "restart-closed":
						s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
						must(t, err)
						w = s.queues[0]
						if outcome == "restart-closed" {
							must(t, f.ClosePR(repo, pr.Number))
						}
					}
					if outcome == "pass" || outcome == "missing" || outcome == "restart" || outcome == "writer-revoked" || outcome == "stale-approval" {
						f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
					}
					// No forced refresh: held requests must be polled even with unchanged PR UpdatedAt.
					must(t, s.reconcile(t.Context(), w, false))
					state = lifecycleState(t, s, w)
					if _, held := state.HeldRequest(pr.Number); held {
						t.Fatal("request remained held after terminal observation")
					}
					wantAdmission := outcome == "pass" || outcome == "missing" || outcome == "restart"
					if (len(state.Entries) == 1) != wantAdmission {
						t.Fatalf("admission=%v entries=%+v", wantAdmission, state.Entries)
					}
					if outcome == "fail" {
						if !slices.ContainsFunc(queue.Plan(w.plannerConfig(s.options.PollInterval), state, time.Now()), func(a queue.Action) bool {
							r, ok := a.(queue.ReportStatus)
							return ok && r.Status == "blocked" && r.Reason == string(queue.NotEligible)
						}) {
							t.Fatal("false condition did not post existing blocked outcome")
						}
					}
					if !wantAdmission {
						must(t, f.SetPRMetadata(repo, pr.Number, "main", false, "contributor"))
						current, err := f.PullRequest(t.Context(), repo, pr.Number)
						must(t, err)
						f.SetCheckRuns(repo, current.HeadSHA, []forge.CheckRun{{Name: "sign-off", State: forge.Success}})
						must(t, s.reconcile(t.Context(), w, true))
						if len(lifecycleState(t, s, w).Entries) != 0 {
							t.Fatal("cancelled request resurrected after conditions passed")
						}
					}
				})
			})
		}
	}
}

func TestEntryDecisionThreeValuedGroups(t *testing.T) {
	for _, tc := range []struct {
		yaml    string
		pending bool
	}{
		{"[check-success = sign-off]", true},
		{"[or: [check-success = sign-off, draft]]", true},
		{"[not: {or: [check-success = sign-off, draft]}]", true},
		{"['-draft', check-success = sign-off]", true},
		{"[draft, check-success = sign-off]", false},
		{"[or: [check-success = sign-off, {and: [draft, check-success = other]}]]", true},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			cfg, _, err := config.Load([]byte(repositoryConfig + "    enter_when: " + tc.yaml + "\n"))
			must(t, err)
			met, pending := entryDecision(cfg.Queues[0].EnterWhen, condition.Snapshot{"draft": condition.Bool(false)})
			if met || (pending != "") != tc.pending {
				t.Fatalf("met=%v pending=%q", met, pending)
			}
		})
	}
}
