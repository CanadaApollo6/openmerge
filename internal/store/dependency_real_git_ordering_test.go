package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	fakeforge "github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// A review ref is not a qualifying landing. Only the source's durable outcome
// plus real ancestry unlocks B; an old explicit pin never follows the new head.
func TestDependencyRealGitReviewPushOrderedLanding(t *testing.T) {
	repo := func(name string) (string, string, string, string) {
		dir := filepath.Join(t.TempDir(), name+".git")
		writerGit(t, dir, "", "init", "--bare", "--template=", dir)
		tree := writerGit(t, dir, "", "mktree")
		base := writerGit(t, dir, name+" base\n", "commit-tree", tree)
		first := writerGit(t, dir, name+" first\n", "commit-tree", tree, "-p", base)
		next := writerGit(t, dir, name+" next\n", "commit-tree", tree, "-p", base)
		writerGit(t, dir, "", "update-ref", "refs/heads/main", base)
		writerGit(t, dir, "", "update-ref", "refs/heads/review", first)
		return dir, base, first, next
	}
	sourceDir, base, first, next := repo("source")
	consumerDir, cbase, chead, _ := repo("consumer")
	s := newStore(t)
	binding := depBinding(t)
	f := &depFixture{s: s, b: binding, landedA: next}
	f.c = newDepQueue(t, s, binding, "acme/client", 7, 'e', cbase, chead)
	f.a = newDepQueue(t, s, binding, "acme/api", 42, 'a', base, first)
	for _, q := range []QueueID{f.c.id, f.a.id} {
		depSide(t, s, q, binding, "init", binding)
	}
	f.a.applyPolicy(t, s, binding, true, []string{}, []string{f.c.key.Repository})
	f.c.applyPolicy(t, s, binding, true, []string{f.a.key.Repository}, []string{})
	f.decl, f.declID = f.c.declare(t, s, binding, dependency.Basis{}, 1, false, edge(f.a, false))
	pinned := addDepPR(t, s, f.c, 8, 'd')
	pd, pid := pinned.declare(t, s, binding, dependency.Basis{}, 1, false, edge(f.a, true))
	fake := fakeforge.New()
	fake.SetDependencySource(forge.RepoRef{Path: "acme/api"}, forge.DependencySource{RepositoryID: f.a.alias.RepositoryID, Number: 42, HeadSHA: first, Base: "main", BaseSHA: base})
	git, err := gitops.New(t.Context(), gitops.Config{WorkDir: t.TempDir(), Remote: func(_ context.Context, r forge.RepoRef) (string, gitops.Auth, error) {
		if r.Path == "acme/client" {
			return consumerDir, gitops.Auth{}, nil
		}
		return sourceDir, gitops.Auth{}, nil
	}, HeadRef: func(int) string { return "refs/heads/review" }})
	must(t, err)
	var pinPrior uint64
	observe := func(sequence uint64, head string, revision uint64, landed bool) {
		t.Helper()
		source, err := fake.DependencySource(t.Context(), forge.RepoRef{Path: "acme/api"}, 42)
		must(t, err)
		if source.HeadSHA != head {
			t.Fatal("fake forge drift")
		}
		onBase, err := git.IsAncestor(t.Context(), forge.RepoRef{Path: "acme/api"}, head, source.BaseSHA)
		must(t, err)
		result := "waiting"
		if landed && onBase {
			result = "landed"
		}
		o := dependency.Observed{Namespace: binding.Namespace, Alias: f.c.alias, ChangeID: string(f.c.change), Revision: 1, Declaration: dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, Source: f.a.ref, ExpectedPrior: f.observedID, ReadSequence: sequence, SourceRevision: dependency.Baseline{Revision: revision, Head: head, IntentDigest: edge(f.a, false).Baseline.IntentDigest}, SourceBase: source.BaseSHA, Result: result}
		f.observedID = depSide(t, s, f.c.id, binding, fmt.Sprint("poll-", sequence), o)[0]
		f.observed = o
		pr := "waiting"
		if revision > 1 {
			pr = "stale"
		}
		po := dependency.Observed{Namespace: binding.Namespace, Alias: pinned.alias, ChangeID: string(pinned.change), Revision: 1, Declaration: dependency.Basis{EventID: pid, Digest: pd.Digest}, Source: edge(f.a, true).Reference, ReadSequence: sequence, SourceRevision: dependency.Baseline{Revision: revision, Head: head, IntentDigest: edge(f.a, false).Baseline.IntentDigest}, SourceBase: source.BaseSHA, Result: pr}
		po.ExpectedPrior = pinPrior
		pinPrior = depSide(t, s, f.c.id, binding, fmt.Sprint("pin-poll-", sequence), po)[0]
	}
	observe(1, first, 1, false)
	assertWaiting := func() {
		state, _, err := s.Load(f.ctx(t.Context()), f.c.id)
		must(t, err)
		if len(builds(depCfg, state, time.Now())) != 0 || len(state.Batches()) != 0 || len(state.Entries) != 2 {
			t.Fatal("unlanded source started CI or dequeued consumer")
		}
	}
	assertWaiting()
	writerGit(t, sourceDir, "", "update-ref", "refs/heads/review", next, first)
	now := time.Now()
	appendEvents(t, s, f.a.id, queue.PRObserved{PR: 42, HeadSHA: next, Eligible: true, Trusted: true}, queue.ChangeRevisionObserved{PR: 42, ChangeID: f.a.change, Revision: 2, Predecessor: 1, HeadSHA: next, Basis: queue.RevisionObservationBasis{BaseSHA: base, At: now}})
	fake.SetDependencySource(forge.RepoRef{Path: "acme/api"}, forge.DependencySource{RepositoryID: f.a.alias.RepositoryID, Number: 42, HeadSHA: next, Base: "main", BaseSHA: base})
	observe(2, next, 2, false)
	assertWaiting()
	sourceState, _, err := s.Load(t.Context(), f.a.id)
	must(t, err)
	for _, a := range queue.Plan(depCfg, sourceState, now) {
		if d, ok := a.(queue.Dequeue); ok {
			appendEvents(t, s, f.a.id, queue.Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason, BatchID: d.BatchID})
		}
	}
	assertWaiting()
	appendEvents(t, s, f.a.id, queue.EnqueueRequested{PR: 42, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 42, HeadSHA: next}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: next, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: next, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "source", FenceToken: "fence-acme/api", At: now})
	writerGit(t, sourceDir, "", "update-ref", "refs/heads/main", next, base)
	f.settlement = uint64(appendEvents(t, s, f.a.id, queue.Landed{BatchID: "b1", NewBaseSHA: next, NewFence: "settled"}))
	f.watermark = f.settlement
	fake.SetDependencySource(forge.RepoRef{Path: "acme/api"}, forge.DependencySource{RepositoryID: f.a.alias.RepositoryID, Number: 42, HeadSHA: next, Base: "main", BaseSHA: next})
	sourceState, _, err = s.Load(t.Context(), f.a.id)
	must(t, err)
	if sourceState.BaseSHA != next || sourceState.Active != nil {
		t.Fatal("source did not settle", sourceState.Active, sourceState.BaseSHA)
	}
	observe(3, next, 2, true)
	state, _, err := s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if !state.DependencyGated(queue.BatchPR{PR: 8, HeadSHA: pinned.head}) || state.DependencyGated(queue.BatchPR{PR: 7, HeadSHA: chead}) {
		t.Fatal("push repinned explicit edge or failed to release revision-less edge")
	}
	id := admitDependencyBuild(t, f, depCfg)
	_, err = s.Append(f.ctx(t.Context()), f.c.id, []queue.Event{queue.TestRefBuilt{BatchID: id, TestedSHA: chead, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: chead, Success: true}}, "")
	must(t, err)
	_, err = s.Plan(f.ctx(t.Context()), f.c.id, depCfg, now)
	must(t, err)
	claim := round2ClaimKey(t, s, f.c.id, now, (queue.Land{BatchID: id}).Key())
	ok, err := s.BeginConsequential(f.ctx(t.Context()), claim, depCfg, now)
	must(t, err)
	if !ok {
		t.Fatal("landing declined")
	}
	evidence := f.evidence()
	evidence.Members[0].Observations[0].SourceRevision = f.observed.SourceRevision
	evidence.Members[0].Sources[0].Revision = f.observed.SourceRevision
	must(t, f.start(t.Context(), claim, evidence))
	land := claim.Action.(queue.Land)
	writerGit(t, consumerDir, "", "update-ref", "refs/heads/main", chead, cbase)
	onBase, err := git.IsAncestor(t.Context(), forge.RepoRef{Path: "acme/client"}, land.TestedSHA, chead)
	must(t, err)
	if !onBase {
		t.Fatal("consumer landing is not on its real base")
	}
	_, err = s.Append(f.ctx(t.Context()), f.c.id, []queue.Event{queue.Landed{BatchID: land.BatchID, NewBaseSHA: land.TestedSHA, NewFence: "consumer-settled"}}, "")
	must(t, err)
	state, _, err = s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 1 || state.Entries[0].PR != 8 {
		t.Fatal("ordered consumer did not land, or pin landed", state.Entries)
	}
	assertCompositionReplay(t, f)
}
