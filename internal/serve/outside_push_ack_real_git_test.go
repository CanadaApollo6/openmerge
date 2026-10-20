package serve

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestOutsidePushAcknowledgementRealGit(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--bare", "--template=", remote)
	tree := git("", "--git-dir="+remote, "mktree")
	base := git("base\n", "--git-dir="+remote, "commit-tree", tree)
	landed := git("queue landing\n", "--git-dir="+remote, "commit-tree", tree, "-p", base)
	head := git("break glass\n", "--git-dir="+remote, "commit-tree", tree, "-p", landed)
	git("", "--git-dir="+remote, "update-ref", "refs/heads/main", base)
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(dir, "observer-cache"), HeadRef: func(int) string { return "refs/pull/1/head" }, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }})
	must(t, err)
	s.options.OutsidePushHistory = func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error) { return g, nil }
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	e, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := e.(queue.PolicyAuthorityApplied)
	appendEvents := func(events ...queue.Event) {
		t.Helper()
		_, err := s.store.Append(t.Context(), w.id, events, "")
		must(t, err)
	}
	initial, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	now := time.Now().UTC()
	appendEvents(queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin, queue.BaseMoved{SHA: base}, queue.PRObserved{PR: 1, HeadSHA: landed, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: landed}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: landed, Method: queue.Merge, At: now}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: initial.Fence, At: now})
	for _, sha := range []string{landed, base, head, base} {
		git("", "--git-dir="+remote, "update-ref", "refs/heads/main", sha)
		appendEvents(queue.BaseMoved{SHA: sha})
	}
	must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.RecentOutsidePushes()) != 0 {
		t.Fatal("audit ran before settlement")
	}
	appendEvents(queue.Landed{BatchID: "b1", NewBaseSHA: landed, NewFence: "next"})
	must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
	var warnings []store.OutsidePushWarning
	must(t, s.store.VisitOutsidePushWarnings(t.Context(), w.id, func(w store.OutsidePushWarning) error { warnings = append(warnings, w); return nil }))
	if len(warnings) != 3 || !warnings[0].PossibleStaleObservation || !warnings[0].Rewrite || len(warnings[0].Commits) != 0 || warnings[1].Rewrite || len(warnings[1].Commits) != 2 || !warnings[2].Rewrite {
		t.Fatalf("incorrect real Git suppression/history: %+v", warnings)
	}
	refs := git("", "--git-dir="+remote, "for-each-ref", "--format=%(refname) %(objectname)")
	before, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	snap, err := queue.EncodeSnapshot(before)
	must(t, err)
	for _, warning := range warnings {
		if !warning.AncestryKnown || !warning.HistoryKnown || warning.VerificationBeforeLanding != "none" {
			t.Fatal("lost caveats/evidence")
		}
		_, err = s.store.AcknowledgeOutsidePush(t.Context(), w.id, warning.EventID, warning.NewSHA, "on-call")
		must(t, err)
	}
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	next, err := queue.EncodeSnapshot(after)
	must(t, err)
	if string(snap) != string(next) || refs != git("", "--git-dir="+remote, "for-each-ref", "--format=%(refname) %(objectname)") {
		t.Fatal("acknowledgement changed queue state or real refs")
	}
	if after.RecentOutsidePushes()[0].PossibleStaleObservation != true {
		t.Fatal("acknowledgement erased uncertainty")
	}
}
