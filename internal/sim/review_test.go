package sim

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func readyLanding(t *testing.T, s *simulation) queue.Land {
	t.Helper()
	for n := 0; n < 1000; n++ {
		for _, a := range queue.Plan(s.cfg.Planner, s.state, s.now) {
			if land, ok := a.(queue.Land); ok {
				return land
			}
		}
		done, err := s.step()
		if err == nil {
			err = s.checkInvariants()
		}
		if err != nil || done {
			t.Fatalf("no landing ready: done=%v, err=%v", done, err)
		}
	}
	t.Fatal("landing did not become ready")
	return queue.Land{}
}

func onePR(t *testing.T) *simulation {
	t.Helper()
	cfg := smallConfig()
	cfg.PRs = 1
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The original no-op and wrong-SHA executor mutants emitted Landed even though
// main had not moved to the tested commit. Neither may count as a merged PR.
func TestLandedRequiresMatchingForgeLanding(t *testing.T) {
	for _, mutant := range []string{"no-op push", "PR head instead of tested SHA", "Landed without push"} {
		t.Run(mutant, func(t *testing.T) {
			s := onePR(t)
			land := readyLanding(t, s)
			s.record(queue.LandingStarted{BatchID: land.BatchID, AttemptID: "mutant", FenceToken: land.FenceToken, At: s.now})
			if mutant != "Landed without push" {
				target := land.ExpectedBaseSHA
				if mutant == "PR head instead of tested SHA" {
					target = land.PRs[0].HeadSHA
				}
				_, err := s.forge.Land(context.Background(), s.repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: land.ExpectedBaseSHA, TestedSHA: target, Fence: "openmerge/fence/main", ExpectedFence: land.FenceToken, NextFenceID: "land/mutant", PRs: []forge.BuildPR{{Number: 1, HeadSHA: land.PRs[0].HeadSHA}}})
				if mutant == "no-op push" {
					if !errors.Is(err, forge.ErrLeaseRejected) {
						t.Fatalf("no-op landing was not refused: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			s.record(queue.Landed{BatchID: land.BatchID, NewBaseSHA: land.TestedSHA})
			err := s.checkInvariants()
			if err == nil || s.metrics().Merged != 0 {
				t.Fatalf("credited forged Landed: err=%v, result=%+v", err, s.metrics())
			}
			if mutant != "PR head instead of tested SHA" && !strings.Contains(err.Error(), "no matching forge landing") {
				t.Fatal(err)
			}
		})
	}
}

func TestForgeLandingRequiresDurableEventAfterRecovery(t *testing.T) {
	s := onePR(t)
	land := readyLanding(t, s)
	err := s.executor.Execute(context.Background(), land, func(e queue.Event) error {
		if _, ok := e.(queue.Landed); ok {
			return nil
		} // mutant silently drops the event
		s.record(e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkSettled(); err == nil || !strings.Contains(err.Error(), "no matching Landed after recovery settled") {
		t.Fatalf("missing converse check: %v", err)
	}
	if s.metrics().Merged != 0 {
		t.Fatal("unrecorded landing credited")
	}
}

// Duplicate execution resumes an old Land action after its first copy times
// out and recovery fences it off. The token-source and missing-lease mutants
// must both be rejected by the simulator, rather than passing the exit test.
func TestSimulatorDetectsFenceMutants(t *testing.T) {
	for _, mutant := range []string{"correct delayed push", "correct duplicate execution", "fresh executor token", "fake ignores fence lease"} {
		t.Run(mutant, func(t *testing.T) {
			s := onePR(t)
			land := readyLanding(t, s)
			switch mutant {
			case "fresh executor token":
				s.executor.Git = freshFenceGit{Git: s.executor.Git}
			case "fake ignores fence lease":
				s.forge.DisableFenceLeaseForTesting()
			}
			s.cfg.Faults.DelayedPushRate = 1
			duplicate := 0
			if mutant == "correct duplicate execution" || mutant == "fresh executor token" {
				duplicate = 1
			}
			s.cfg.Faults.DuplicateExecutionRate = float64(duplicate)
			if err := s.execute(land); err != nil {
				t.Fatal(err)
			}
			s.cfg.Faults.DelayedPushRate, s.cfg.Faults.DuplicateExecutionRate = 0, 0
			r, err := s.run()
			if strings.HasPrefix(mutant, "correct") {
				if err != nil || r.Merged != 1 || r.DelayedPushes != 1 || r.DuplicateExecutions != duplicate || r.RejectedReplays != 1 || len(s.abandoned) != 1 {
					t.Fatalf("fault not exercised/recovered: %+v, %v", r, err)
				}
			} else if mutant == "fresh executor token" {
				// The start-event gate cancels this duplicate before its forge call.
				// TestAbandonmentBeforePush covers the fresh-token bug after a valid start.
				if err != nil || r.Merged != 1 || r.RejectedReplays != 1 {
					t.Fatalf("duplicate bypassed the start gate: %+v, %v", r, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "abandoned attempt") || r.Merged != 0 {
				t.Fatalf("simulator missed fence mutant: %+v, %v", r, err)
			} else {
				t.Logf("detected %s: %v", mutant, err)
			}
		})
	}
}

// Preserve the reviewer's exact pause window: abandonment occurs between the
// durable LandingStarted and the executor's call to Git.Land.
func TestAbandonmentBeforePush(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured token", true: "fresh token mutant"}[fresh], func(t *testing.T) {
			s := onePR(t)
			land := readyLanding(t, s)
			if fresh {
				s.executor.Git = freshFenceGit{Git: s.executor.Git}
			}
			err := s.executor.Execute(context.Background(), land, func(e queue.Event) error {
				s.record(e)
				if started, ok := e.(queue.LandingStarted); ok {
					// The reviewer's pause window is itself the disturbance that justifies abandoning.
					s.injectFault("landing/" + land.BatchID)
					return s.executor.Execute(context.Background(), queue.ResolveLanding{BatchID: land.BatchID, AttemptID: started.AttemptID, TestedSHA: land.TestedSHA, FenceToken: started.FenceToken}, func(e queue.Event) error { s.record(e); return s.violation })
				}
				return s.violation
			})
			inv := s.checkInvariants()
			if fresh {
				if inv == nil || !strings.Contains(inv.Error(), "abandoned attempt") {
					t.Fatalf("fresh token mutant passed: %v, %v", err, inv)
				}
			} else {
				tip, _ := s.forge.ReadRef(context.Background(), s.repo, "main")
				if err != nil || inv != nil || tip != s.rootSHA {
					t.Fatalf("abandoned push accepted: %s, %v, %v", tip, err, inv)
				}
			}
		})
	}
}

func TestAdvanceFenceErrorCannotCertifyAbandonment(t *testing.T) {
	for _, mutant := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "ignore error mutant"}[mutant], func(t *testing.T) {
			s := onePR(t)
			land := readyLanding(t, s)
			s.cfg.Faults.FailFirst = map[string]int{"Land": 1, "AdvanceFence": 1}
			if err := s.execute(land); err != nil {
				t.Fatal(err)
			}
			if mutant {
				next, err := s.boundary.AdvanceFence(context.Background(), s.repo, "openmerge/fence/main", land.FenceToken, "abandon/"+s.state.Active.LandingAttempt.ID)
				if !errors.Is(err, errForge) {
					t.Fatalf("fault not injected: %v", err)
				}
				s.record(queue.LandingAbandoned{BatchID: land.BatchID, NewFence: next})
				if err := s.checkInvariants(); err == nil || !strings.Contains(err.Error(), "without successful AdvanceFence") {
					t.Fatalf("false abandonment accepted: %v", err)
				}
			} else {
				r, err := s.run()
				if err != nil || r.Merged != 1 || r.FaultsByMethod["AdvanceFence"] != 1 || r.FaultsInjected != r.FaultsRecovered {
					t.Fatalf("did not recover: %+v, %v", r, err)
				}
			}
		})
	}
}

func TestEveryForgeCallBoundary(t *testing.T) {
	s := onePR(t)
	s.cfg.Faults.Methods = make(map[string]float64)
	calls := map[string]func() error{
		"PullRequest":  func() error { _, e := s.boundary.PullRequest(context.Background(), s.repo, 1); return e },
		"CheckRuns":    func() error { _, e := s.boundary.CheckRuns(context.Background(), s.repo, ""); return e },
		"DeleteRef":    func() error { return s.boundary.DeleteRef(context.Background(), s.repo, "") },
		"ReportStatus": func() error { return s.boundary.ReportStatus(context.Background(), s.repo, 1, "", forge.Status{}) },
		"MarkLanded":   func() error { return s.boundary.MarkLanded(context.Background(), s.repo, 1, "") },
		"Comment":      func() error { return s.boundary.Comment(context.Background(), s.repo, 1, "", "") },
		"BuildTestRef": func() error {
			_, e := s.boundary.BuildTestRef(context.Background(), s.repo, forge.BuildRequest{})
			return e
		},
		"Land":         func() error { _, e := s.boundary.Land(context.Background(), s.repo, forge.LandRequest{}); return e },
		"EnsureFence":  func() error { _, e := s.boundary.EnsureFence(context.Background(), s.repo, ""); return e },
		"AdvanceFence": func() error { _, e := s.boundary.AdvanceFence(context.Background(), s.repo, "", "", ""); return e },
		"ReadFence":    func() error { _, _, _, e := s.boundary.ReadFence(context.Background(), s.repo, ""); return e },
		"ReadRef":      func() error { _, e := s.boundary.ReadRef(context.Background(), s.repo, ""); return e },
		"IsAncestor":   func() error { _, e := s.boundary.IsAncestor(context.Background(), s.repo, "", ""); return e },
	}
	for _, method := range forgeMethods {
		t.Run(method, func(t *testing.T) {
			s.cfg.Faults.Methods[method] = 1
			if err := calls[method](); !errors.Is(err, errForge) {
				t.Fatalf("boundary not faulted: %v", err)
			}
			delete(s.cfg.Faults.Methods, method)
			if s.result.FaultsByMethod[method] != 1 {
				t.Fatal("fault not counted")
			}
		})
	}
	if s.result.FaultsInjected != len(forgeMethods) {
		t.Fatal("missing method")
	}
}

func TestEveryActionRecoversItsMethodFault(t *testing.T) {
	for _, target := range []string{"BuildTestRef", "RefreshChecks", "Land", "ResolveLanding", "DeleteRef", "ReportStatus", "Dequeue", "MarkLanded"} {
		t.Run(target, func(t *testing.T) {
			cfg := smallConfig()
			cfg.PRs = 1
			if target == "Dequeue" {
				cfg.BrokenRate = 1
			}
			if target == "MarkLanded" {
				cfg.MergeMethod = queue.Squash
			}
			if target == "ResolveLanding" {
				cfg.Faults.LandingCrashRate = 1
			}
			s, err := newSimulation(cfg)
			if err != nil {
				t.Fatal(err)
			}
			injected := false
			method := map[string]string{"BuildTestRef": "BuildTestRef", "RefreshChecks": "CheckRuns", "Land": "Land", "ResolveLanding": "ReadFence", "DeleteRef": "DeleteRef", "ReportStatus": "ReportStatus", "Dequeue": "ReportStatus", "MarkLanded": "MarkLanded"}[target]
			for n := 0; n < 1000; n++ {
				for _, a := range queue.Plan(s.cfg.Planner, s.state, s.now) {
					if reflect.TypeOf(a).Name() == target && !injected {
						s.forge.FailNext(method, errForge)
						s.injectFault(faultScope(a))
						injected = true
						break
					}
				}
				done, err := s.step()
				if err == nil {
					err = s.checkInvariants()
				}
				if err != nil {
					t.Fatal(err)
				}
				if done {
					r := s.metrics()
					if !injected || r.FaultsInjected != r.FaultsRecovered {
						t.Fatalf("fault not recovered: %+v", r)
					}
					return
				}
			}
			t.Fatal("wedge")
		})
	}
}

func TestWebhookReconciliationLatency(t *testing.T) {
	for _, duration := range []time.Duration{120 * time.Second, 130 * time.Second} {
		for _, drop := range []float64{0, 1} {
			cfg := smallConfig()
			cfg.PRs = 1
			cfg.CIDuration = duration
			cfg.DropWebhookRate = drop
			_, r := runScenario(t, cfg)
			want := duration
			if drop == 1 && duration == 130*time.Second {
				want = 180 * time.Second
			}
			if r.MeanTimeToMerge != want {
				t.Fatalf("ci=%s drop=%g mean=%s want=%s", duration, drop, r.MeanTimeToMerge, want)
			}
		}
	}
}

func TestTwoCommitRebaseCoveredByTipCI(t *testing.T) {
	s := onePR(t)
	s.cfg.MergeMethod, s.cfg.Planner.MergeMethod = queue.Rebase, queue.Rebase
	// Pause after arrival but before admission, then give the PR a second commit.
	for s.prs[1].head == "" {
		if _, err := s.step(); err != nil {
			t.Fatal(err)
		}
	}
	pr, err := s.forge.PushPR(s.repo, 1, "second commit")
	if err != nil {
		t.Fatal(err)
	}
	s.prs[1].head = pr.HeadSHA
	s.record(queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
	r, err := s.run()
	if err != nil || r.Merged != 1 || r.CIRuns != 1 {
		t.Fatalf("rebase falsely rejected: %+v, %v", r, err)
	}
	history, err := s.forge.History(s.repo, "main")
	if err != nil || len(history) != 3 {
		t.Fatalf("expected root plus two rebased commits: %d, %v", len(history), err)
	}
	if len(s.tests[history[2].SHA].covered) != 2 {
		t.Fatal("CI did not cover both commits")
	}
}

func TestRepushesAndSharedFileConflicts(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 6
	cfg.RepushRate = 1
	cfg.ConflictRate = 1
	s, r := runScenario(t, cfg)
	if r.Dequeued[queue.HeadChanged] != cfg.PRs || r.Dequeued[queue.Conflict] != cfg.PRs/2 || r.Merged != cfg.PRs/2 || r.StillQueued != 0 {
		t.Fatalf("workload not exercised: %+v", r)
	}
	admissions := 0
	for _, entry := range s.events {
		if _, ok := entry.Event.(queue.EnqueueRequested); ok {
			admissions++
		}
	}
	if admissions != 2*cfg.PRs+r.Untrusted {
		t.Fatalf("re-push did not re-enqueue: %d", admissions)
	}
}

func TestMedianAndReadableJSON(t *testing.T) {
	s := onePR(t)
	s.prs = make([]pullRequest, 5)
	for i, seconds := range []int{1, 3, 8, 10} {
		s.prs[i+1].enqueued = s.start
		s.prs[i+1].firstEnqueued = s.start.Add(-2 * time.Second)
		s.prs[i+1].landed = s.start.Add(time.Duration(seconds) * time.Second)
	}
	r := s.metrics()
	if r.MedianTimeToMerge != 5500*time.Millisecond || r.MeanTimeToMerge != 5500*time.Millisecond || r.P95TimeToMerge != 10*time.Second ||
		r.MedianTimeFromFirstEnqueue != 7500*time.Millisecond || r.P95TimeFromFirstEnqueue != 12*time.Second {
		t.Fatalf("incorrect statistics: %+v", r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	cfg := got["config"].(map[string]any)
	if got["median_time_to_merge_seconds"] != 5.5 || cfg["seed"] != float64(s.cfg.Seed) || cfg["ci_duration_seconds"] != 120.0 || strings.Contains(string(data), "_ns") {
		t.Fatalf("unreadable/unidentified JSON: %s", data)
	}
	if got["median_time_from_first_enqueue_seconds"] != 7.5 || got["p95_time_from_first_enqueue_seconds"] != 12.0 {
		t.Fatalf("missing first-enqueue statistics: %s", data)
	}
}

func TestExternalPushesRebuildQueue(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
		for _, mutant := range []bool{false, true} {
			t.Run(string(method)+map[bool]string{false: "/base lease", true: "/fresh base mutant"}[mutant], func(t *testing.T) {
				cfg := smallConfig()
				cfg.PRs, cfg.ExternalPushRate, cfg.MergeMethod = 1, 1, method
				s, err := newSimulation(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if mutant {
					s.executor.Git = freshBaseGit{Git: s.executor.Git}
				}
				r, err := s.run()
				if mutant {
					if err == nil || !strings.Contains(err.Error(), "main history lost observed tip") || r.Merged != 0 {
						t.Fatalf("simulator missed lost external commit: %v; result=%+v", err, r)
					}
					return
				}
				if err != nil {
					t.Fatalf("simulator rejected mutation: %v; result=%+v", err, r)
				}
				if r.Merged != 1 || r.ExternalPushes != 2 || r.CIRuns != 3 || len(s.abandoned) != 1 {
					t.Fatalf("external pushes did not rebuild and recover: %+v", r)
				}
				moves := 0
				for _, entry := range s.events {
					if _, ok := entry.Event.(queue.BaseMoved); ok {
						moves++
					}
				}
				if moves != r.ExternalPushes || len(s.mainTips) != 4 {
					t.Fatalf("main transitions missing: webhooks=%d, tips=%v", moves, s.mainTips)
				}
				for _, tip := range s.mainTips {
					retained, err := s.forge.IsAncestor(context.Background(), s.repo, tip, s.mainSHA)
					if err != nil || !retained {
						t.Fatalf("observed main tip disappeared: %s, %v", tip, err)
					}
				}
			})
		}
	}
}

func TestMissingBatchPRRejected(t *testing.T) {
	for _, scenario := range []struct {
		name, want                    string
		method                        queue.MergeMethod
		reportMembership, keepContent bool
	}{
		{name: "squash membership", method: queue.Squash, want: "CI membership"},
		{name: "squash content", method: queue.Squash, reportMembership: true, want: "lacks intended PR 1 content"},
		{name: "merge ancestry", method: queue.Merge, reportMembership: true, keepContent: true, want: "lacks intended PR 1 head"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := onePR(t)
			s.cfg.MergeMethod, s.cfg.Planner.MergeMethod = scenario.method, scenario.method
			s.executor.Git = missingPRGit{Git: s.executor.Git, sim: s, reportMembership: scenario.reportMembership, keepContent: scenario.keepContent}
			r, err := s.run()
			if err == nil || !strings.Contains(err.Error(), scenario.want) || r.Merged != 0 {
				t.Fatalf("simulator credited incomplete batch: %v; result=%+v", err, r)
			}
		})
	}
}

func TestForeignBaseEqualityDoesNotResolveLanding(t *testing.T) {
	for _, forged := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolve from fence", true: "forged base equality"}[forged], func(t *testing.T) {
			s := onePR(t)
			land := readyLanding(t, s)
			s.cfg.Faults.FailFirst = map[string]int{"Land": 1}
			if err := s.execute(land); err != nil {
				t.Fatal(err)
			}
			// The first push failed before writing. A different actor now
			// fast-forwards main to the tested SHA without moving our fence.
			if err := s.forge.PushRef(s.repo, "main", land.TestedSHA); err != nil {
				t.Fatal(err)
			}
			if err := s.externalBaseMoved(land.TestedSHA); err != nil {
				t.Fatal(err)
			}
			if forged {
				s.record(queue.Landed{BatchID: land.BatchID, NewBaseSHA: land.TestedSHA, NewFence: land.FenceToken})
			}
			r, err := s.run()
			if forged {
				if err == nil || !strings.Contains(err.Error(), "no matching forge landing") || r.Merged != 0 {
					t.Fatalf("base equality certified a landing: %v; result=%+v", err, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("simulator rejected mutation: %v; result=%+v", err, r)
			}
			if r.Merged != 1 || s.abandoned[land.TestedSHA] == "" || r.FaultsInjected != r.FaultsRecovered {
				t.Fatalf("foreign base push did not abandon and rebuild: %+v", r)
			}
			for _, entry := range s.events {
				if event, ok := entry.Event.(queue.Landed); ok && event.BatchID == land.BatchID {
					t.Fatal("foreign push was credited to the uncertain attempt")
				}
			}
		})
	}
}

func TestTimeFromFirstEnqueueSurvivesRepush(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.RepushRate = 1, 1
	s, r := runScenario(t, cfg)
	var admissions []time.Time
	for _, entry := range s.events {
		if event, ok := entry.Event.(queue.EnqueueRequested); ok && event.PR == 1 {
			admissions = append(admissions, event.At)
		}
	}
	p := s.prs[1]
	if len(admissions) != 2 || p.firstEnqueued != admissions[0] || p.enqueued != admissions[1] ||
		r.MedianTimeFromFirstEnqueue != p.landed.Sub(admissions[0]) || r.P95TimeFromFirstEnqueue != r.MedianTimeFromFirstEnqueue ||
		r.MedianTimeToMerge != p.landed.Sub(admissions[1]) || r.MedianTimeFromFirstEnqueue <= r.MedianTimeToMerge {
		t.Fatalf("re-enqueue lost the initial wait: admissions=%v; result=%+v", admissions, r)
	}
}

// freshBaseGit models a push that discards the captured base lease.
type freshBaseGit struct{ forge.Git }

func (g freshBaseGit) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	sha, err := g.ReadRef(ctx, repo, req.Base)
	if err != nil {
		return "", err
	}
	req.ExpectedBaseSHA = sha
	return g.Git.Land(ctx, repo, req)
}

// missingPRGit builds a new commit without replaying the PR. It can report
// truthful empty membership, lie about membership, or copy content without the
// head ancestry required for the merge method.
type missingPRGit struct {
	forge.Git
	sim                           *simulation
	reportMembership, keepContent bool
}

func (g missingPRGit) BuildTestRef(_ context.Context, repo forge.RepoRef, req forge.BuildRequest) (string, error) {
	if err := g.sim.forge.SetRef(repo, req.Ref, req.BaseSHA); err != nil {
		return "", err
	}
	var files map[string]string
	if g.keepContent {
		commit, err := g.sim.forge.Commit(repo, req.PRs[0].HeadSHA)
		if err != nil {
			return "", err
		}
		files = commit.Files
	}
	sha, err := g.sim.forge.PushBase(repo, req.Ref, "incomplete batch "+req.BatchID, files)
	if err != nil {
		return "", err
	}
	var prs []int
	if g.reportMembership {
		for _, pr := range req.PRs {
			prs = append(prs, pr.Number)
		}
	}
	g.sim.startCI(sha, prs)
	return sha, nil
}

// freshFenceGit is the "executor re-reads the fence" mutant: it discards the
// fence token captured in LandingStarted and leases on whatever the fence is now.
// A correct executor must never behave this way, and the simulator must catch it.
type freshFenceGit struct{ forge.Git }

func (g freshFenceGit) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	sha, _, _, err := g.ReadFence(ctx, repo, req.Fence)
	if err != nil {
		return "", err
	}
	req.ExpectedFence = sha
	return g.Git.Land(ctx, repo, req)
}
