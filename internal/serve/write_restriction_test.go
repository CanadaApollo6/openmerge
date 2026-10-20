package serve

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type restrictionReaderFunc func(context.Context, forge.WriteRestrictionInput) (authority.Observation, error)

func (f restrictionReaderFunc) ObserveWriteRestriction(ctx context.Context, in forge.WriteRestrictionInput) (authority.Observation, error) {
	return f(ctx, in)
}

func TestStartedRealGitLandingSettlesAcrossAuthorityViolation(t *testing.T) {
	db := database(t)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--bare", "--template=", remote)
	tree := git("", "--git-dir="+remote, "mktree")
	base := git("base\n", "--git-dir="+remote, "commit-tree", tree)
	head := git("source\n", "--git-dir="+remote, "commit-tree", tree, "-p", base)
	git("", "--git-dir="+remote, "update-ref", "refs/heads/main", base)
	git("", "--git-dir="+remote, "update-ref", "refs/pull/1/head", head)
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(dir, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: func(int) string { return "refs/pull/1/head" }})
	must(t, err)
	key := store.QueueKey{Installation: "7", Repository: "local/authority", BaseBranch: "main"}
	repo := forge.RepoRef{Path: key.Repository}
	fence, err := g.EnsureFence(t.Context(), repo, "openmerge/fence/main", forge.PushOptions{})
	must(t, err)
	tested, err := g.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "fixture-test", BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: head}}, Method: forge.Merge, BatchID: "b1"}, forge.PushOptions{})
	must(t, err)
	q, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	must(t, db.InitializeFence(t.Context(), q, fence))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	decoded, err := queue.DecodeEvent(raw)
	must(t, err)
	draft := decoded.(queue.WriteRestrictionVerified)
	selectors := []authority.BaseSelector{{DefaultBranch: true}}
	coverage, err := authority.CoverageDigest("main", selectors)
	must(t, err)
	binding := draft.Binding
	binding.InstallationID = 7
	binding.Repository = key.Repository
	binding.QueueKey = store.AuthorityQueueKey(key)
	binding.CoverageDigest = coverage
	pin := queue.PolicyAuthorityApplied{GoverningCommit: base, EffectiveDigest: binding.EffectiveDigest, Authority: authority.OpenMerge, DefaultBranch: "main", CoverageDigest: coverage, OpenMerge: &queue.AuthorityPin{Binding: binding}}
	at := time.Now().UTC().Add(-2 * time.Minute)
	events := []queue.Event{queue.ConfigApplied{Commit: base, Digest: binding.EffectiveDigest}, pin, queue.BaseMoved{SHA: base}, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: head}}, At: at}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: tested, At: at, Method: queue.Merge}, queue.ChecksCompleted{TestedSHA: tested, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: fence, At: at}}
	_, err = db.Append(t.Context(), q, events, "")
	must(t, err)
	// The push has started and its atomic base/fence transaction succeeds. No
	// completed result was recorded before the observer finds a definite failure.
	_, err = g.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: tested, Fence: "openmerge/fence/main", ExpectedFence: fence, NextFenceID: "land/attempt", PRs: []forge.BuildPR{{Number: 1, HeadSHA: head}}}, forge.PushOptions{})
	must(t, err)
	input := forge.WriteRestrictionInput{Repo: repo, Binding: binding, AppID: "5383059", BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}}
	o := authority.CloneObservation(draft.Evidence)
	a := authority.BypassActor{Kind: "user", ID: "unauthorized", Mode: "always"}
	o.Protections[0].BypassActors = append(o.Protections[0].BypassActors, a)
	id, err := ObserveWriteRestriction(t.Context(), db, q, restrictionReaderFunc(func(ctx context.Context, _ forge.WriteRestrictionInput) (authority.Observation, error) {
		// A concurrent append completes during the read: no queue lock spans I/O.
		_, _, err := db.Load(ctx, q)
		return o, err
	}), input)
	must(t, err)
	state, _, err := db.Load(t.Context(), q)
	must(t, err)
	if id == 0 || state.WriteRestrictionStatus().State != "violated" || state.Active == nil || state.Active.Phase != queue.Landing {
		t.Fatal("missing violation during landing")
	}
	s := &Server{store: db, options: Options{PollInterval: time.Millisecond, ActionTimeout: 10 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	w := &worker{Target: Target{Key: key, Git: g, Driver: fake.New()}, id: q, cfg: config.Queue{MergeMethod: queue.Merge}, configDigest: binding.EffectiveDigest, configTip: base}
	// Legacy checks cannot emit an authority clear or erase its source revision.
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	must(t, s.repositoryRules(t.Context(), w))
	afterRules, _, err := db.Load(t.Context(), q)
	must(t, err)
	if afterRules.RulesSourceRevision(queue.WriteRestriction) != state.RulesSourceRevision(queue.WriteRestriction) || afterRules.WriteRestrictionStatus().State != "violated" {
		t.Fatal("generic emitter changed authority source")
	}
	pending, err := s.settle(t.Context(), w)
	must(t, err)
	state, _, err = db.Load(t.Context(), q)
	must(t, err)
	if pending || state.Active != nil || state.BaseSHA != tested || state.WriteRestrictionStatus().State != "violated" {
		t.Fatalf("started landing did not settle: pending=%v base=%s state=%+v", pending, state.BaseSHA, state.WriteRestrictionStatus())
	}
	if got := git("", "--git-dir="+remote, "rev-parse", "refs/heads/main"); got != tested {
		t.Fatal("real remote differs", got, tested)
	}
	t.Logf("governing_commit=%s effective_digest=%s coverage_digest=%s actor_team_id=%s observer_event=%d source_revision=%d landed=%s remote=%s", pin.GoverningCommit, pin.EffectiveDigest, pin.CoverageDigest, binding.ResolvedActor.Team.TeamID, id, state.RulesSourceRevision(queue.WriteRestriction), state.BaseSHA, tested)
}
