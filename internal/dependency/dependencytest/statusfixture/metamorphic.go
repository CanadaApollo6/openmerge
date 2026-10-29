package statusfixture

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// MetamorphicJournal pairs private histories with the same public observation.
// Seed chooses the public transition; world chooses private progress and batch history.
func MetamorphicJournal(t testing.TB, seed int64, depth, world int) (store.Journal, dependency.JournalInitialized, []int) {
	t.Helper()
	variants := []struct{ code, phase string }{
		{"dependency-waiting", "queued"}, {"dependency-closed", "queued"},
		{"dependency-stale", "queued"}, {"dependency-unavailable", "queued"},
		{"dependency-landed", "testing"}, {"dependency-waiting", "invalidating"},
		{"dependency-waiting", "discarded"},
	}
	v := variants[world%len(variants)]
	j, binding, _, grant, prs := MultiJournal(t, v.code, v.phase, depth, true)
	appendEvent := func(e queue.Event) {
		t.Helper()
		_, raw, err := queue.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), RecordedAt: j.Events[0].RecordedAt, Event: e, Payload: raw})
	}
	// Append another private observation before or after the public transition.
	// The last observation is the causal predecessor even after invalidation.
	privateTransition := func() {
		t.Helper()
		var last dependency.Observed
		var prior uint64
		for _, record := range j.Events {
			if record.Event != nil {
				continue
			}
			e, err := dependency.DecodeEvent(record.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if observed, ok := e.(dependency.Observed); ok {
				last, prior = observed, uint64(record.ID)
			}
		}
		last.ExpectedPrior, last.ReadSequence = prior, last.ReadSequence+1
		states := []struct{ result, code string }{
			{"waiting", "dependency-waiting"}, {"unavailable", "dependency-closed"},
			{"stale", "dependency-stale"}, {"landed", "dependency-landed"},
			{"unavailable", "dependency-unavailable"},
		}
		next := states[(world+int(seed))%len(states)]
		last.Result = next.result
		appendPrivate := func(event dependency.Event) uint64 {
			t.Helper()
			_, raw, err := dependency.EncodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			id := uint64(len(j.Events) + 1)
			j.Events = append(j.Events, store.JournalEvent{ID: int64(id), RecordedAt: j.Events[0].RecordedAt, Payload: raw})
			return id
		}
		id := appendPrivate(last)
		appendPrivate(dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: id, Source: dependencytest.Request().Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: last.Declaration, Grant: grant, Observation: last, Code: next.code}})
	}
	if (world+int(seed))%2 == 0 {
		privateTransition()
	}
	// Provenance is only observable when a batch exists; the public transition
	// occurs in both worlds after that observation.
	if v.phase == "testing" && seed%2 == 0 {
		appendEvent(queue.CheckProvenanceObserved{BatchID: "b1", TestedSHA: fmt.Sprintf("%040x", 100), Checks: []queue.UnboundCheck{{Name: "ci", Source: "github-actions", HeadBranch: "other"}}})
	}
	rng := rand.New(rand.NewSource(seed))
	switch seed % 5 {
	case 0:
		appendEvent(queue.PRObserved{PR: 3, HeadSHA: fmt.Sprintf("%040x", rng.Int63()), Eligible: true, Trusted: true})
	case 1:
		appendEvent(queue.PRClosed{PR: 3})
	case 2:
		appendEvent(queue.DequeueRequested{PR: 3})
	case 3:
		appendEvent(queue.PRObserved{PR: 3, HeadSHA: fmt.Sprintf("%040x", 13), Eligible: false, Trusted: true})
	case 4:
		appendEvent(queue.PauseRequested{Reason: "operator", Detail: "public maintenance"})
	}
	if (world+int(seed))%2 != 0 {
		privateTransition()
	}
	return j, binding, prs
}
