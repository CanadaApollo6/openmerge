package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func depSide(t *testing.T, s *store.Store, q store.QueueID, b dependency.JournalInitialized, key string, e dependency.Event) uint64 {
	t.Helper()
	var id uint64
	_, err := s.AppendSide(t.Context(), q, store.SideAppend{Count: 1, DedupeKey: key, Binding: b, Build: func(ids []uint64) ([]journalreader.Event, error) {
		id = ids[0]
		return []journalreader.Event{{Dependency: e}}, nil
	}})
	must(t, err)
	return id
}

func depPolicy(b dependency.JournalInitialized, alias dependency.SourceAlias, q dependency.QueueKey, from, to []string) dependency.PolicyApplied {
	p := dependency.Policy{Mode: "ordered", AllowFrom: from, AllowTo: to}
	return dependency.PolicyApplied{Enabled: true, Namespace: b.Namespace, Alias: alias, Queue: q, DefaultBranch: "main", ReadRef: "refs/heads/main", ReadID: "policy-read", Commit: strings.Repeat("c", 40), Digest: dependency.PolicyDigest(p, true), Policy: p, Writers: []dependency.Grant{}}
}

// The worker plans B only after A's recorded landing, starts B through the
// sanctioned dependency writer, and drains landed recovery before every cycle
// and execution: deleting either recover call lets a blocked worker admit work.
func TestDependencyWorkerGateStartAndRecoveryBeforeAdmission(t *testing.T) {
	t.Parallel()
	db, conn := databaseConnection(t)
	s, w, f, _ := fixtureWithStore(t, repositoryConfig, db)
	ctx := t.Context()
	var token [32]byte
	token[0] = 5
	b, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	scope := dependency.Scope{ForgeInstance: b.ForgeInstance, Installation: b.Installation, JournalNamespace: b.Namespace}

	// A (owner/api#42) lands in its own queue in this database.
	aKey := dependency.QueueKey{Repository: "https://github.com/owner/api", Base: "main"}
	bKey := dependency.QueueKey{Repository: "https://github.com/owner/repo", Base: "main"}
	a, err := s.store.EnsureQueue(ctx, store.QueueKey{Installation: "7", Repository: "owner/api", BaseBranch: "main"})
	must(t, err)
	must(t, s.store.InitializeFence(ctx, a, "fence-a"))
	aChange, err := queue.PRChangeID("7", "owner/api", "main", "", 42)
	must(t, err)
	aHead, aLanded, now := strings.Repeat("a", 40), strings.Repeat("b", 40), time.Now()
	_, err = s.store.Append(ctx, a, []queue.Event{queue.BaseMoved{SHA: strings.Repeat("0", 40)}, queue.PRObserved{PR: 42, HeadSHA: aHead, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 42, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: strings.Repeat("0", 40), PRs: []queue.BatchPR{{PR: 42, HeadSHA: aHead}}, At: now}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: aLanded, Method: queue.Merge, At: now}, queue.ChecksCompleted{TestedSHA: aLanded, Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "a1", FenceToken: "fence-a", At: now}}, "")
	must(t, err)
	settlement, err := s.store.Append(ctx, a, []queue.Event{queue.Landed{BatchID: "b1", NewBaseSHA: aLanded, NewFence: "next"}}, "")
	must(t, err)
	aAlias := dependency.SourceAlias{ForgeInstance: b.ForgeInstance, Installation: "7", RepositoryID: "id-owner-api", Number: 42}
	depSide(t, s.store, a, b, "init", b)
	depSide(t, s.store, a, b, "policy", depPolicy(b, aAlias, aKey, []string{}, []string{bKey.Repository}))

	// B (this worker's owner/repo#1) declares a revision-less edge to A.
	bAlias := dependency.SourceAlias{ForgeInstance: b.ForgeInstance, Installation: "7", RepositoryID: "id-owner-repo", Number: 1}
	bChange, err := queue.PRChangeID("7", "owner/repo", "main", "", 1)
	must(t, err)
	ref, err := dependency.Reference{Scope: scope, ChangeID: string(aChange)}.Canonical()
	must(t, err)
	depSide(t, s.store, w.id, b, "init", b)
	policy := depPolicy(b, bAlias, bKey, []string{aKey.Repository}, []string{})
	depSide(t, s.store, w.id, b, "policy", policy)
	edges := []dependency.DeclaredEdge{{Reference: ref, Queue: aKey, Baseline: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: aHead}}}
	decl := dependency.DependenciesDeclared{Hold: dependency.DeclarationHold{Editors: []string{}}, Namespace: b.Namespace, Alias: bAlias, ChangeID: string(bChange), Revision: 1, Queue: bKey, Poll: 1, Digest: dependency.DeclarationDigest(edges), Edges: edges, Bindings: []dependency.AliasBinding{}, Attribution: dependency.Attribution{Editors: []string{"author"}}, ReadGrant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: "id-owner-repo", ReadID: "bind", Fresh: true, Allowed: true}, Accountability: dependency.Accountability{Author: "author", Humans: []string{}}}
	declID := depSide(t, s.store, w.id, b, "declare", decl)
	observed := dependency.Observed{Namespace: b.Namespace, Alias: bAlias, ChangeID: string(bChange), Revision: 1, Declaration: dependency.Basis{EventID: declID, Digest: decl.Digest}, Source: ref, ReadSequence: 1, SourceRevision: edges[0].Baseline, SourceBase: aLanded, Result: "landed"}

	var trace []string
	var recoverErr error
	var observedID uint64
	w.dependencies = &dependencyPass{binding: b,
		recover: func(ctx context.Context, q store.QueueID) error {
			trace = append(trace, "recover")
			if recoverErr != nil {
				return recoverErr
			}
			return s.store.RecoverProvenance(ctx, q)
		},
		landingStart: func(_ context.Context, _ queue.Land, _ queue.LandingStarted) (store.DependencyStart, error) {
			trace = append(trace, "landing-start")
			o := observed
			o.ExpectedPrior, o.ReadSequence = observedID, 2
			return store.DependencyStart{Binding: b, Members: []store.DependencyMemberStart{{Observations: []dependency.Observed{o}, Sources: []dependency.SourceSettlement{{Reference: ref, Queue: aKey, Source: aAlias, Grant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: "id-owner-api", ReadID: "landing-start", Fresh: true, Allowed: true}, Revision: o.SourceRevision, Commit: aLanded, SettlementEventID: uint64(settlement), Watermark: uint64(settlement), BaseSHA: aLanded}}, PolicyCommit: policy.Commit, PolicyDigest: policy.Digest}}}, nil
		},
	}

	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(ctx, w))
	state, _, err := s.store.Load(ctx, w.id)
	must(t, err)
	// The cycle's own plan is gated: no build is planned, claimed or cancelled.
	var builds int
	must(t, conn.QueryRow(ctx, `SELECT count(*) FROM actions WHERE queue_id=$1 AND type='build_test_ref'`, w.id).Scan(&builds))
	if state.Active != nil || len(state.Entries) != 1 || builds != 0 || len(trace) == 0 || trace[0] != "recover" {
		t.Fatal("unobserved dependency was planned or tested", state.Active, builds, trace)
	}
	observedID = depSide(t, s.store, w.id, b, "work-read", observed)
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(ctx, w))
	state, _, err = s.store.Load(ctx, w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing {
		t.Fatal("landed dependency not admitted", state.Active)
	}
	f.SetCheckRuns(repo, state.Active.TestedSHA, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: state.Active.TestRef}})

	// Failing recovery must stop the cycle before planning, and execution
	// before any landing-start read or durable start.
	recoverErr = errors.New("pending landed recovery")
	_, before, err := s.store.Load(ctx, w.id)
	must(t, err)
	if err := s.cycle(ctx, w); !errors.Is(err, recoverErr) {
		t.Fatal("cycle admitted work without recovery", err)
	}
	cfg := w.plannerConfig(s.options.PollInterval)
	activated := store.WithDependencyActivation(ctx, w.id, b)
	time.Sleep(2 * time.Millisecond)
	_, err = s.store.Plan(activated, w.id, cfg, time.Now())
	must(t, err)
	claims, err := s.store.ClaimQueue(activated, w.id, 1, time.Minute, time.Now())
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("missing claim", claims)
	}
	if err := s.execute(ctx, w, claims[0]); !errors.Is(err, recoverErr) {
		t.Fatal("execution admitted work without recovery", err)
	}
	if _, after, err := s.store.Load(ctx, w.id); err != nil || after != before || strings.Contains(strings.Join(trace, ","), "landing-start") {
		t.Fatal("blocked recovery appended or read landing-start evidence", err, trace)
	}
	recoverErr = nil
	must(t, s.execute(ctx, w, claims[0]))
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(ctx, w))
	state, _, err = s.store.Load(ctx, w.id)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 0 {
		t.Fatal("dependency start did not land", state.Active)
	}
	journals, err := s.store.ReportingJournals(ctx, []store.QueueKey{w.Key})
	must(t, err)
	var kinds []string
	for _, e := range journals[0].Events {
		decoded, err := journalreader.DecodeEvent(e.Payload)
		must(t, err)
		switch {
		case decoded.Dependency != nil:
			kind, _, _ := dependency.EncodeEvent(decoded.Dependency)
			kinds = append(kinds, kind)
		case decoded.Queue != nil:
			if _, ok := decoded.Queue.(queue.LandingStarted); ok {
				kinds = append(kinds, "landing_started")
			}
		}
	}
	if got := strings.Join(kinds, ","); !strings.HasSuffix(got, "dependency_observed,dependency_composition_recorded,landing_started,dependency_observed,dependency_decision_recorded") {
		t.Fatal("start was not atomic with its companion:", got)
	}
	for i, step := range trace {
		if step == "landing-start" && (i == 0 || trace[i-1] != "recover") {
			t.Fatal("landing-start without preceding recovery", trace)
		}
	}
}
