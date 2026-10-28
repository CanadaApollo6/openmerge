package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// admitDependencyBuild exercises the actual activated planner/claim/start path.
// No forge calls or refs are involved: the admission precedes all build I/O.
func admitDependencyBuild(t *testing.T, f *depFixture, cfg queue.Config) string {
	t.Helper()
	ctx := f.ctx(t.Context())
	now := time.Now()
	_, err := f.s.Plan(ctx, f.c.id, cfg, now)
	must(t, err)
	claims, err := f.s.ClaimQueue(ctx, f.c.id, 20, time.Minute, now)
	must(t, err)
	for _, claim := range claims {
		var start queue.Event
		var id string
		switch a := claim.Action.(type) {
		case queue.BuildTestRef:
			id = a.BatchID
			start = queue.BatchStarted{BatchID: id, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now}
		case queue.BuildChainedTestRef:
			id = a.BatchID
			start = queue.ChainedBatchStarted{BatchID: id, BaseSHA: a.BaseSHA, PRs: a.PRs, ParentBatchID: a.ParentBatchID, Position: a.Position, At: now}
		default:
			continue
		}
		ok, err := f.s.BeginConsequential(ctx, claim, cfg, now)
		must(t, err)
		if !ok {
			t.Fatal("start declined")
		}
		must(t, f.s.Emit(ctx, claim, cfg)(start))
		must(t, f.s.Complete(ctx, claim, Done))
		return id
	}
	t.Fatal("no build claim", claims)
	return ""
}

func assertCompositionReplay(t *testing.T, f *depFixture) {
	t.Helper()
	type record struct {
		id  uint64
		raw []byte
	}
	rows, err := f.s.pool.Query(t.Context(), `SELECT id,payload FROM events WHERE queue_id=$1 ORDER BY id`, f.c.id)
	must(t, err)
	var records []record
	for rows.Next() {
		var r record
		must(t, rows.Scan(&r.id, &r.raw))
		records = append(records, r)
	}
	must(t, rows.Err())
	rows.Close()
	lookup := func(id uint64) ([]byte, error) {
		for _, r := range records {
			if r.id == id {
				return r.raw, nil
			}
		}
		return nil, errors.New("missing target")
	}
	fresh := func() journalreader.Reader {
		r := journalreader.New(queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, f.b, lookup)
		r.WithChangeIdentity(func(pr int) (queue.ChangeID, error) { return queue.PRChangeID("7", "acme/client", "main", "", pr) })
		return r
	}
	full := fresh()
	for _, r := range records {
		_, err := full.Apply(r.id, r.raw)
		must(t, err)
	}
	want, err := full.CursorSnapshot()
	must(t, err)
	prefix := fresh()
	for cut, r := range records {
		_, err := prefix.Apply(r.id, r.raw)
		must(t, err)
		raw, err := prefix.CursorSnapshot()
		must(t, err)
		resumed := fresh()
		must(t, resumed.LoadSnapshot(raw, r.id))
		for _, tail := range records[cut+1:] {
			_, err := resumed.Apply(tail.id, tail.raw)
			must(t, err)
		}
		got, err := resumed.CursorSnapshot()
		must(t, err)
		if !bytes.Equal(got, want) || !reflect.DeepEqual(queue.Plan(depCfg, resumed.Queue, time.Now()), queue.Plan(depCfg, full.Queue, time.Now())) {
			t.Fatalf("snapshot/tail diverged at cut %d", cut)
		}
	}
}

func TestDependencyCompositionInvalidationThroughFoldAndReplay(t *testing.T) {
	for _, cause := range []string{"rewind", "declaration", "consumer-policy", "source-policy", "refresh", "advance", "rewind-and-recover"} {
		t.Run(cause, func(t *testing.T) {
			f := newDepFixture(t)
			f.observe(t, "landed", f.landedA)
			id := admitDependencyBuild(t, f, depCfg)
			before, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
			must(t, err)
			reader, _, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
			must(t, err)
			deps, err := reader.BoundDependencies(f.b)
			must(t, err)
			c, compID, ok := deps.Composition(id)
			if !ok || c.BatchID != id || len(c.Members) != 1 || compID <= c.StartEventID {
				t.Fatal("missing exact build companion", c, compID)
			}
			switch cause {
			case "rewind", "rewind-and-recover":
				appendEvents(t, f.s, f.a.id, queue.BaseMoved{SHA: f.a.base})
				f.observe(t, "waiting", f.a.base)
				if cause == "rewind-and-recover" {
					f.observe(t, "landed", f.landedA)
				}
			case "declaration":
				f.c.declare(t, f.s, f.b, dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, 2, true)
			case "consumer-policy":
				f.c.applyPolicy(t, f.s, f.b, true, []string{}, []string{})
			case "source-policy":
				f.a.applyPolicy(t, f.s, f.b, true, []string{}, []string{})
				// The fresh direct read, scheduled by the reverse wake, records lost consent.
				f.observe(t, "unavailable", f.landedA)
			case "advance":
				f.observe(t, "landed", sha('9'))
			case "refresh":
				f.observe(t, "landed", f.landedA)
				f.c.applyPolicy(t, f.s, f.b, true, []string{f.a.key.Repository}, []string{})
			}
			ctx := f.ctx(t.Context())
			state, _, err := f.s.Load(ctx, f.c.id)
			must(t, err)
			if cause == "refresh" || cause == "advance" {
				if state.Active == nil || state.Active.InvalidationReason != "" {
					t.Fatal("unchanged evidence invalidated", state.Active)
				}
				assertCompositionReplay(t, f)
				return
			}
			if state.Active == nil || state.Active.InvalidationReason != "dependency-changed" {
				t.Fatal("no latched invalidation", state.Active)
			}
			actions, err := f.s.Plan(ctx, f.c.id, depCfg, time.Now())
			must(t, err)
			var invalid queue.InvalidateBatches
			for _, a := range actions {
				if v, ok := a.Action.(queue.InvalidateBatches); ok {
					invalid = v
				}
			}
			if !slices.Equal(invalid.BatchIDs, []string{id}) {
				t.Fatal("wrong invalidation", actions)
			}
			_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated(invalid)}, "")
			must(t, err)
			after, _, err := f.s.Load(ctx, f.c.id)
			must(t, err)
			if after.Active != nil || !reflect.DeepEqual(before.Entries, after.Entries) {
				t.Fatal("discard dequeued or reordered entries", after.Active, after.Entries)
			}
			var raw []byte
			must(t, f.s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 ORDER BY id DESC LIMIT 1`, f.c.id).Scan(&raw))
			decoded, err := queue.DecodeEvent(raw)
			must(t, err)
			event, ok := decoded.(queue.DependencyCompositionsInvalidated)
			if !ok || !slices.Equal(event.CompositionIDs, []uint64{compID}) || len(event.CauseEventIDs) != 1 || event.CauseEventIDs[0] <= compID {
				t.Fatal("missing exact retirement companion", decoded)
			}
			// The emitted event must also apply through the pure queue fold.
			if got := queue.Apply(before, event); got.Active != nil || len(got.Entries) != 1 {
				t.Fatal("raw fold refused planner discard")
			}
			assertCompositionReplay(t, f)
		})
	}
}

func TestDependencyCompositionAdmissionIsAtomic(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	// A queue start cannot be committed when the companion is refused.
	f.observe(t, "waiting", f.a.base)
	var before int
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1`, f.c.id).Scan(&before))
	_, err := f.s.Append(f.ctx(t.Context()), f.c.id, []queue.Event{queue.BatchStarted{BatchID: "b1", BaseSHA: f.c.base, PRs: []queue.BatchPR{{PR: f.c.pr, HeadSHA: f.c.head}}, At: time.Now()}}, "")
	if !errors.Is(err, dependency.ErrNotAdmitted) {
		t.Fatal("waiting composition admitted", err)
	}
	var after int
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1`, f.c.id).Scan(&after))
	if before != after {
		t.Fatal("partial admission committed", before, after)
	}
	state, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	if state.Active != nil {
		t.Fatal("refused admission survived replay")
	}
}

func TestDependencyCascadeRebuildAndPollingRepair(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	ref, err := dependency.ParseReference(f.a.ref, dependency.Scope{ForgeInstance: f.b.ForgeInstance, Installation: f.b.Installation, JournalNamespace: f.b.Namespace})
	must(t, err)
	for range 2 {
		got, err := f.s.DependencyCascade(t.Context(), []QueueID{f.c.id, f.a.id}, f.b, ref)
		must(t, err)
		if !slices.Equal(got, []QueueID{f.c.id}) {
			t.Fatal("restart lost reverse wake", got)
		}
	}
	id := admitDependencyBuild(t, f, depCfg)
	// Deliberately miss the wake. Ordinary polling of recorded direct evidence
	// still retires the same composition; cache absence never authorizes it.
	f.observe(t, "waiting", f.a.base)
	state, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if state.Batch(id) == nil || state.Batch(id).InvalidationReason != "dependency-changed" {
		t.Fatal("missed wake bypassed polling repair")
	}
	assertCompositionReplay(t, f)
}

func TestDependencyCompositionDowngradeRefusal(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(fmt.Sprint(retired), func(t *testing.T) {
			f := newDepFixture(t)
			f.observe(t, "landed", f.landedA)
			id := admitDependencyBuild(t, f, depCfg)
			if retired {
				f.observe(t, "waiting", f.a.base)
				_, err := f.s.Append(f.ctx(t.Context()), f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{id}, Reason: "dependency-changed"}}, "")
				must(t, err)
			}
			// Isolate the real new row so another side kind cannot mask a missing gate.
			kind := "dependency_composition_recorded"
			if retired {
				kind = "dependency_compositions_invalidated"
			}
			if !slices.Contains(authorityDowngradeEventTypes, kind) {
				t.Fatal("new kind absent from downgrade inventory", kind)
			}
			_, err := f.s.pool.Exec(t.Context(), `DELETE FROM observations`)
			must(t, err)
			_, err = f.s.pool.Exec(t.Context(), `DELETE FROM events WHERE type<>$1`, kind)
			must(t, err)
			reader := &JournalReader{store: f.s, timeout: time.Minute}
			if err := reader.CheckAuthorityDowngrade(t.Context()); !errors.Is(err, ErrAuthorityDowngradeRefused) {
				t.Fatal("new history permitted downgrade", kind, err)
			}
		})
	}
}

func TestDependencyEligibleClockAccumulatesRewindIntervals(t *testing.T) {
	f := newDepFixture(t)
	state, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	origin := state.Entries[0].EnqueuedAt.Truncate(time.Microsecond)
	// Control recorded observation boundaries without sleeps. The queue admission
	// time is unchanged, and unrelated refreshes consume no additional interval.
	observe := func(result string, minutes int) {
		f.observe(t, result, f.landedA)
		_, err := f.s.pool.Exec(t.Context(), `UPDATE events SET recorded_at=$2 WHERE id=$1`, f.observedID, origin.Add(time.Duration(minutes)*time.Minute))
		must(t, err)
	}
	observe("landed", 1)
	observe("waiting", 4)
	observe("landed", 20)
	observe("waiting", 22)
	observe("landed", 40)
	cfg := depCfg
	cfg.BatchMaxSize = 2
	cfg.BatchMaxWait = 10 * time.Minute
	for _, restart := range []bool{false, true} {
		if restart {
			_, err := f.s.pool.Exec(t.Context(), `DELETE FROM observations WHERE queue_id=$1`, f.c.id)
			must(t, err)
		}
		state, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
		must(t, err)
		got, ok := queue.NextWake(cfg, state, origin.Add(41*time.Minute))
		if !ok || !got.Equal(origin.Add(45*time.Minute)) {
			t.Fatal("lost 5 minutes of prior eligible time", restart, got, ok, "origin", origin, "want", origin.Add(45*time.Minute))
		}
	}
}

// The 4a acknowledgement fixtures used to reuse pre-edit CI. Composition
// invalidation now requires that they explicitly retire and rebuild first.
func (f *depFixture) rebuildAfterDependencyEdit(t *testing.T, claim ClaimedAction) ClaimedAction {
	t.Helper()
	state, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if state.Active == nil || state.Active.InvalidationReason == "" {
		return claim
	}
	// The original claim must stay refused even after the hold is acknowledged.
	if err := f.start(t.Context(), claim, f.holdEvidence(t)); !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatal("acknowledgement revived pre-edit composition", err)
	}
	must(t, f.s.Complete(t.Context(), claim, Cancelled))
	_, err = f.s.Append(f.ctx(t.Context()), f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{state.Active.ID}, Reason: "dependency-changed"}}, "")
	must(t, err)
	state, _, err = f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	for _, action := range queue.Plan(depCfg, state, time.Now()) {
		if build, ok := action.(queue.BuildTestRef); ok {
			return f.claimLand(t, build.BatchID)
		}
	}
	t.Fatal("acknowledged edit did not become rebuildable")
	return claim
}

func dependencyDecisionLand(t *testing.T, s *Store, setup func(*depFixture)) (*depFixture, ClaimedAction, landingCase, queue.LandingDecisionRecorded) {
	t.Helper()
	inputRaw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	var input landingCase
	must(t, json.Unmarshal(inputRaw, &input))
	f := &depFixture{s: s, b: depBinding(t)}
	_, claim, c, d := decisionLandInput(t, s, inputRaw, func(q QueueID, event queue.Event) context.Context {
		if start, ok := event.(queue.BatchStarted); ok {
			f.c = depQueue{id: q, key: dependency.QueueKey{Repository: "https://github.com/Owner/Repo", Base: "release"}, alias: dependency.SourceAlias{ForgeInstance: f.b.ForgeInstance, Installation: "7", RepositoryID: "id-owner-repo", Number: uint64(input.Land.PRs[0].PR)}, change: input.Fresh.Basis.ChangeID, revision: uint64(input.Fresh.Basis.Revision), base: start.BaseSHA, head: input.Fresh.Basis.Head}
			f.landSource(t)
			f.c.applyPolicy(t, s, f.b, true, []string{f.a.key.Repository}, []string{})
			f.decl, f.declID = f.c.declare(t, s, f.b, dependency.Basis{}, 1, false, edge(f.a, false))
			f.observe(t, "landed", f.landedA)
			if setup != nil {
				setup(f)
			}
			return WithDependencyActivation(t.Context(), q, f.b)
		}
		return t.Context()
	})
	return f, claim, c, d
}

func TestDependencyRealGitSourceRewindRetiresComposition(t *testing.T) {
	// Two independent disposable bare repositories, with no OpenMerge refs or
	// external service. Object construction uses Git plumbing; ancestry uses the
	// production gitops reader, never parsed log/rev-list output.
	objectRepo := func(name string) (string, string, string) {
		dir := filepath.Join(t.TempDir(), name+".git")
		writerGit(t, dir, "", "init", "--bare", "--template=", dir)
		tree := writerGit(t, dir, "", "mktree")
		base := writerGit(t, dir, name+" base\n", "commit-tree", tree)
		head := writerGit(t, dir, name+" head\n", "commit-tree", tree, "-p", base)
		writerGit(t, dir, "", "update-ref", "refs/heads/main", head)
		return dir, base, head
	}
	sourceDir, sourceBase, sourceHead := objectRepo("source")
	_, consumerBase, consumerHead := objectRepo("consumer")
	s := newStore(t)
	binding := depBinding(t)
	f := &depFixture{s: s, b: binding, landedA: sourceHead}
	f.c = newDepQueue(t, s, binding, "acme/client", 7, 'e', consumerBase, consumerHead)
	f.a = newDepQueue(t, s, binding, "acme/api", 42, 'a', sourceBase, sourceHead)
	for _, q := range []QueueID{f.c.id, f.a.id} {
		depSide(t, s, q, binding, "init", binding)
	}
	f.a.applyPolicy(t, s, binding, true, []string{}, []string{f.c.key.Repository})
	f.c.applyPolicy(t, s, binding, true, []string{f.a.key.Repository}, []string{})
	f.decl, f.declID = f.c.declare(t, s, binding, dependency.Basis{}, 1, false, edge(f.a, false))
	now := time.Now()
	appendEvents(t, s, f.a.id, queue.BatchStarted{BatchID: "b1", BaseSHA: sourceBase, PRs: []queue.BatchPR{{PR: 42, HeadSHA: sourceHead}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: sourceHead, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: sourceHead, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "source", FenceToken: "fence-acme/api", At: now})
	f.settlement = uint64(appendEvents(t, s, f.a.id, queue.Landed{BatchID: "b1", NewBaseSHA: sourceHead, NewFence: "settled"}))
	f.watermark = f.settlement
	git, err := gitops.New(t.Context(), gitops.Config{WorkDir: t.TempDir(), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) {
		return sourceDir, gitops.Auth{}, nil
	}, HeadRef: func(int) string { return "refs/heads/main" }})
	must(t, err)
	record := func(base string) {
		onBase, err := git.IsAncestor(t.Context(), forge.RepoRef{Path: "acme/api"}, sourceHead, base)
		must(t, err)
		result := "waiting"
		if onBase {
			result = "landed"
		}
		f.observe(t, result, base)
	}
	record(sourceHead)
	id := admitDependencyBuild(t, f, depCfg)
	writerGit(t, sourceDir, "", "update-ref", "refs/heads/main", sourceBase, sourceHead)
	appendEvents(t, s, f.a.id, queue.BaseMoved{SHA: sourceBase})
	record(sourceBase)
	state, _, err := s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if state.Batch(id) == nil || state.Batch(id).InvalidationReason != "dependency-changed" {
		t.Fatal("real rewind retained composition")
	}
	if !state.DependencyGated(queue.BatchPR{PR: 7, HeadSHA: consumerHead}) {
		t.Fatal("real rewind did not wait")
	}
	assertCompositionReplay(t, f)
}

func TestDependencyCompositionOutsideAdmissionAndWrongReferencesRefused(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	id := admitDependencyBuild(t, f, depCfg)
	read := func() sourceFold {
		r, w, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
		must(t, err)
		return sourceFold{r, uint64(w)}
	}
	current := read()
	deps, err := current.reader.BoundDependencies(f.b)
	must(t, err)
	c, compID, _ := deps.Composition(id)
	_, err = f.s.AppendSide(t.Context(), f.c.id, SideAppend{Count: 1, DedupeKey: "outside", Binding: f.b, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Dependency: c}}, nil }})
	if err == nil || !strings.Contains(err.Error(), "build-start transaction") {
		t.Fatal("composition escaped admission", err)
	}
	f.observe(t, "waiting", f.a.base)
	current = read()
	for _, bad := range []queue.DependencyCompositionsInvalidated{
		{BatchIDs: []string{id}, CompositionIDs: []uint64{compID - 1}, Reason: "dependency-changed", CauseEventIDs: []uint64{f.observedID}},
		{BatchIDs: []string{"other"}, CompositionIDs: []uint64{compID}, Reason: "dependency-changed", CauseEventIDs: []uint64{f.observedID}},
		{BatchIDs: []string{id}, CompositionIDs: []uint64{compID}, Reason: "dependency-changed", CauseEventIDs: []uint64{f.observedID + 1}},
	} {
		_, raw, err := queue.EncodeEvent(bad)
		must(t, err)
		reader := current.reader
		if _, err := reader.Apply(current.watermark+100, raw); err == nil {
			t.Fatal("wrong retirement reference accepted", bad)
		}
	}
}

func TestDependencyEligibleClockHistoryGrowth(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	state, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	key, err := storedKey(t.Context(), f.s.pool, f.c.id)
	must(t, err)
	measure := func(label string) {
		started := time.Now()
		clocks, err := dependencyEligibleClocks(t.Context(), f.s.pool, f.c.id, state, key)
		must(t, err)
		if clocks[f.c.pr].IsZero() {
			t.Fatal("lost eligible clock")
		}
		t.Logf("%s history replay: %s", label, time.Since(started))
	}
	measure("initial")
	const refreshes = 256
	_, err = f.s.AppendSide(t.Context(), f.c.id, SideAppend{Count: refreshes, DedupeKey: "clock-growth", Binding: f.b, Build: func(ids []uint64) ([]journalreader.Event, error) {
		out := make([]journalreader.Event, refreshes)
		o := f.observed
		prior := f.observedID
		for i, id := range ids {
			o.ExpectedPrior = prior
			o.ReadSequence++
			out[i] = journalreader.Event{Dependency: o}
			prior = id
		}
		return out, nil
	}})
	must(t, err)
	measure("256 unchanged refreshes")
}
