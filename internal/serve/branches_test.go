package serve

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func managedRepository(f *fake.Forge, name string) Repository {
	return Repository{Installation: "7", Name: name,
		Branches: func(ctx context.Context) ([]string, error) { return f.ListBranches(ctx, forge.RepoRef{Path: name}) },
		Target: func(base string) Target {
			return Target{Key: store.QueueKey{Installation: "7", Repository: name, BaseBranch: base}, Driver: f, Git: f, BotLogin: fake.Identity}
		},
	}
}

func managedFixture(t *testing.T, contents string, options Options) (*Server, *fake.Forge) {
	t.Helper()
	db := database(t)
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	tip, err := f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": contents})
	must(t, err)
	must(t, f.PushRef(repo, "release/v1", tip))
	options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := NewRepositories(t.Context(), db, []Repository{managedRepository(f, repo.Path)}, options)
	must(t, err)
	return s, f
}

func baseWorker(t *testing.T, s *Server, base string) *worker {
	t.Helper()
	for _, w := range s.workers() {
		if w.Key.BaseBranch == base {
			return w
		}
	}
	t.Fatalf("missing worker %s", base)
	return nil
}

func lifecycleState(t *testing.T, s *Server, w *worker) queue.State {
	t.Helper()
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	return state
}

func TestBranchExpansionLifecycleAndDynamicStatus(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{})
	main := baseWorker(t, s, "main")
	release := baseWorker(t, s, "release/v1")
	for _, w := range []*worker{main, release} {
		state := lifecycleState(t, s, w)
		if state.LifecycleState() != "active" || state.Fence == "" {
			t.Fatalf("not activated with fence: %+v", state)
		}
	}
	fence, id := lifecycleState(t, s, release).Fence, release.id
	must(t, f.DeleteRef(t.Context(), repo, "release/v1"))
	must(t, s.RefreshBranches(t.Context()))
	if lifecycleState(t, s, release).LifecycleState() != "dormant" {
		t.Fatal("deleted base not dormant")
	}
	must(t, s.cycle(t.Context(), release))
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, f.PushRef(repo, "release/v1", tip))
	must(t, f.PushRef(repo, "release/v2", tip))
	must(t, s.RefreshBranches(t.Context()))
	if release.id != id || lifecycleState(t, s, release).Fence != fence || lifecycleState(t, s, release).LifecycleState() != "active" {
		t.Fatal("recreation replaced identity or fence")
	}
	// The handler was built after startup but obtains keys on each request.
	handler := status.DynamicHandler(s.store, s.Keys, "", nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/queues", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"base":"release/v2"`) || !strings.Contains(response.Body.String(), `"state":"active"`) {
		t.Fatal(response.Body.String())
	}
}

func TestOverlapsBlockOnlyInvolvedQueuesAndProtectionIsPerBase(t *testing.T) {
	contents := releaseConfig + "  - name: narrow\n    base: release/v1\n    merge_when: [check-success = ci]\n"
	s, f := managedFixture(t, contents, Options{})
	if lifecycleState(t, s, baseWorker(t, s, "release/v1")).LifecycleState() != "overlapping" || lifecycleState(t, s, baseWorker(t, s, "main")).LifecycleState() != "active" {
		t.Fatal("overlap blocked independent queue")
	}
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, f.PushRef(repo, "release/v2", tip))
	must(t, s.RefreshBranches(t.Context()))
	if lifecycleState(t, s, baseWorker(t, s, "release/v2")).LifecycleState() != "overlapping" {
		t.Fatal("overlapping queue's other base activated")
	}
	_, err = f.PushBase(repo, "main", "remove overlap", map[string]string{".openmerge.yml": releaseConfig})
	must(t, err)
	checks := make(map[string]int)
	r := managedRepository(f, repo.Path)
	original := r.Target
	r.Target = func(base string) Target {
		target := original(base)
		target.CheckProtection = func(_ context.Context, method forge.MergeMethod) ([]string, []string, error) {
			checks[base]++
			if method != map[bool]forge.MergeMethod{true: forge.Squash, false: forge.Merge}[base == "main"] {
				t.Errorf("wrong method for %s: %s", base, method)
			}
			if base == "release/v1" {
				return []string{"missing fence coverage"}, nil, nil
			}
			return nil, nil, nil
		}
		return target
	}
	// Restart to install the same branch-specific adapter shape used by the CLI.
	restarted, err := NewRepositories(t.Context(), s.store, []Repository{r}, s.options)
	must(t, err)
	for _, base := range []string{"main", "release/v1", "release/v2"} {
		if checks[base] == 0 {
			t.Fatalf("no protection check for %s", base)
		}
	}
	if lifecycleState(t, restarted, baseWorker(t, restarted, "release/v1")).LifecycleState() != "settling-only" || lifecycleState(t, restarted, baseWorker(t, restarted, "release/v2")).LifecycleState() != "active" {
		t.Fatal("per-base protection not enforced")
	}
}

// recordLanding models a crash after the atomic push and before its result is
// appended. All I/O stays in the fake forge and a test-only Postgres schema.
func recordLanding(t *testing.T, s *Server, w *worker, f *fake.Forge, landed bool) string {
	t.Helper()
	state := lifecycleState(t, s, w)
	base, err := f.ReadRef(t.Context(), w.repo(), w.Key.BaseBranch)
	must(t, err)
	pr, err := f.OpenPR(w.repo(), 41, base, true)
	must(t, err)
	tested, err := f.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: "openmerge/queue/" + w.Key.BaseBranch + "/b1", BaseSHA: base, PRs: []forge.BuildPR{{Number: pr.Number, HeadSHA: pr.HeadSHA}}, Method: forge.Merge, BatchID: "b1"})
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{
		queue.BaseMoved{SHA: base}, queue.PRObserved{PR: pr.Number, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: pr.Number, At: time.Now()}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: pr.Number, HeadSHA: pr.HeadSHA}}, At: time.Now()},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: tested, Method: queue.Merge, At: time.Now()},
		queue.LandingStarted{BatchID: "b1", AttemptID: "crashed", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)},
	}, "")
	must(t, err)
	if landed {
		_, err = f.Land(t.Context(), w.repo(), forge.LandRequest{Base: w.Key.BaseBranch, ExpectedBaseSHA: base, TestedSHA: tested, Fence: "openmerge/fence/" + w.Key.BaseBranch, ExpectedFence: state.Fence, NextFenceID: "land/crashed", PRs: []forge.BuildPR{{Number: pr.Number, HeadSHA: pr.HeadSHA}}})
		must(t, err)
	}
	return tested
}

func TestRecreatedBranchSettlesBeforeReactivation(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{})
	w := baseWorker(t, s, "release/v1")
	tested := recordLanding(t, s, w, f, true)
	id := w.id
	must(t, f.DeleteRef(t.Context(), repo, w.Key.BaseBranch))
	must(t, s.RefreshBranches(t.Context()))
	if lifecycleState(t, s, w).LifecycleState() != "dormant" {
		t.Fatal("deleted worker remained active")
	}
	// Absence is not evidence that the earlier push failed.
	_ = s.cycle(t.Context(), w)
	if state := lifecycleState(t, s, w); state.Active == nil || state.Active.Phase != queue.Landing {
		t.Fatal("deletion invented an outcome")
	}
	must(t, f.PushRef(repo, w.Key.BaseBranch, tested))
	must(t, s.RefreshBranches(t.Context()))
	state := lifecycleState(t, s, w)
	if state.Active != nil || len(state.Entries) != 0 || state.LifecycleState() != "active" || id != w.id {
		t.Fatalf("recreated branch did not settle: %+v", state)
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	for _, e := range events {
		if _, abandoned := e.Event.(queue.LandingAbandoned); abandoned {
			t.Fatal("landed push reported abandoned")
		}
	}
}

func TestRetirementAndRestartOutsideAllowlistsSettleRealOutcome(t *testing.T) {
	for _, landed := range []bool{false, true} {
		t.Run(map[bool]string{false: "abandoned", true: "landed"}[landed], func(t *testing.T) {
			s, f := managedFixture(t, releaseConfig, Options{})
			w := baseWorker(t, s, "release/v1")
			recordLanding(t, s, w, f, landed)
			_, err := f.PushBase(repo, "main", "retire releases", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			must(t, s.RefreshBranches(t.Context()))
			if lifecycleState(t, s, w).LifecycleState() != "retired" {
				t.Fatal("removed queue not retired")
			}
			// Restart with both repository and base excluded. Credentials still serve
			// this installation, so its old attempt must be found in the store.
			other := forge.RepoRef{Path: "owner/other"}
			f.Init(other, "main", "openmerge/fence/main")
			_, err = f.PushBase(other, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			options := s.options
			options.BaseAllowlist = []string{"main"}
			options.RecoveryTarget = func(key store.QueueKey) (Target, bool) {
				return managedRepository(f, key.Repository).Target(key.BaseBranch), key.Installation == "7"
			}
			restarted, err := NewRepositories(t.Context(), s.store, []Repository{managedRepository(f, other.Path)}, options)
			must(t, err)
			recovered := baseWorker(t, restarted, "release/v1")
			if recovered.id != w.id || lifecycleState(t, restarted, recovered).LifecycleState() != "settling-only" {
				t.Fatal("attempt not recovered outside allowlists")
			}
			must(t, restarted.cycle(t.Context(), recovered))
			state := lifecycleState(t, restarted, recovered)
			if state.Active != nil && state.Active.Phase == queue.Landing {
				t.Fatal("attempt stranded")
			}
			events, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			found := false
			for _, e := range events {
				switch e.Event.(type) {
				case queue.Landed:
					if !landed {
						t.Fatal("invented landed outcome")
					}
					found = true
				case queue.LandingAbandoned:
					if landed {
						t.Fatal("invented abandoned outcome")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("no definitive outcome")
			}
		})
	}
}

func TestSettlementPrecedesFailedConfigurationAndStaleActions(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{})
	w := baseWorker(t, s, "main")
	recordLanding(t, s, w, f, true)
	must(t, s.pause(t.Context(), w, "operator-pause", "hold admissions"))
	// Place reporting ahead of resolution in durable action order.
	_, err := s.store.Plan(t.Context(), w.id, w.plannerConfig(time.Minute), time.Now().Add(-2*time.Hour))
	must(t, err)
	f.FailNext("DefaultBranch", errors.New("metadata unavailable"))
	must(t, s.cycle(t.Context(), w))
	if state := lifecycleState(t, s, w); state.Active != nil || len(state.Entries) != 0 {
		t.Fatal("configuration failure skipped fence settlement")
	}
}

func TestMainDeploymentReusesExactStoredKeyAndHistory(t *testing.T) {
	contents, err := os.ReadFile("../../.openmerge.yml")
	must(t, err)
	legacy, w, f, _ := fixture(t, string(contents))
	must(t, legacy.configuration(t.Context(), w))
	before, err := legacy.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	fence := lifecycleState(t, legacy, w).Fence
	options := legacy.options
	options.BaseAllowlist = []string{"main"}
	s, err := NewRepositories(t.Context(), legacy.store, []Repository{managedRepository(f, repo.Path)}, options)
	must(t, err)
	current := baseWorker(t, s, "main")
	if current.id != w.id || current.Key != w.Key || lifecycleState(t, s, current).Fence != fence || len(s.Keys()) != 1 {
		t.Fatal("live main partition replaced")
	}
	after, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	for i, e := range before {
		if after[i].ID != e.ID {
			t.Fatal("old event log changed")
		}
	}
}

func TestBranchRefreshFailuresNeverRetireAndAllowlistOnlyNarrows(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{BaseAllowlist: []string{"release/*", "unconfigured"}})
	if len(s.Keys()) != 1 || s.Keys()[0].BaseBranch != "release/v1" {
		t.Fatal("base flag created an unconfigured queue or failed to narrow")
	}
	w := baseWorker(t, s, "release/v1")
	recordLanding(t, s, w, f, true)
	f.FailNext("ListBranches", errors.New("page two failed"))
	if err := s.RefreshBranches(t.Context()); err == nil {
		t.Fatal("listing failure swallowed")
	}
	if lifecycleState(t, s, w).LifecycleState() != "active" {
		t.Fatal("failed listing removed queue")
	}
	must(t, s.cycle(t.Context(), w))
	if state := lifecycleState(t, s, w); state.Active != nil || state.PauseReason(queue.Config{}) != "branch-refresh-failed" {
		t.Fatal("read failure skipped settlement or resumed work")
	}
	must(t, s.RefreshBranches(t.Context()))
	if lifecycleState(t, s, w).PauseReason(queue.Config{}) != "" {
		t.Fatal("successful refresh failed to clear its own block")
	}
}

func TestBranchRefreshIntervalAndWebhookWakeStartOneWorkerPerBase(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{DiscoveryInterval: 10 * time.Millisecond, PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); must(t, <-done) }()
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	must(t, f.PushRef(repo, "release/timer", tip))
	waitFor := func(base string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			found := 0
			for _, k := range s.Keys() {
				if k.BaseBranch == base {
					found++
				}
			}
			if found == 1 {
				return
			}
			if found > 1 {
				t.Fatal("duplicate worker")
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("branch %s never discovered", base)
	}
	waitFor("release/timer")
	must(t, f.PushRef(repo, "release/webhook", tip))
	must(t, s.NotifyBranches(t.Context(), "7", repo.Path, "create-delivery"))
	waitFor("release/webhook")
}

func TestBroadSelectorExcludesInternalRefsAndRepositoryStatusListsBases(t *testing.T) {
	contents := "version: 1\nqueues:\n  - name: all\n    base: '**'\n    merge_when: [check-success = ci]\n"
	s, f := managedFixture(t, contents, Options{})
	must(t, s.RefreshBranches(t.Context()))
	if len(s.Keys()) != 2 {
		t.Fatalf("internal fences became queues: %v", s.Keys())
	}
	handler := status.DynamicHandler(s.store, s.Keys, "", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/queues/owner/repo", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"base":"main"`) || !strings.Contains(response.Body.String(), `"base":"release/v1"`) {
		t.Fatal(response.Body.String())
	}
	// Branch API failure cannot introduce a speculative matching base.
	f.FailNext("ListBranches", errors.New("unavailable"))
	if err := s.RefreshBranches(t.Context()); err == nil {
		t.Fatal("expected refresh error")
	}
	if len(s.Keys()) != 2 {
		t.Fatal("failed listing changed served partitions")
	}
}

func TestStartupSettlementIgnoresInvalidConfigurationAndMissingCredentials(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid-config", true: "credentials-unavailable"}[failRead], func(t *testing.T) {
			s, f := managedFixture(t, releaseConfig, Options{})
			w := baseWorker(t, s, "release/v1")
			recordLanding(t, s, w, f, true)
			_, err := f.PushBase(repo, "main", "break config", map[string]string{".openmerge.yml": "version: invalid"})
			must(t, err)
			if failRead {
				f.FailNext("ReadFence", errors.New("credentials unavailable"))
			}
			restarted, err := NewRepositories(t.Context(), s.store, []Repository{managedRepository(f, repo.Path)}, s.options)
			must(t, err)
			recovered := baseWorker(t, restarted, "release/v1")
			if failRead && lifecycleState(t, restarted, recovered).Active == nil {
				t.Fatal("credential loss invented landing outcome")
			}
			must(t, restarted.cycle(t.Context(), recovered))
			if state := lifecycleState(t, restarted, recovered); state.Active != nil || len(state.Entries) != 0 {
				t.Fatal("invalid config stranded landing")
			}
		})
	}
}

func TestIndependentBaseWorkersLandUnderDefaultBranchPolicy(t *testing.T) {
	s, f := managedFixture(t, releaseConfig, Options{})
	_, err := f.PushBase(repo, "release/v1", "release-only policy is ignored", map[string]string{".openmerge.yml": "version: invalid"})
	must(t, err)
	f.SetCanWrite(repo, "writer", true)
	for number, base := range map[int]string{1: "main", 2: "release/v1"} {
		tip, err := f.ReadRef(t.Context(), repo, base)
		must(t, err)
		_, err = f.OpenPR(repo, number, tip, true)
		must(t, err)
		f.AddComment(repo, number, "writer", "@openmerge queue")
	}
	f.OnTestRef(func(sha string, prs []int) {
		base, name := "main", "ci"
		if prs[0] == 2 {
			base, name = "release/v1", "release-ci"
		}
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: name, State: forge.Success, HeadBranch: "openmerge/queue/" + base + "/b1"}})
	})
	for _, base := range []string{"main", "release/v1"} {
		w := baseWorker(t, s, base)
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		if state.Active != nil || len(state.Entries) != 0 || state.BaseSHA == "" {
			t.Fatalf("%s did not land independently: %+v", base, state)
		}
		events, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		landed := false
		for _, event := range events {
			if _, ok := event.Event.(queue.Landed); ok {
				landed = true
			}
		}
		if !landed {
			t.Fatalf("%s never landed", base)
		}
	}
}
