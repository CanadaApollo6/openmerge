package dependency_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	dep "github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func reconcileFixture(t *testing.T) (dep.State, dep.JournalInitialized, *fake.Forge, dep.ObservationRequest) {
	t.Helper()
	r := dependencytest.Request()
	var token [32]byte
	token[0] = 1
	init, err := dep.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, r.Registry.Scope.ForgeInstance, r.Registry.Scope.Installation)
	if err != nil {
		t.Fatal(err)
	}
	s, err := dep.Apply(dep.State{}, 1, init)
	if err != nil {
		t.Fatal(err)
	}
	s, err = dep.Apply(s, 3, r.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	f := fake.New()
	f.SetIdentity(r.Declarer)
	f.SetWriteGrant(forge.RepoRef{Path: "acme/project"}, r.Declarer.ID, "read")
	f.SetDependencySource(forge.RepoRef{Path: "acme/project"}, dependencytest.Source(r))
	return s, init, f, r
}

func applyObservation(t *testing.T, s dep.State, id uint64, o dep.ScopedObservation) dep.State {
	t.Helper()
	next, err := dep.Apply(s, id, o.Evidence().Observation)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestReverseWakesRestartAndPollingRepair(t *testing.T) {
	s, init, f, r := reconcileFixture(t)
	if _, err := (dep.ReverseIndex{}).Wake(r.Source.Reference, 3); !errors.Is(err, dep.ErrStale) {
		t.Fatal("unbuilt index admitted work", err)
	}
	index, err := dep.RebuildReverse(s, init)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := index.Wake(r.Source.Reference, 3)
	if err != nil || !reflect.DeepEqual(wake, []string{r.Consumer.ChangeID}) {
		t.Fatal(wake, err)
	}
	wake[0] = "mutated"
	wake, err = index.Wake(r.Source.Reference, 3)
	if err != nil || wake[0] != r.Consumer.ChangeID {
		t.Fatal("index alias mutated", err)
	}
	foreign := r.Source.Reference
	foreign.Scope.Installation = "8"
	if _, err := index.Wake(foreign, 3); !errors.Is(err, dep.ErrOutOfScope) {
		t.Fatal(err)
	}
	invalid := r.Source.Reference
	invalid.ChangeID = "invalid"
	if _, err := index.Wake(invalid, 3); !errors.Is(err, dep.ErrMalformed) {
		t.Fatal("noncanonical source accepted", err)
	}
	// No wake is delivered. Polling still enumerates B and reads A's new landing.
	r.Source.Landing = &dep.SourceLanding{Revision: r.Source.Current, Commit: strings.Repeat("d", 40), SettlementEventID: 2, BaseSHA: r.Source.BaseSHA, OnBase: true}
	r.Stage = dep.WorkRead
	if changes := dep.PollDeclarations(s); len(changes) != 1 || changes[0].ChangeID != r.Consumer.ChangeID {
		t.Fatal(changes)
	}
	observations, err := dep.ReconcileEdges(t.Context(), f, s, []dep.ObservationRequest{r})
	if err != nil || len(observations) != 1 || observations[0].Evidence().Observation.Result != "landed" {
		t.Fatal(observations, err)
	}
	s = applyObservation(t, s, 4, observations[0])
	if _, err := index.Wake(r.Source.Reference, 4); !errors.Is(err, dep.ErrStale) {
		t.Fatal("stale index was treated as complete", err)
	}
	raw, err := dep.EncodeSnapshot(queue.State{}, s)
	if err != nil {
		t.Fatal(err)
	}
	_, restored, err := dep.DecodeSnapshot(raw, init)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := dep.RebuildReverse(restored, init)
	if err != nil {
		t.Fatal(err)
	}
	wake, err = rebuilt.Wake(r.Source.Reference, 4)
	if err != nil || !reflect.DeepEqual(wake, []string{r.Consumer.ChangeID}) {
		t.Fatal("restart lost wakes", wake, err)
	}
	// Dequeue is represented by admission evidence: no source is auto-enqueued.
	r.Admission.Enqueued = false
	if _, err := dep.ReconcileEdges(t.Context(), f, restored, []dep.ObservationRequest{r}); err == nil {
		t.Fatal("poll bypassed dequeue")
	}
}

func TestReconcilePartialReadsAndDelayedSuccess(t *testing.T) {
	s, _, f, r := reconcileFixture(t)
	second := r
	second.Source.Reference.ChangeID = "chg1_" + strings.Repeat("3", 64)
	second.Edge.Reference, _ = second.Source.Reference.Canonical()
	second.Source.Alias.Number = 3
	r.Consumer.Edges = append(r.Consumer.Edges, second.Edge)
	r.Consumer.Digest = dep.DeclarationDigest(r.Consumer.Edges)
	r.Consumer.Prior = s.Prior(r.Consumer.Alias)
	r.Consumer.Poll++
	var err error
	s, err = dep.Apply(s, 4, r.Consumer)
	if err != nil {
		t.Fatal(err)
	}
	r.Declaration = s.Prior(r.Consumer.Alias)
	second.Consumer = r.Consumer
	second.Declaration = r.Declaration
	f.SetDependencySource(forge.RepoRef{Path: "acme/project"}, dependencytest.Source(second))
	f.FailNext("DependencySource", errors.New("partial read unavailable"))
	observations, err := dep.ReconcileEdges(t.Context(), f, s, []dep.ObservationRequest{r, second})
	if err != nil || len(observations) != 2 {
		t.Fatal(err)
	}
	if observations[0].Evidence().Observation.Result != "unavailable" || observations[1].Evidence().Observation.Result != "waiting" {
		t.Fatal("partial read reused success or starved unrelated edge", observations)
	}
	s = applyObservation(t, s, 5, observations[0])
	s = applyObservation(t, s, 6, observations[1])
	// A successful read started before a newer failure must not erase the failure.
	r.Prior = 5
	r.Sequence = 2
	delayed, err := dep.ObserveEdge(t.Context(), f, r)
	if err != nil {
		t.Fatal(err)
	}
	f.FailNext("WriteGrant", errors.New("revoked permission"))
	r.Sequence = 3
	unavailable, err := dep.ObserveEdge(t.Context(), f, r)
	if err != nil {
		t.Fatal(err)
	}
	s = applyObservation(t, s, 7, unavailable)
	if _, err := dep.Apply(s, 8, delayed.Evidence().Observation); !errors.Is(err, dep.ErrStale) {
		t.Fatal("delayed read erased revocation", err)
	}
	// Reconciliation requires a complete exact set, independently of forge reads.
	r.Prior = 7
	if _, err := dep.ReconcileEdges(t.Context(), f, s, []dep.ObservationRequest{r}); !errors.Is(err, dep.ErrStale) {
		t.Fatal("partial graph accepted", err)
	}
	if _, err := dep.ReconcileEdges(t.Context(), f, s, []dep.ObservationRequest{r, r}); !errors.Is(err, dep.ErrStale) {
		t.Fatal("duplicate graph accepted", err)
	}
}

func TestReverseWakesSortedConsumersAndRemovedEdges(t *testing.T) {
	s, init, _, r := reconcileFixture(t)
	d := r.Consumer
	d.Alias.Number = 3
	d.ChangeID = "chg1_" + strings.Repeat("1", 64)
	var err error
	s, err = dep.Apply(s, 4, d)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dep.RebuildReverse(s, init)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := index.Wake(r.Source.Reference, 4)
	if err != nil || !reflect.DeepEqual(wake, []string{d.ChangeID, r.Consumer.ChangeID}) {
		t.Fatal(wake, err)
	}
	d.Prior = s.Prior(d.Alias)
	d.Poll++
	d.Edges = []dep.DeclaredEdge{}
	d.Digest = dep.DeclarationDigest(d.Edges)
	d.Hold = dep.DeclarationHold{Held: true, Editors: []string{"11"}}
	s, err = dep.Apply(s, 5, d)
	if err != nil {
		t.Fatal(err)
	}
	index, err = dep.RebuildReverse(s, init)
	if err != nil {
		t.Fatal(err)
	}
	wake, err = index.Wake(r.Source.Reference, 5)
	if err != nil || !reflect.DeepEqual(wake, []string{r.Consumer.ChangeID}) {
		t.Fatal("removed edge still wakes consumer", wake, err)
	}
	if len(dep.PollDeclarations(s)) != 1 {
		t.Fatal("empty declaration triggered graph discovery")
	}
}
