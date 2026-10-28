package store

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	fakeforge "github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// holdActors obtains every author/editor identity and its fresh writer grant
// from the fake forge. The dependency journal keeps only stable subject IDs;
// these fixtures never infer an editor from a body, markdown, or git history.
type holdActors struct{ ids map[string]string }

func newHoldActors(t *testing.T) holdActors {
	t.Helper()
	f := fakeforge.New()
	repo := forge.RepoRef{Path: "acme/client"}
	names := []string{"author", "editor", "submitter", "human", "independent"}
	actors := holdActors{ids: make(map[string]string, len(names))}
	for i, name := range names {
		user := forge.UserIdentity{ID: int64(i + 1), Login: name, Kind: forge.UserAccount}
		f.SetIdentity(user)
		f.SetWriteGrant(repo, user.ID, "write")
	}
	for _, name := range names {
		user, err := f.ResolveUser(t.Context(), name)
		must(t, err)
		grant, err := f.WriteGrant(t.Context(), repo, user)
		must(t, err)
		if !grant.CanWrite(user.ID) {
			t.Fatalf("fake forge did not return a writer grant for %s", name)
		}
		actors.ids[name] = strconv.FormatInt(grant.User.ID, 10)
	}
	return actors
}

func (a holdActors) id(name string) string { return a.ids[name] }

func (a holdActors) attribution(names ...string) dependency.Attribution {
	editors := make([]string, len(names))
	for i, name := range names {
		editors[i] = a.id(name)
	}
	slices.Sort(editors)
	return dependency.Attribution{Editors: slices.Compact(editors)}
}

func (a holdActors) grant(name, repositoryID string) dependency.Grant {
	id := a.id(name)
	return dependency.Grant{SubjectID: id, ReturnedID: id, RepositoryID: repositoryID, ReadID: "fake-write-grant-" + id, Fresh: true, Allowed: true}
}

func (f *depFixture) setHoldPolicy(t *testing.T, actors holdActors, singleMaintainer bool, writers ...string) {
	t.Helper()
	p := dependency.Policy{Mode: "ordered", AllowFrom: []string{f.a.key.Repository}, AllowTo: []string{}, SingleMaintainer: singleMaintainer}
	grants := make([]dependency.Grant, len(writers))
	for i, writer := range writers {
		grants[i] = actors.grant(writer, f.c.alias.RepositoryID)
	}
	next := dependency.PolicyApplied{
		Enabled:       true,
		Namespace:     f.b.Namespace,
		Alias:         f.c.alias,
		Queue:         f.c.key,
		PriorEventID:  f.c.policyID,
		DefaultBranch: "main",
		ReadRef:       "refs/heads/main",
		ReadID:        fmt.Sprintf("fake-default-branch-policy-%d", f.c.policyID+1),
		Commit:        sha('c'),
		Digest:        dependency.PolicyDigest(p, true),
		Policy:        p,
		Writers:       grants,
	}
	f.c.policy, f.c.policyID = next, depSide(t, f.s, f.c.id, f.b, next.ReadID, next)[0]
}

func (f *depFixture) redeclareHold(t *testing.T, actors holdActors, held, unknown bool, editors []string, edges ...dependency.DeclaredEdge) {
	t.Helper()
	if edges == nil {
		edges = []dependency.DeclaredEdge{}
	}
	attribution := actors.attribution(editors...)
	hold := dependency.DeclarationHold{Editors: []string{}}
	if held {
		hold = dependency.DeclarationHold{Held: true, Unknown: unknown, Editors: slices.Clone(attribution.Editors)}
	}
	e := dependency.DependenciesDeclared{
		Hold:           hold,
		Namespace:      f.b.Namespace,
		Alias:          f.c.alias,
		ChangeID:       string(f.c.change),
		Revision:       f.c.revision,
		Queue:          f.c.key,
		Prior:          dependency.Basis{EventID: f.declID, Digest: f.decl.Digest},
		Poll:           f.decl.Poll + 1,
		Digest:         dependency.DeclarationDigest(edges),
		Edges:          edges,
		Bindings:       []dependency.AliasBinding{},
		Attribution:    dependency.Attribution{Editors: attribution.Editors, Unknown: unknown},
		ReadGrant:      actors.grant("author", f.c.alias.RepositoryID),
		Accountability: dependency.Accountability{Author: actors.id("author"), Submitter: actors.id("submitter"), Humans: []string{actors.id("human")}},
	}
	f.decl, f.declID = e, depSide(t, f.s, f.c.id, f.b, fmt.Sprintf("hold-declaration-%d", e.Poll), e)[0]
	f.observed, f.observedID = dependency.Observed{}, 0
}

func (f *depFixture) restoreHoldAttribution(t *testing.T, actors holdActors) {
	t.Helper()
	attribution := actors.attribution("author", "editor")
	e := f.decl
	e.Prior = dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}
	e.Poll++
	e.Redeclaration = true
	e.Hold = dependency.DeclarationHold{Held: true, Editors: slices.Clone(attribution.Editors)}
	e.Attribution = attribution
	e.ReadGrant = actors.grant("author", f.c.alias.RepositoryID)
	e.Accountability = dependency.Accountability{Author: actors.id("author"), Submitter: actors.id("submitter"), Humans: []string{actors.id("human")}}
	f.decl, f.declID = e, depSide(t, f.s, f.c.id, f.b, fmt.Sprintf("hold-attribution-restored-%d", e.Poll), e)[0]
}

func (f *depFixture) acknowledgement(actors holdActors, writer string) dependency.Acknowledged {
	exclusions := append([]string{}, f.decl.Hold.Editors...)
	exclusions = append(exclusions, f.decl.Accountability.Author, f.decl.Accountability.Submitter)
	exclusions = append(exclusions, f.decl.Accountability.Humans...)
	slices.Sort(exclusions)
	return dependency.Acknowledged{
		Namespace:       f.b.Namespace,
		Alias:           f.c.alias,
		Declaration:     dependency.Basis{EventID: f.declID, Digest: f.decl.Digest},
		Writer:          actors.grant(writer, f.c.alias.RepositoryID),
		Exclusions:      slices.Compact(exclusions),
		PolicyEventID:   f.c.policyID,
		GoverningCommit: f.c.policy.Commit,
		PolicyDigest:    f.c.policy.Digest,
	}
}

func (f *depFixture) appendAcknowledgement(t *testing.T, actors holdActors, writer string, self bool) error {
	t.Helper()
	ack := f.acknowledgement(actors, writer)
	var event dependency.Event = ack
	if self {
		event = dependency.SelfAcknowledged{Acknowledged: ack, Overridden: slices.Clone(ack.Exclusions)}
	}
	_, err := f.s.AppendSide(t.Context(), f.c.id, SideAppend{
		Count:     1,
		DedupeKey: fmt.Sprintf("hold-ack-%d-%s-%t", f.declID, writer, self),
		Binding:   f.b,
		Build: func([]uint64) ([]journalreader.Event, error) {
			return []journalreader.Event{{Dependency: event}}, nil
		},
	})
	return err
}

// holdEvidence names no direct sources because the loosening declaration drops
// the final edge. The start path still replays the declaration and policy under
// the queue lock, which is the assertion this slice needs.
func (f *depFixture) holdEvidence(t *testing.T) DependencyStart {
	t.Helper()
	if len(f.decl.Edges) != 0 {
		t.Fatal("hold fixture retained a dependency edge")
	}
	return DependencyStart{Binding: f.b, Members: []DependencyMemberStart{{PolicyCommit: f.c.policy.Commit, PolicyDigest: f.c.policy.Digest}}}
}

func (f *depFixture) declarationEvidence() DependencyStart {
	o := f.observed
	o.ExpectedPrior, o.ReadSequence, o.Result, o.SourceBase = f.observedID, o.ReadSequence+1, "landed", f.landedA
	source := dependency.SourceSettlement{Reference: f.decl.Edges[0].Reference, Queue: f.a.key, Source: f.a.alias, Grant: dependency.Grant{SubjectID: f.decl.ReadGrant.SubjectID, ReturnedID: f.decl.ReadGrant.ReturnedID, RepositoryID: f.a.alias.RepositoryID, ReadID: "landing-start-read", Fresh: true, Allowed: true}, Revision: o.SourceRevision, Commit: f.landedA, SettlementEventID: f.settlement, Watermark: f.watermark, BaseSHA: f.landedA}
	return DependencyStart{Binding: f.b, Members: []DependencyMemberStart{{Observations: []dependency.Observed{o}, Sources: []dependency.SourceSettlement{source}, PolicyCommit: f.c.policy.Commit, PolicyDigest: f.c.policy.Digest}}}
}

func (f *depFixture) observeDeclaredEdge(t *testing.T, result, base string) {
	t.Helper()
	if len(f.decl.Edges) != 1 {
		t.Fatal("fixture needs exactly one declared edge")
	}
	o := dependency.Observed{Namespace: f.b.Namespace, Alias: f.c.alias, ChangeID: string(f.c.change), Revision: f.c.revision, Declaration: dependency.Basis{EventID: f.declID, Digest: f.decl.Digest}, Source: f.decl.Edges[0].Reference, ExpectedPrior: f.observedID, ReadSequence: f.observed.ReadSequence + 1, SourceRevision: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: f.a.head}, SourceBase: base, Result: result}
	f.observedID = depSide(t, f.s, f.c.id, f.b, fmt.Sprintf("observe-declared-%d-%d", f.declID, o.ReadSequence), o)[0]
	f.observed = o
}

func dependencyHoldState(t *testing.T, f *depFixture) dependency.State {
	t.Helper()
	reader, _, err := replayJournal(t.Context(), f.s.pool, f.c.id, queue.State{BaseBranch: "main", Fence: "fence-acme/client"}, 0, true)
	must(t, err)
	state, err := reader.BoundDependencies(f.b)
	must(t, err)
	return state
}

func assertHoldBlocksPlannerAndStart(t *testing.T, f *depFixture, claim ClaimedAction, evidence DependencyStart) {
	t.Helper()
	state, _, err := f.s.Load(f.ctx(t.Context()), f.c.id)
	must(t, err)
	if !state.DependencyGated(queue.BatchPR{PR: f.c.pr, HeadSHA: f.c.head}) {
		t.Fatal("declaration hold did not gate the planner")
	}
	before := eventCount(t, f.s, f.c.id)
	err = f.start(t.Context(), claim, evidence)
	if !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatalf("held dependency start error = %v, want stale refusal", err)
	}
	assertNoLandingSideEffects(t, f.s, f.c.id, claim, before)
}

func heldClaim(t *testing.T, f *depFixture, actors holdActors, singleMaintainer, unknown bool, writers ...string) ClaimedAction {
	t.Helper()
	f.observe(t, "landed", f.landedA)
	claim := f.claimLand(t)
	f.setHoldPolicy(t, actors, singleMaintainer, writers...)
	f.redeclareHold(t, actors, true, unknown, []string{"author", "editor"})
	return claim
}

func TestDependencyDeclarationHoldRefusalsGatePlannerAndStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*testing.T, *depFixture, holdActors) ClaimedAction
		attempt func(*testing.T, *depFixture, holdActors)
		recover func(*testing.T, *depFixture, holdActors, ClaimedAction)
	}{
		{
			name: "writer author editor exclusion is strict by default",
			setup: func(t *testing.T, f *depFixture, actors holdActors) ClaimedAction {
				return heldClaim(t, f, actors, false, false, "author", "independent")
			},
			attempt: func(t *testing.T, f *depFixture, actors holdActors) {
				for _, writer := range []string{"author", "editor", "submitter", "human"} {
					if err := f.appendAcknowledgement(t, actors, writer, false); !errors.Is(err, dependency.ErrHeld) {
						t.Fatalf("excluded %s acknowledgement error = %v", writer, err)
					}
				}
			},
		},
		{
			name: "head only opt in is not governing policy evidence",
			setup: func(t *testing.T, f *depFixture, actors holdActors) ClaimedAction {
				return heldClaim(t, f, actors, false, false, "author")
			},
			attempt: func(t *testing.T, f *depFixture, actors holdActors) {
				// A policy read from B's change head is deliberately absent from the
				// journal. Its digest cannot substitute for the default-branch read.
				head := f.c.policy.Policy
				head.SingleMaintainer = true
				ack := f.acknowledgement(actors, "author")
				ack.PolicyDigest = dependency.PolicyDigest(head, true)
				_, err := f.s.AppendSide(t.Context(), f.c.id, SideAppend{Count: 1, DedupeKey: "head-only-self-ack", Binding: f.b, Build: func([]uint64) ([]journalreader.Event, error) {
					return []journalreader.Event{{Dependency: dependency.SelfAcknowledged{Acknowledged: ack, Overridden: slices.Clone(ack.Exclusions)}}}, nil
				}})
				if !errors.Is(err, dependency.ErrStale) {
					t.Fatalf("head-only governing evidence error = %v", err)
				}
				if err := f.appendAcknowledgement(t, actors, "author", true); !errors.Is(err, dependency.ErrHeld) {
					t.Fatalf("default policy unexpectedly allowed self acknowledgement: %v", err)
				}
			},
		},
		{
			name: "multi writer repository remains strict without opt in",
			setup: func(t *testing.T, f *depFixture, actors holdActors) ClaimedAction {
				return heldClaim(t, f, actors, false, false, "author", "independent")
			},
			attempt: func(t *testing.T, f *depFixture, actors holdActors) {
				if err := f.appendAcknowledgement(t, actors, "author", true); !errors.Is(err, dependency.ErrHeld) {
					t.Fatalf("strict multi-writer self acknowledgement error = %v", err)
				}
			},
		},
		{
			name: "unresolved attribution blocks independent and self acknowledgement",
			setup: func(t *testing.T, f *depFixture, actors holdActors) ClaimedAction {
				return heldClaim(t, f, actors, true, true, "author", "independent")
			},
			attempt: func(t *testing.T, f *depFixture, actors holdActors) {
				for _, ack := range []struct {
					writer string
					self   bool
				}{{"independent", false}, {"author", true}} {
					if err := f.appendAcknowledgement(t, actors, ack.writer, ack.self); !errors.Is(err, dependency.ErrHeld) {
						t.Fatalf("unresolved attribution %s/self=%t error = %v", ack.writer, ack.self, err)
					}
				}
			},
			recover: func(t *testing.T, f *depFixture, actors holdActors, claim ClaimedAction) {
				before := f.acknowledgement(actors, "independent").Exclusions
				f.restoreHoldAttribution(t, actors)
				after := f.acknowledgement(actors, "independent").Exclusions
				if !slices.Equal(before, after) {
					t.Fatalf("attribution recovery changed exclusions: %v -> %v", before, after)
				}
				if got := dependencyHoldState(t, f).Hold(f.c.alias); !got.Held || got.UnknownEditor {
					t.Fatalf("authenticated redeclaration did not retain a known hold: %+v", got)
				}
				must(t, f.appendAcknowledgement(t, actors, "independent", false))
				must(t, f.start(t.Context(), claim, f.holdEvidence(t)))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDepFixture(t)
			actors := newHoldActors(t)
			claim := tc.setup(t, f, actors)
			tc.attempt(t, f, actors)
			assertHoldBlocksPlannerAndStart(t, f, claim, f.holdEvidence(t))
			if tc.recover != nil {
				tc.recover(t, f, actors, claim)
			}
		})
	}
}

func TestDependencySelfAcknowledgementAndTighteningThroughStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *depFixture, holdActors)
	}{
		{
			name: "explicit opted in self acknowledgement clears the hold",
			run: func(t *testing.T, f *depFixture, actors holdActors) {
				f.observeDeclaredEdge(t, "landed", f.landedA)
				f.setHoldPolicy(t, actors, true, "author")
				f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
				must(t, f.appendAcknowledgement(t, actors, "author", true))
				claim := f.claimLand(t)
				must(t, f.start(t.Context(), claim, f.holdEvidence(t)))
				if got := dependencyHoldState(t, f).Hold(f.c.alias); got.Held || got.LastAcknowledgementKind != "dependency_declaration_self_acknowledged" {
					t.Fatalf("self acknowledgement was not retained distinctly: %+v", got)
				}
			},
		},
		{
			name: "multi writer opted in self acknowledgement starts landing",
			run: func(t *testing.T, f *depFixture, actors holdActors) {
				f.observe(t, "landed", f.landedA)
				f.setHoldPolicy(t, actors, true, "author", "independent")
				f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
				must(t, f.appendAcknowledgement(t, actors, "author", true))
				if got := dependencyHoldState(t, f).Hold(f.c.alias); got.Held || !got.MultiWriterWarning {
					t.Fatalf("opted-in multi-writer acknowledgement/warning = %+v", got)
				}
				claim := f.claimLand(t)
				must(t, f.start(t.Context(), claim, f.holdEvidence(t)))
			},
		},
		{
			name: "landing start revokes a previously audited self acknowledgement",
			run: func(t *testing.T, f *depFixture, actors holdActors) {
				f.observe(t, "landed", f.landedA)
				f.setHoldPolicy(t, actors, true, "author", "independent")
				f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
				must(t, f.appendAcknowledgement(t, actors, "author", true))
				claim := f.claimLand(t)
				fresh := f.holdEvidence(t)
				freshPolicy := f.c.policy.Policy
				freshPolicy.SingleMaintainer = false
				fresh.Members[0].PolicyDigest = dependency.PolicyDigest(freshPolicy, true)
				before := eventCount(t, f.s, f.c.id)
				err := f.start(t.Context(), claim, fresh)
				if !errors.Is(err, ErrLandingDecisionStale) {
					t.Fatalf("landing start accepted changed governing policy evidence: %v", err)
				}
				assertNoLandingSideEffects(t, f.s, f.c.id, claim, before)
				f.setHoldPolicy(t, actors, false, "author", "independent")
				if got := dependencyHoldState(t, f).Hold(f.c.alias); !got.Held || !got.AcknowledgementRevoked || got.LastAcknowledgementKind != "dependency_declaration_self_acknowledged" {
					t.Fatalf("revoked self acknowledgement = %+v", got)
				}
				assertHoldBlocksPlannerAndStart(t, f, claim, f.holdEvidence(t))
				must(t, f.appendAcknowledgement(t, actors, "independent", false))
				must(t, f.start(t.Context(), claim, f.holdEvidence(t)))
				var selfAcknowledgements int
				must(t, f.s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1 AND type='dependency_declaration_self_acknowledged'`, f.c.id).Scan(&selfAcknowledgements))
				if selfAcknowledgements != 1 {
					t.Fatalf("audited self acknowledgement was not retained: %d", selfAcknowledgements)
				}
			},
		},
		{
			name: "tightening alone needs no acknowledgement",
			run: func(t *testing.T, f *depFixture, actors holdActors) {
				f.redeclareHold(t, actors, false, false, []string{"author", "editor"}, edge(f.a, true))
				if got := dependencyHoldState(t, f).Hold(f.c.alias); got.Held {
					t.Fatalf("tightening created a declaration hold: %+v", got)
				}
				f.observeDeclaredEdge(t, "landed", f.landedA)
				claim := f.claimLand(t)
				must(t, f.start(t.Context(), claim, f.declarationEvidence()))
			},
		},
		{
			name: "tightening after loosening retains its hold",
			run: func(t *testing.T, f *depFixture, actors holdActors) {
				f.observeDeclaredEdge(t, "landed", f.landedA)
				claim := f.claimLand(t)
				f.setHoldPolicy(t, actors, false, "author", "independent")
				f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
				f.redeclareHold(t, actors, true, false, []string{"author", "editor"}, edge(f.a, true))
				f.observeDeclaredEdge(t, "landed", f.landedA)
				if got := dependencyHoldState(t, f).Hold(f.c.alias); !got.Held {
					t.Fatalf("tightening cleared an existing hold: %+v", got)
				}
				assertHoldBlocksPlannerAndStart(t, f, claim, f.declarationEvidence())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDepFixture(t)
			tc.run(t, f, newHoldActors(t))
		})
	}
}

func TestDependencyStrictMultiWriterHoldSurvivesNativeApproval(t *testing.T) {
	s := newStore(t)
	q, claim, c, decision := decisionLand(t, s)
	f := &depFixture{s: s, b: depBinding(t)}
	f.c = depQueue{
		id:       q,
		key:      dependency.QueueKey{Repository: "https://github.com/Owner/Repo", Base: "release"},
		alias:    dependency.SourceAlias{ForgeInstance: f.b.ForgeInstance, Installation: "7", RepositoryID: "id-owner-repo", Number: uint64(c.Land.PRs[0].PR)},
		change:   c.Fresh.Basis.ChangeID,
		revision: uint64(c.Fresh.Basis.Revision),
	}
	f.landSource(t)
	f.c.applyPolicy(t, s, f.b, true, []string{f.a.key.Repository}, []string{})
	f.decl, f.declID = f.c.declare(t, s, f.b, dependency.Basis{}, 1, false, edge(f.a, false))
	actors := newHoldActors(t)
	f.setHoldPolicy(t, actors, false, "author", "independent")
	f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
	if err := f.appendAcknowledgement(t, actors, "author", true); !errors.Is(err, dependency.ErrHeld) {
		t.Fatalf("strict self acknowledgement error = %v", err)
	}
	read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{c.Fresh.Basis.ChangeID})
	must(t, err)
	before := eventCount(t, s, q)
	err = s.StartDependencyLanding(read.Context(t.Context()), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{decision}, c.Start, f.holdEvidence(t))
	if !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatalf("native approval bypassed a strict multi-writer hold: %v", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
}

func TestDependencyOptedSelfAcknowledgementStillRequiresApprovalDecision(t *testing.T) {
	s := newStore(t)
	q, claim, c, decision := decisionLand(t, s)
	f := &depFixture{s: s, b: depBinding(t)}
	f.c = depQueue{
		id:       q,
		key:      dependency.QueueKey{Repository: "https://github.com/Owner/Repo", Base: "release"},
		alias:    dependency.SourceAlias{ForgeInstance: f.b.ForgeInstance, Installation: "7", RepositoryID: "id-owner-repo", Number: uint64(c.Land.PRs[0].PR)},
		change:   c.Fresh.Basis.ChangeID,
		revision: uint64(c.Fresh.Basis.Revision),
	}
	f.landSource(t)
	f.c.applyPolicy(t, s, f.b, true, []string{f.a.key.Repository}, []string{})
	f.decl, f.declID = f.c.declare(t, s, f.b, dependency.Basis{}, 1, false, edge(f.a, false))
	actors := newHoldActors(t)
	f.setHoldPolicy(t, actors, true, "author")
	f.redeclareHold(t, actors, true, false, []string{"author", "editor"})
	must(t, f.appendAcknowledgement(t, actors, "author", true))
	read, err := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{c.Fresh.Basis.ChangeID})
	must(t, err)
	before := eventCount(t, s, q)
	err = s.StartDependencyLanding(read.Context(t.Context()), claim, c.Config, c.Policy, nil, nil, c.Start, f.holdEvidence(t))
	if !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatalf("self acknowledgement bypassed ordinary approval decision: %v", err)
	}
	assertNoLandingSideEffects(t, s, q, claim, before)
	must(t, s.StartDependencyLanding(read.Context(t.Context()), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{decision}, c.Start, f.holdEvidence(t)))
	var decisionID uint64
	var raw []byte
	must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_decision_recorded'`, q).Scan(&decisionID))
	must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='dependency_decision_recorded'`, q).Scan(&raw))
	e, err := dependency.DecodeEvent(raw)
	must(t, err)
	if companion := e.(dependency.DecisionRecorded); companion.DecisionID != decisionID {
		t.Fatalf("self acknowledgement start omitted ordinary decision binding: %+v", companion)
	}
}
