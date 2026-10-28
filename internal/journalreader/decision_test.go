package journalreader

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// decisionJournal replays one consumer through its durable start. When
// observeFirst is set, the landing-start read precedes LandingStarted.
func decisionJournal(t *testing.T, observeFirst bool) (Reader, dependency.DecisionRecorded, map[uint64][]byte) {
	t.Helper()
	var token [32]byte
	token[0] = 6
	b, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	scope := dependency.Scope{ForgeInstance: b.ForgeInstance, Installation: b.Installation, JournalNamespace: b.Namespace}
	aChange, err := queue.PRChangeID("7", "owner/api", "main", "", 42)
	must(t, err)
	bChange, err := queue.PRChangeID("7", "owner/repo", "main", "", 1)
	must(t, err)
	ref, err := dependency.Reference{Scope: scope, ChangeID: string(aChange)}.Canonical()
	must(t, err)
	alias := dependency.SourceAlias{ForgeInstance: b.ForgeInstance, Installation: "7", RepositoryID: "id-repo", Number: 1}
	bKey := dependency.QueueKey{Repository: "https://github.com/owner/repo", Base: "main"}
	aKey := dependency.QueueKey{Repository: "https://github.com/owner/api", Base: "main"}
	p := dependency.Policy{Mode: "ordered", AllowFrom: []string{aKey.Repository}, AllowTo: []string{}}
	edges := []dependency.DeclaredEdge{{Reference: ref, Queue: aKey, Baseline: dependency.Baseline{Revision: 1, IntentDigest: strings.Repeat("9", 64), Head: strings.Repeat("a", 40)}}}
	decl := dependency.DependenciesDeclared{Hold: dependency.DeclarationHold{Editors: []string{}}, Namespace: b.Namespace, Alias: alias, ChangeID: string(bChange), Revision: 1, Queue: bKey, Poll: 1, Digest: dependency.DeclarationDigest(edges), Edges: edges, Bindings: []dependency.AliasBinding{}, Attribution: dependency.Attribution{Editors: []string{"author"}}, ReadGrant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: "id-repo", ReadID: "bind", Fresh: true, Allowed: true}, Accountability: dependency.Accountability{Author: "author", Humans: []string{}}}
	head, tested, at := strings.Repeat("e", 40), strings.Repeat("f", 40), time.Unix(1, 0).UTC()
	observed := dependency.Observed{Namespace: b.Namespace, Alias: alias, ChangeID: string(bChange), Revision: 1, Declaration: dependency.Basis{EventID: 3, Digest: decl.Digest}, Source: ref, ReadSequence: 1, SourceRevision: edges[0].Baseline, SourceBase: strings.Repeat("b", 40), Result: "landed"}
	events := []any{b, dependency.PolicyApplied{Enabled: true, Namespace: b.Namespace, Alias: alias, Queue: bKey, DefaultBranch: "main", ReadRef: "refs/heads/main", ReadID: "policy", Commit: strings.Repeat("c", 40), Digest: dependency.PolicyDigest(p, true), Policy: p, Writers: []dependency.Grant{}}, decl,
		queue.BaseMoved{SHA: strings.Repeat("0", 40)}, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at},
		queue.BatchStarted{BatchID: "b1", BaseSHA: strings.Repeat("0", 40), PRs: []queue.BatchPR{{PR: 1, HeadSHA: head}}, At: at}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: tested, Method: queue.Merge, At: at}, queue.ChecksCompleted{TestedSHA: tested, Success: true}}
	start := queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "f0", At: at}
	if observeFirst {
		events = append(events, observed, start)
	} else {
		events = append(events, start, observed)
	}
	journal := map[uint64][]byte{}
	r := New(queue.State{BaseBranch: "main", Fence: "f0"}, b, func(id uint64) ([]byte, error) {
		if raw, ok := journal[id]; ok {
			return raw, nil
		}
		return nil, errors.New("missing")
	})
	for i, e := range events {
		id := uint64(i + 1)
		journal[id] = encode(t, e)
		_, err := r.Apply(id, journal[id])
		must(t, err)
	}
	deps, err := r.BoundDependencies(b)
	must(t, err)
	a := deps.Admission(string(bChange), 1)
	if a.Code != "" {
		t.Fatal(a.Code)
	}
	startID := uint64(10)
	if observeFirst {
		startID = 11
	}
	return r, dependency.DecisionRecorded{Namespace: b.Namespace, BatchID: "b1", AttemptID: "attempt", TestedSHA: tested, LandedSHA: tested, StartEventID: startID, Members: 1, Alias: alias, ChangeID: string(bChange), Revision: 1, Head: head, Admission: a.Basis,
		Sources: []dependency.SourceSettlement{{Reference: ref, Queue: aKey, Source: dependency.SourceAlias{ForgeInstance: b.ForgeInstance, Installation: "7", RepositoryID: "id-api", Number: 42}, Grant: dependency.Grant{SubjectID: "author", ReturnedID: "author", RepositoryID: "id-api", ReadID: "start", Fresh: true, Allowed: true}, Revision: observed.SourceRevision, Commit: observed.SourceBase, SettlementEventID: 1, Watermark: 1, BaseSHA: observed.SourceBase}}}, journal
}

func TestDependencyDecisionBindsDurableStart(t *testing.T) {
	r, c, journal := decisionJournal(t, false)
	apply := func(r Reader, c dependency.DecisionRecorded) error {
		raw := encode(t, c)
		journal[12] = raw
		_, err := r.Apply(12, raw)
		return err
	}
	if err := apply(r, c); err != nil {
		t.Fatal("valid companion", err)
	}
	for name, mutate := range map[string]func(*dependency.DecisionRecorded){
		"start is not LandingStarted":   func(c *dependency.DecisionRecorded) { c.StartEventID = 9 },
		"decision without a decision":   func(c *dependency.DecisionRecorded) { c.DecisionID = 5 },
		"other attempt":                 func(c *dependency.DecisionRecorded) { c.AttemptID = "other" },
		"other member head":             func(c *dependency.DecisionRecorded) { c.Head = strings.Repeat("d", 40) },
		"batch size differs":            func(c *dependency.DecisionRecorded) { c.Members = 2 },
		"intermediate landed composite": func(c *dependency.DecisionRecorded) { c.LandedSHA = strings.Repeat("d", 40) },
	} {
		mutated := c
		mutate(&mutated)
		if err := apply(r, mutated); !errors.Is(err, ErrReference) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A read recorded before the durable start cannot certify that start.
	early, c, journal := decisionJournal(t, true)
	raw := encode(t, c)
	journal[12] = raw
	if _, err := early.Apply(12, raw); !errors.Is(err, ErrReference) {
		t.Fatal("pre-start observation certified a landing start", err)
	}
}
