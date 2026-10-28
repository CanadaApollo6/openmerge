package dependency_test

import (
	"errors"
	"strings"
	"testing"

	dep "github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestReviewPushPreservesWaitAndNeverRepins(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "revision-less"
		if pinned {
			name = "explicit-pin"
		}
		t.Run(name, func(t *testing.T) {
			s, init, f, r := reconcileFixture(t)
			if pinned {
				ref := r.Source.Reference
				ref.Revision = r.Source.Current.Revision
				r.Edge.Reference, _ = ref.Canonical()
				r.Edge.PinnedHead = r.Source.Current.Head
				r.Consumer.Edges = []dep.DeclaredEdge{r.Edge}
				r.Consumer.Digest = dep.DeclarationDigest(r.Consumer.Edges)
				r.Declaration.Digest = r.Consumer.Digest
				var err error
				s, err = dep.Apply(dep.State{}, 1, init)
				if err != nil {
					t.Fatal(err)
				}
				s, err = dep.Apply(s, 3, r.Consumer)
				if err != nil {
					t.Fatal(err)
				}
			}
			declaration := s.Prior(r.Consumer.Alias)
			read := func(id uint64, want string) {
				t.Helper()
				f.SetDependencySource(forge.RepoRef{Path: "acme/project"}, dependencytest.Source(r))
				out, err := dep.ReconcileEdges(t.Context(), f, s, []dep.ObservationRequest{r})
				if err != nil || len(out) != 1 || out[0].Evidence().Code != want {
					t.Fatalf("read: %v %v want %s", out, err, want)
				}
				s = applyObservation(t, s, id, out[0])
				r.Prior = id
				r.Sequence++
				if got := s.Prior(r.Consumer.Alias); got != declaration {
					t.Fatal("review push edited declaration", got)
				}
			}
			read(4, "dependency-waiting")
			delayed := r
			r.Source.Current.Revision++
			r.Source.Current.Head = strings.Repeat("e", 40)
			r.Source.Current.IntentDigest = strings.Repeat("f", 64)
			want := "dependency-waiting"
			if pinned {
				want = "dependency-stale"
			}
			read(5, want)
			raw, err := dep.EncodeSnapshot(queue.State{}, s)
			if err != nil {
				t.Fatal(err)
			}
			_, s, err = dep.DecodeSnapshot(raw, init)
			if err != nil {
				t.Fatal(err)
			}
			// A success acquired before the push cannot replace its newer wait/stale.
			delayed.Source.Landing = &dep.SourceLanding{Revision: delayed.Source.Current, Commit: strings.Repeat("d", 40), SettlementEventID: 2, BaseSHA: delayed.Source.BaseSHA, OnBase: true}
			f.SetDependencySource(forge.RepoRef{Path: "acme/project"}, dependencytest.Source(delayed))
			old, err := dep.ObserveEdge(t.Context(), f, delayed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = dep.Apply(s, 6, old.Evidence().Observation); !errors.Is(err, dep.ErrStale) {
				t.Fatal("delayed success replaced new push", err)
			}
			r.Source.Landing = &dep.SourceLanding{Revision: r.Source.Current, Commit: strings.Repeat("d", 40), SettlementEventID: 2, BaseSHA: r.Source.BaseSHA, OnBase: true}
			want = "dependency-landed"
			if pinned {
				want = "dependency-stale"
			}
			read(7, want)
			o, _, ok := s.Observation(dep.Observed{Alias: r.Consumer.Alias, ChangeID: r.Consumer.ChangeID, Revision: r.Consumer.Revision, Source: r.Edge.Reference})
			if !ok || o.SourceRevision.Revision != 8 || o.SourceRevision.Head != r.Source.Current.Head {
				t.Fatal("lost actual observed revision", o)
			}
			// A confirmed matching old landing remains terminal after later pushes.
			r.Source.Landing.Revision = delayed.Source.Current
			read(8, "dependency-landed")
		})
	}
}
