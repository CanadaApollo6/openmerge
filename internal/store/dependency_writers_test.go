package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ADR 0011 slice 4a-1 store proofs. All queues, events and SHAs are local
// fixtures; there is no forge or git I/O. Ancestry is the seam's trusted read.

var depCfg = queue.Config{MergeMethod: queue.Merge}

func depBinding(t *testing.T) dependency.JournalInitialized {
	t.Helper()
	var token [32]byte
	token[0] = 4
	b, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	return b
}

func depSide(t *testing.T, s *Store, q QueueID, b dependency.JournalInitialized, key string, events ...dependency.Event) []uint64 {
	t.Helper()
	var reserved []uint64
	_, err := s.AppendSide(t.Context(), q, SideAppend{Count: len(events), DedupeKey: key, Binding: b, Build: func(ids []uint64) ([]journalreader.Event, error) {
		reserved = ids
		out := make([]journalreader.Event, len(events))
		for i, e := range events {
			out[i] = journalreader.Event{Dependency: e}
		}
		return out, nil
	}})
	must(t, err)
	return reserved
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

// depQueue is one served PR-backed queue with its stable alias and change.
type depQueue struct {
	id       QueueID
	key      dependency.QueueKey
	alias    dependency.SourceAlias
	change   queue.ChangeID
	pr       int
	head     string
	base     string
	ref      string
	revision uint64
	policy   dependency.PolicyApplied
	policyID uint64
}

func newDepQueue(t *testing.T, s *Store, b dependency.JournalInitialized, repo string, pr int, head byte, objects ...string) depQueue {
	t.Helper()
	id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: repo, BaseBranch: "main"})
	must(t, err)
	must(t, s.InitializeFence(t.Context(), id, "fence-"+repo))
	change, err := queue.PRChangeID("7", repo, "main", "", pr)
	must(t, err)
	ref, err := dependency.Reference{Scope: dependency.Scope{ForgeInstance: b.ForgeInstance, Installation: b.Installation, JournalNamespace: b.Namespace}, ChangeID: string(change)}.Canonical()
	must(t, err)
	d := depQueue{id: id, key: dependency.QueueKey{Repository: "https://github.com/" + repo, Base: "main"}, alias: dependency.SourceAlias{ForgeInstance: b.ForgeInstance, Installation: "7", RepositoryID: "id-" + strings.ReplaceAll(repo, "/", "-"), Number: uint64(pr)}, change: change, pr: pr, head: sha(head), base: sha('0'), ref: ref, revision: 1}
	if len(objects)==2 { d.base,d.head=objects[0],objects[1] }
 now := time.Now()
	appendEvents(t, s, id, queue.BaseMoved{SHA: d.base}, queue.PRObserved{PR: pr, HeadSHA: d.head, Eligible: true, Trusted: true}, queue.ChangeRevisionObserved{PR: pr, ChangeID: change, Revision: 1, HeadSHA: d.head, Basis: queue.RevisionObservationBasis{BaseSHA: d.base, At: now}}, queue.EnqueueRequested{PR: pr, At: now})
	return d
}

func (d *depQueue) applyPolicy(t *testing.T, s *Store, b dependency.JournalInitialized, enabled bool, from, to []string) uint64 {
	t.Helper()
	p := dependency.Policy{Mode: "ordered", AllowFrom: from, AllowTo: to}
	next := dependency.PolicyApplied{Enabled: enabled, Namespace: b.Namespace, Alias: d.alias, Queue: d.key, PriorEventID: d.policyID, DefaultBranch: "main", ReadRef: "refs/heads/main", ReadID: fmt.Sprint("policy-read-", d.policyID), Commit: sha('c'), Digest: dependency.PolicyDigest(p, enabled), Policy: p, Writers: []dependency.Grant{}}
	d.policy, d.policyID = next, depSide(t, s, d.id, b, next.ReadID, next)[0]
	return d.policyID
}

// edge declares source revision-less, or pinned to its revision 1 head.
func edge(source depQueue, pinned bool) dependency.DeclaredEdge {
	e := dependency.DeclaredEdge{Reference: source.ref, Queue: source.key, Baseline: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: source.head}}
	if pinned {
		e.Reference, e.PinnedHead = source.ref+"@1", source.head
	}
	return e
}

// declare records d's complete declaration; hold is the resulting loosening hold.
func (d depQueue) declare(t *testing.T, s *Store, b dependency.JournalInitialized, prior dependency.Basis, poll uint64, held bool, edges ...dependency.DeclaredEdge) (dependency.DependenciesDeclared, uint64) {
	t.Helper()
	if edges == nil {
		edges = []dependency.DeclaredEdge{}
	}
	hold := dependency.DeclarationHold{Editors: []string{}}
	if held {
		hold = dependency.DeclarationHold{Held: true, Editors: []string{"author"}}
	}
	e := dependency.DependenciesDeclared{Hold: hold, Namespace: b.Namespace, Alias: d.alias, ChangeID: string(d.change), Revision: d.revision, Queue: d.key, Prior: prior, Poll: poll, Digest: dependency.DeclarationDigest(edges), Edges: edges, Bindings: []dependency.AliasBinding{}, Attribution: dependency.Attribution{Editors: []string{"author"}}, ReadGrant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: d.alias.RepositoryID, ReadID: "bind-read", Fresh: true, Allowed: true}, Accountability: dependency.Accountability{Author: "author", Humans: []string{}}}
	ids := depSide(t, s, d.id, b, fmt.Sprint("declare-", d.pr, "-", poll), e)
	return e, ids[0]
}

// depFixture: B (acme/client#7) depends revision-less on A (acme/api#42).
type depFixture struct {
	s          *Store
	b          dependency.JournalInitialized
	a, c       depQueue
	settlement uint64
	watermark  uint64
	landedA    string
	decl       dependency.DependenciesDeclared
	declID     uint64
	observed   dependency.Observed
	observedID uint64
}

func newDepFixture(t *testing.T) *depFixture {
	t.Helper()
	s := newStore(t)
	b := depBinding(t)
	f := &depFixture{s: s, b: b}
	f.c = newDepQueue(t, s, b, "acme/client", 7, 'e')
	f.landSource(t)
	f.c.applyPolicy(t, s, b, true, []string{f.a.key.Repository}, []string{})
	f.decl, f.declID = f.c.declare(t, s, b, dependency.Basis{}, 1, false, edge(f.a, false))
	return f
}

// landSource creates A (acme/api#42), consenting to f.c, and lands it through
// its own queue: that Landed row is the settlement evidence. It also records
// f.c's journal initialization.
func (f *depFixture) landSource(t *testing.T) {
	t.Helper()
	s, b := f.s, f.b
	f.landedA = sha('b')
	f.a = newDepQueue(t, s, b, "acme/api", 42, 'a')
	depSide(t, s, f.a.id, b, "init", b)
	depSide(t, s, f.c.id, b, "init", b)
	f.a.applyPolicy(t, s, b, true, []string{}, []string{f.c.key.Repository})
	now := time.Now()
	appendEvents(t, s, f.a.id, queue.BatchStarted{BatchID: "b1", BaseSHA: f.a.base, PRs: []queue.BatchPR{{PR: 42, HeadSHA: f.a.head}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: f.landedA, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: f.landedA, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "a-attempt", FenceToken: "fence-acme/api", At: now})
	f.settlement = uint64(appendEvents(t, s, f.a.id, queue.Landed{BatchID: "b1", NewBaseSHA: f.landedA, NewFence: "next"}))
	f.watermark = f.settlement
	state, _, err := s.Load(t.Context(), f.a.id)
	must(t, err)
	if state.BaseSHA != f.landedA || state.Active != nil {
		t.Fatal("source fixture did not land")
	}
}

func (f *depFixture) observe(t *testing.T, result string, base string) {
	t.Helper()
	o := dependency.Observed{Namespace: f.b.Namespace, Alias: f.c.alias, ChangeID: string(f.c.change), Revision: f.c.revision, Declaration: dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, Source: f.a.ref, ExpectedPrior: f.observedID, ReadSequence: f.observed.ReadSequence + 1, SourceRevision: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: f.a.head}, SourceBase: base, Result: result}
	f.observedID = depSide(t, f.s, f.c.id, f.b, fmt.Sprint("observe-", f.declID, "-", o.ReadSequence), o)[0]
	f.observed = o
}

func (f *depFixture) ctx(ctx context.Context) context.Context {
	return WithDependencyActivation(ctx, f.c.id, f.b)
}

// claimLand drives B's own batch to an authorized Land claim.
func (f *depFixture) claimLand(t *testing.T, batch ...string) ClaimedAction {
	t.Helper()
	ctx := f.ctx(t.Context())
	now := time.Now()
	id := "b1"
	if len(batch) > 0 {
		id = batch[0]
	}
 tested:=sha('f')
 if id!="b1" { tested=fmt.Sprintf("%040x",1000+len(id)+int(id[len(id)-1])) }
	_, startErr := f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchStarted{BatchID: id, BaseSHA: f.c.base, PRs: []queue.BatchPR{{PR: f.c.pr, HeadSHA: f.c.head}}, At: now}, queue.TestRefBuilt{BatchID: id, TestedSHA: tested, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: tested, Success: true}}, "")
 must(t,startErr)
	_, err := f.s.Plan(ctx, f.c.id, depCfg, now)
	must(t, err)
	// Only Land is claimed: a prior landing's effects stay unclaimed.
	claims, err := f.s.ClaimQueue(ctx, f.c.id, 10, time.Minute, now)
	must(t, err)
	i := slices.IndexFunc(claims, func(c ClaimedAction) bool { _, ok := c.Action.(queue.Land); return ok })
	if i < 0 {
		t.Fatal("missing Land claim", claims)
	}
	ok, err := f.s.BeginConsequential(ctx, claims[i], depCfg, now)
	must(t, err)
	if !ok {
		t.Fatal("Land declined")
	}
	return claims[i]
}

// evidence is the trusted seam's fresh landing-start read for B's one edge.
func (f *depFixture) evidence() DependencyStart {
	o := f.observed
	o.ExpectedPrior, o.ReadSequence, o.Result, o.SourceBase = f.observedID, o.ReadSequence+1, "landed", f.landedA
	source := dependency.SourceSettlement{Reference: f.a.ref, Queue: f.a.key, Source: f.a.alias, Grant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: f.a.alias.RepositoryID, ReadID: "landing-start-read", Fresh: true, Allowed: true}, Revision: o.SourceRevision, Commit: f.landedA, SettlementEventID: f.settlement, Watermark: f.watermark, BaseSHA: f.landedA}
	return DependencyStart{Binding: f.b, Members: []DependencyMemberStart{{Observations: []dependency.Observed{o}, Sources: []dependency.SourceSettlement{source}, PolicyCommit: f.c.policy.Commit, PolicyDigest: f.c.policy.Digest}}}
}

func (f *depFixture) start(ctx context.Context, claim ClaimedAction, evidence DependencyStart) error {
	land := claim.Action.(queue.Land)
	return f.s.StartDependencyLanding(ctx, claim, depCfg, cp.Policy{}, nil, nil, queue.LandingStarted{BatchID: land.BatchID, AttemptID: "b-attempt", FenceToken: land.FenceToken, At: time.Now()}, evidence)
}

func builds(cfg queue.Config, s queue.State, now time.Time) [][]int {
	var out [][]int
	for _, a := range queue.Plan(cfg, s, now) {
		if build, ok := a.(queue.BuildTestRef); ok {
			var prs []int
			for _, pr := range build.PRs {
				prs = append(prs, pr.PR)
			}
			out = append(out, prs)
		}
	}
	return out
}

func TestDependencyPlannerGateFromRecordedFacts(t *testing.T) {
	f := newDepFixture(t)
	legacy, _, err := f.s.Load(t.Context(), f.c.id)
	must(t, err)
	if got := builds(depCfg, legacy, time.Now()); len(got) != 1 {
		t.Fatal("legacy load gated without activation", got)
	}
	f.observe(t, "waiting", f.a.base)
	gated, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if got := builds(depCfg, gated, time.Now()); got != nil || !gated.DependencyGated(queue.BatchPR{PR: 7, HeadSHA: f.c.head}) {
		t.Fatal("waiting dependency admitted", got)
	}
	// Persisted planning uses the same gate and plans nothing for B.
	planned, err := f.s.Plan(f.ctx(t.Context()), f.c.id, depCfg, time.Now())
	must(t, err)
	if len(planned) != 0 {
		t.Fatal("persisted gated plan", planned)
	}
	f.observe(t, "landed", f.landedA)
	gated, _, err = f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if got := builds(depCfg, gated, time.Now()); len(got) != 1 || got[0][0] != 7 {
		t.Fatal("landed dependency not admitted", got)
	}
	// Activation for another queue, or a rotated incarnation, never applies here.
	if _, _, err := f.s.Load(WithDependencyActivation(t.Context(), f.a.id+1000, f.b), f.c.id); err != nil {
		t.Fatal(err)
	}
	var token [32]byte
	token[0] = 9
	rotated, err := dependency.NewJournalIdentity(f.b.DatabaseIdentity, token, f.b.ForgeInstance, f.b.Installation)
	must(t, err)
	if _, _, err := f.s.Load(WithDependencyActivation(t.Context(), f.c.id, rotated), f.c.id); err == nil {
		t.Fatal("rotated incarnation admitted work")
	}
}

func TestDependencyStartCommitsCompanionAtomically(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	claim := f.claimLand(t)
	beforeCursor, version := cursorVersion(t, f.s, f.c.id)
	if version != 9 {
		t.Fatal("first side append did not upgrade the outside-push cursor", version)
	}
	must(t, f.start(t.Context(), claim, f.evidence()))
	var kinds []string
	var startID uint64
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_started'`, f.c.id).Scan(&startID))
	rows, err := f.s.pool.Query(t.Context(), `SELECT id,type,payload FROM events WHERE queue_id=$1 AND id>=$2 ORDER BY id`, f.c.id, startID)
	must(t, err)
	var companion dependency.DecisionRecorded
	for rows.Next() {
		var id int64
		var kind string
		var raw []byte
		must(t, rows.Scan(&id, &kind, &raw))
		kinds = append(kinds, kind)
		if kind == "dependency_decision_recorded" {
			e, err := dependency.DecodeEvent(raw)
			must(t, err)
			companion = e.(dependency.DecisionRecorded)
		}
	}
	rows.Close()
	if strings.Join(kinds, ",") != "landing_started,dependency_observed,dependency_decision_recorded" {
		t.Fatal("start bundle", kinds)
	}
	if companion.StartEventID != startID || companion.DecisionID != 0 || len(companion.Sources) != 1 || companion.Sources[0].SettlementEventID != f.settlement || companion.Admission.ObservationIDs[0] <= startID {
		t.Fatalf("companion binding %+v", companion)
	}
	afterCursor, version := cursorVersion(t, f.s, f.c.id)
	if version != 9 || afterCursor.ScannedThrough != beforeCursor.ScannedThrough {
		t.Fatal("start advanced outside-push audit progress", beforeCursor.ScannedThrough, afterCursor.ScannedThrough)
	}
	// Full offline replay with the independent binding re-validates everything.
	reader, _, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
	must(t, err)
	if _, err := reader.BoundDependencies(f.b); err != nil || reader.Queue.Active == nil || reader.Queue.Active.Phase != queue.Landing {
		t.Fatal("replay", err)
	}
	// The ordinary emitter is still refused a companion: AppendSide lacks a start.
	if _, err := f.s.AppendSide(t.Context(), f.c.id, SideAppend{Count: 1, DedupeKey: "forged", Binding: f.b, Build: func([]uint64) ([]journalreader.Event, error) {
		return []journalreader.Event{{Dependency: companion}}, nil
	}}); err == nil || !strings.Contains(err.Error(), "landing-start transaction") {
		t.Fatal("companion admitted outside its start", err)
	}
}

func TestDependencyStartRefusesEachAdmissionGateAtomically(t *testing.T) {
	for name, tc := range map[string]struct {
		before func(*testing.T, *depFixture)
		mutate func(*depFixture, *DependencyStart)
	}{
		"landing-start read waiting": {mutate: func(_ *depFixture, e *DependencyStart) { e.Members[0].Observations[0].Result = "waiting" }},
		"stale observation CAS":      {mutate: func(_ *depFixture, e *DependencyStart) { e.Members[0].Observations[0].ExpectedPrior-- }},
		// Tighten to a pin, then weaken it again at the same head: the edge set
		// is unchanged but the loosening hold blocks a fresh landed read.
		"declaration loosened": {before: func(t *testing.T, f *depFixture) {
			f.decl, f.declID = f.c.declare(t, f.s, f.b, dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, 2, false, edge(f.a, true))
			f.decl, f.declID = f.c.declare(t, f.s, f.b, dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, 3, true, edge(f.a, false))
			f.observed, f.observedID = dependency.Observed{}, 0
			f.observe(t, "landed", f.landedA)
		}},
		"consumer policy disabled": {before: func(t *testing.T, f *depFixture) {
			f.c.applyPolicy(t, f.s, f.b, false, []string{f.a.key.Repository}, []string{})
		}},
		"governing policy reread differs": {mutate: func(_ *depFixture, e *DependencyStart) { e.Members[0].PolicyDigest = strings.Repeat("0", 64) }},
		"source consent revoked": {before: func(t *testing.T, f *depFixture) {
			f.a.applyPolicy(t, f.s, f.b, true, []string{}, []string{})
		}},
		"source base drift": {before: func(t *testing.T, f *depFixture) {
			appendEvents(t, f.s, f.a.id, queue.BaseMoved{SHA: sha('d')})
		}},
		"settlement is not a Landed outcome": {mutate: func(_ *depFixture, e *DependencyStart) { e.Members[0].Sources[0].SettlementEventID-- }},
		"settled commit differs":             {mutate: func(_ *depFixture, e *DependencyStart) { e.Members[0].Sources[0].Commit = sha('d') }},
		"direct-source set changed": {mutate: func(_ *depFixture, e *DependencyStart) {
			e.Members[0].Observations, e.Members[0].Sources = []dependency.Observed{}, []dependency.SourceSettlement{}
		}},
		"unserved source queue": {mutate: func(_ *depFixture, e *DependencyStart) {
			e.Members[0].Sources[0].Queue.Repository = "https://github.com/acme/unserved"
		}},
		"non-declarer source grant": {mutate: func(_ *depFixture, e *DependencyStart) {
			e.Members[0].Sources[0].Grant.SubjectID, e.Members[0].Sources[0].Grant.ReturnedID = "writer", "writer"
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDepFixture(t)
			f.observe(t, "landed", f.landedA)
			claim := f.claimLand(t)
			if tc.before != nil {
				tc.before(t, f)
			}
			evidence := f.evidence()
			if tc.mutate != nil {
				tc.mutate(f, &evidence)
			}
			cursor, err := f.s.Observation(t.Context(), f.c.id, outsidePushCursorKey)
			must(t, err)
			before := eventCount(t, f.s, f.c.id)
			err = f.start(t.Context(), claim, evidence)
			if err == nil || !errors.Is(err, ErrLandingDecisionStale) {
				t.Fatal("gate admitted or failed unretryably:", err)
			}
			assertNoLandingSideEffects(t, f.s, f.c.id, claim, before)
			after, err := f.s.Observation(t.Context(), f.c.id, outsidePushCursorKey)
			must(t, err)
			if !bytes.Equal(cursor, after) {
				t.Fatal("refused start changed cursor bytes")
			}
		})
	}
}

// Two consumers declare each other (a concurrent cycle). Both starts must lock
// {X,Y} in one canonical order and refuse, never deadlock: no member of a cycle
// lands while a direct source is unlanded. Reordering the lock set deadlocks.
// Each queue first lands an unrelated filler change, so the lock-free
// settlement-prefix check passes and the refusal happens under the locks.
func TestDependencyStartLockOrderAndConcurrentCycle(t *testing.T) {
	s := newStore(t)
	b := depBinding(t)
	x := newDepQueue(t, s, b, "acme/x", 1, '1')
	y := newDepQueue(t, s, b, "acme/y", 2, '2')
	settled := map[QueueID]uint64{}
	for _, d := range []*depQueue{&x, &y} {
		now, landed := time.Now(), sha(byte('5'+d.pr))
		appendEvents(t, s, d.id, queue.PRObserved{PR: 100, HeadSHA: sha('7'), Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 100, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: d.base, PRs: []queue.BatchPR{{PR: 100, HeadSHA: sha('7')}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: landed, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: landed, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "filler", FenceToken: "fence-" + strings.TrimPrefix(d.key.Repository, "https://github.com/"), At: now})
		settled[d.id] = uint64(appendEvents(t, s, d.id, queue.Landed{BatchID: "b1", NewBaseSHA: landed, NewFence: "next"}))
		d.base = landed
	}
	claims := map[QueueID]ClaimedAction{}
	evidence := map[QueueID]DependencyStart{}
	for _, pair := range [][2]*depQueue{{&x, &y}, {&y, &x}} {
		self, source := pair[0], pair[1]
		depSide(t, s, self.id, b, "init", b)
		self.applyPolicy(t, s, b, true, []string{source.key.Repository}, []string{source.key.Repository})
		decl, declID := self.declare(t, s, b, dependency.Basis{}, 1, false, edge(*source, false))
		o := dependency.Observed{Namespace: b.Namespace, Alias: self.alias, ChangeID: string(self.change), Revision: 1, Declaration: dependency.Basis{EventID: declID, Digest: decl.Digest}, Source: source.ref, ReadSequence: 1, SourceRevision: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: source.head}, SourceBase: source.base, Result: "landed"}
		o.ExpectedPrior = depSide(t, s, self.id, b, "observe", o)[0]
		o.ReadSequence = 2
		f := &depFixture{s: s, b: b, c: *self}
		claims[self.id] = f.claimLand(t, "b2")
		evidence[self.id] = DependencyStart{Binding: b, Members: []DependencyMemberStart{{Observations: []dependency.Observed{o}, Sources: []dependency.SourceSettlement{{Reference: source.ref, Queue: source.key, Source: source.alias, Grant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: source.alias.RepositoryID, ReadID: "landing-start", Fresh: true, Allowed: true}, Revision: o.SourceRevision, Commit: source.base, SettlementEventID: settled[source.id], Watermark: settled[source.id], BaseSHA: source.base}}, PolicyCommit: self.policy.Commit, PolicyDigest: self.policy.Digest}}}
	}
	var calls atomic.Int32
	var first sync.WaitGroup
	first.Add(2)
	s.beforeQueueLock = func() {
		if calls.Add(1) <= 2 {
			first.Done()
			first.Wait()
			return
		}
		// Give the other start time to take its first lock before this one
		// requests its second: an inconsistent order would now deadlock.
		time.Sleep(100 * time.Millisecond)
	}
	errs := make(chan error, 2)
	for _, q := range []QueueID{x.id, y.id} {
		go func() {
			land := claims[q].Action.(queue.Land)
			errs <- s.StartDependencyLanding(t.Context(), claims[q], depCfg, cp.Policy{}, nil, nil, queue.LandingStarted{BatchID: land.BatchID, AttemptID: "cycle", FenceToken: land.FenceToken, At: time.Now()}, evidence[q])
		}()
	}
	for range 2 {
		if err := <-errs; !errors.Is(err, ErrLandingDecisionStale) || !strings.Contains(err.Error(), "dependency-unverified") {
			t.Fatal("cycle start admitted, deadlocked or refused before its locks:", err)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("both starts did not reach both locks", calls.Load())
	}
	s.beforeQueueLock = nil
	for _, q := range []QueueID{x.id, y.id} {
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if state.Active == nil || state.Active.Phase != queue.Testing {
			t.Fatal("cycle member started landing")
		}
	}
}

// The approval-policy path links each member companion to its exact decision,
// and every activated admission refuses while an accepted Landed awaits
// RecoverLanded, restricted to its companion and required member inventories.
func TestDependencyStartWithApprovalDecisionsAndPendingRecovery(t *testing.T) {
	t.Run("decision link", func(t *testing.T) {
		s := newStore(t)
  f,claim,c,d:=dependencyDecisionLand(t,s,nil)
  q:=f.c.id
		read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{c.Fresh.Basis.ChangeID})
		must(t, err)
		must(t, s.StartDependencyLanding(read.Context(t.Context()), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, f.evidence()))
		var decisionID, startID uint64
		var raw []byte
		must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'`, q).Scan(&decisionID))
		must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_started'`, q).Scan(&startID))
		must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='dependency_decision_recorded'`, q).Scan(&raw))
		e, err := dependency.DecodeEvent(raw)
		must(t, err)
		if companion := e.(dependency.DecisionRecorded); companion.DecisionID != decisionID || companion.StartEventID != startID || decisionID >= startID {
			t.Fatalf("companion not bound to its decision/start: %+v", companion)
		}
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if len(state.LandingDecisions()) != 1 || state.Active.Phase != queue.Landing {
			t.Fatal("unchanged policy decision/start missing")
		}
	})
	t.Run("pending landed recovery", func(t *testing.T) {
		s := newStore(t)
		q, claim, c, d := decisionLand(t, s)
		must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
		landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
		admit := func() error {
			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			must(t, s.lockQueue(t.Context(), tx, q))
			state, _, err := load(t.Context(), tx, q)
			must(t, err)
			_, err = DependencyStart{Binding: depBinding(t)}.validate(t.Context(), tx, q, dependencyLockSet{}, state, c.Land, c.Start, true)
			return err
		}
		if err := admit(); err == nil || !strings.Contains(err.Error(), "landed recovery required before admission") {
			t.Fatal("admission with pending Landed:", err)
		}
		var raw []byte
		must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded'`, q).Scan(&raw))
		tested, err := journalreader.DecodeEvent(raw)
		must(t, err)
		landed := tested.Provenance.(pj.Landing)
		landed.Stage = "landed"
		landed.Members[0].Mapping.Build.Stage, landed.Members[0].Mapping.Build.ID, landed.Members[0].Mapping.Build.Result = "landed", "recovered", d.LandedSHA
		landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
		_, err = s.RecoverLanded(t.Context(), q, landedID, SideAppend{Count: 1, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Provenance: landed}}, nil }})
		must(t, err)
		if err := admit(); err == nil || strings.Contains(err.Error(), "landed recovery") {
			t.Fatal("recovered queue still refused for recovery, or admitted without dependency facts:", err)
		}
	})
}
