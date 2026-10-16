package executor_test

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRequiredChecksMustBeExplicit(t *testing.T) {
	h := setup(t)
	h.x.RequiredChecks = nil
	var events []queue.Event
	if err := h.x.Execute(context.Background(), h.buildAction(), record(&events)); err == nil || len(events) != 0 {
		t.Fatalf("empty required checks accepted: %v, %v", err, events)
	}
	h.x.AllowNoRequiredChecks = true
	must(t, h.x.Execute(context.Background(), h.buildAction(), record(&events)))
}

func TestRandomAttemptIDsAndGenerationFailure(t *testing.T) {
	h := setup(t)
	h.x.NewID = nil
	seen := make(map[string]bool)
	for range 32 {
		// A crash after the start record leaves the push unexecuted. Each new queue
		// generation must still mint a fresh ID, even with identical action keys.
		var id string
		err := h.x.Execute(context.Background(), queue.Land{BatchID: "b1", FenceToken: h.fence}, func(e queue.Event) error { id = e.(queue.LandingStarted).AttemptID; return errLog })
		decoded, decodeErr := hex.DecodeString(id)
		if !errors.Is(err, errLog) || decodeErr != nil || len(decoded) < 16 || seen[id] {
			t.Fatalf("invalid or reused random attempt %q: %v", id, err)
		}
		seen[id] = true
	}
	h.x.NewID = func() (string, error) { return "", errIO }
	var events []queue.Event
	if err := h.x.Execute(context.Background(), queue.Land{}, record(&events)); !errors.Is(err, errIO) || len(events) != 0 {
		t.Fatalf("ID failure started a landing: %v, %v", err, events)
	}
}

func TestFenceDerivedFromBase(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	const base = "release/stable"
	const fence = "openmerge/fence/" + base
	must(t, h.f.SetRef(repo, base, h.base))
	token, err := h.f.EnsureFence(ctx, repo, fence)
	must(t, err)
	h.x.Base = base
	a := queue.Land{BatchID: "release", ExpectedBaseSHA: h.base, TestedSHA: h.tested(t), FenceToken: token, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}
	must(t, h.x.Execute(ctx, a, func(queue.Event) error { return nil }))
	if h.read(t, base) != a.TestedSHA || h.read(t, fence) == token || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence {
		t.Fatal("landing used a fence from a different base")
	}
}

func TestLandRequiresFenceSetup(t *testing.T) {
	for _, token := range []string{"empty", "stale"} {
		t.Run(token, func(t *testing.T) {
			h := setup(t)
			ctx := context.Background()
			a := queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: h.tested(t), PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}
			if token == "stale" {
				a.FenceToken = h.fence
			}
			must(t, h.f.DeleteRef(ctx, repo, fenceRef))
			var events []queue.Event
			err := h.x.Execute(ctx, a, record(&events))
			want := []queue.Event{queue.LandingStarted{BatchID: a.BatchID, AttemptID: "attempt", FenceToken: a.FenceToken, At: epoch}}
			if err == nil || !reflect.DeepEqual(events, want) || h.read(t, "main") != h.base {
				t.Fatalf("missing fence allowed a landing: %v, events %#v", err, events)
			}
			if _, err := h.f.ReadRef(ctx, repo, fenceRef); err == nil {
				t.Fatal("Land bootstrapped the missing fence")
			}
			pr, err := h.f.PullRequest(ctx, repo, 1)
			must(t, err)
			if pr.Closed {
				t.Fatal("failed landing closed the PR")
			}
		})
	}
}

func TestResolveRequiresMatchingTestedSHA(t *testing.T) {
	for _, tested := range []string{"", "wrong-tested-sha"} {
		t.Run(tested, func(t *testing.T) {
			h := setup(t)
			a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
			message := "openmerge fence land/attempt"
			if tested != "" {
				message += " tested=" + tested
			}
			pr, err := h.f.PushPR(repo, 2, message)
			must(t, err)
			must(t, h.f.SetRef(repo, fenceRef, pr.HeadSHA))
			var events []queue.Event
			err = h.x.Execute(context.Background(), a, record(&events))
			var tampered *executor.ErrFenceTampered
			if !errors.As(err, &tampered) || len(events) != 0 || h.read(t, "main") != h.base {
				t.Fatalf("unverified tested SHA acknowledged: %v, %v", err, events)
			}
		})
	}
}

func TestLatestCheckRuns(t *testing.T) {
	for _, tc := range []struct {
		name              string
		checks            []forge.CheckRun
		allow             bool
		terminal, success bool
	}{
		{name: "rerun pending supersedes failed", checks: []forge.CheckRun{{ID: 2, State: forge.Pending}, {ID: 1, State: forge.Failure}}},
		{name: "rerun pending supersedes success", checks: []forge.CheckRun{{ID: 2, State: forge.Pending}, {ID: 1, State: forge.Success}}},
		{name: "rerun success supersedes failed", checks: []forge.CheckRun{{ID: 2, State: forge.Success}, {ID: 1, State: forge.Failure}}, terminal: true, success: true},
		{name: "latest failure supersedes pending", checks: []forge.CheckRun{{ID: 2, State: forge.Failure}, {ID: 1, State: forge.Pending}}, terminal: true},
		{name: "cancelled fails", checks: []forge.CheckRun{{ID: 1, State: forge.Cancelled}}, terminal: true},
		{name: "rerun replaces cancelled", checks: []forge.CheckRun{{ID: 2, State: forge.Success}, {ID: 1, State: forge.Cancelled}}, terminal: true, success: true},
		{name: "skipped fails by default", checks: []forge.CheckRun{{ID: 1, State: forge.Skipped}}, terminal: true},
		{name: "neutral fails by default", checks: []forge.CheckRun{{ID: 1, State: forge.Neutral}}, terminal: true},
		{name: "skipped explicitly allowed", checks: []forge.CheckRun{{ID: 1, State: forge.Skipped}}, allow: true, terminal: true, success: true},
		{name: "neutral explicitly allowed", checks: []forge.CheckRun{{ID: 1, State: forge.Neutral}}, allow: true, terminal: true, success: true},
		{name: "allow does not bless cancelled", checks: []forge.CheckRun{{ID: 1, State: forge.Cancelled}}, allow: true, terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			h.x.RequiredChecks = []string{"ci"}
			h.x.AllowSkippedNeutral = tc.allow
			for i := range tc.checks {
				tc.checks[i].Name = "ci"
			}
			h.f.SetCheckRuns(repo, "tested", tc.checks)
			var events []queue.Event
			must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: "tested"}, record(&events)))
			var want []queue.Event
			if tc.terminal {
				want = append(want, queue.ChecksCompleted{TestedSHA: "tested", Success: tc.success})
			}
			want = append(want, queue.ChecksRefreshed{BatchID: "batch", TestedSHA: "tested", At: epoch})
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}

func TestSquashRebaseClosingEffectSurvivesLostResult(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			h := setup(t)
			ctx := context.Background()
			s := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			s = queue.Apply(s, queue.PRObserved{PR: 1, HeadSHA: h.prs[0].HeadSHA, Eligible: true, Trusted: true})
			s = queue.Apply(s, queue.EnqueueRequested{PR: 1, At: epoch})
			cfg := queue.Config{MergeMethod: method}
			emit := func(e queue.Event) error { s = queue.Apply(s, e); return nil }
			must(t, h.x.Execute(ctx, queue.Plan(cfg, s, epoch)[0], emit))
			s = queue.Apply(s, queue.ChecksCompleted{TestedSHA: s.Active.TestedSHA, Success: true})
			tested := s.Active.TestedSHA
			must(t, h.x.Execute(ctx, queue.Plan(cfg, s, epoch)[0], emit))
			pr, err := h.f.PullRequest(ctx, repo, 1)
			must(t, err)
			if method == queue.Merge && !pr.Closed {
				t.Fatal("merge heads reachable but PR left open")
			}
			var closing *queue.MarkLanded
			for _, action := range queue.Plan(cfg, s, epoch) {
				if a, ok := action.(queue.MarkLanded); ok {
					closing = &a
				}
			}
			if method == queue.Merge {
				if closing != nil {
					t.Fatal("merge landing unnecessarily emits MarkLanded")
				}
				return
			}
			if closing == nil || closing.HeadSHA != h.prs[0].HeadSHA || closing.LandedSHA != tested {
				t.Fatalf("missing pinned closing effect: %+v", closing)
			}
			h.f.FailNext("MarkLanded", errIO)
			if err := h.x.Execute(ctx, *closing, emit); !errors.Is(err, errIO) {
				t.Fatalf("closing error: %v", err)
			}
			if err := h.x.Execute(ctx, *closing, func(queue.Event) error { return errLog }); !errors.Is(err, errLog) {
				t.Fatalf("closing emit error: %v", err)
			}
			restarted := *h.x
			must(t, restarted.Execute(ctx, *closing, emit))
			for _, a := range queue.Plan(cfg, s, epoch.Add(time.Hour)) {
				if _, ok := a.(queue.MarkLanded); ok {
					t.Fatal("acknowledged closing effect reappeared")
				}
			}
			pr, err = h.f.PullRequest(ctx, repo, 1)
			must(t, err)
			if !pr.Closed || len(h.f.Comments(repo, 1)) != 1 {
				t.Fatal("closing retries lost or duplicated effect")
			}
		})
	}
}
