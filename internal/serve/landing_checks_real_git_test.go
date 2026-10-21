package serve

import (
	"os"
	"reflect"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Persist the actual cold-cache test object's evidence before the real git push,
// then read it back after confirming and recording the remote base settlement.
// Authority without a review floor is an internal store seam, activated by PR 9.
func recordRealGitCheckStart(t *testing.T, key store.QueueKey, land queue.Land, e queue.LandingChecksRecorded, fence string) func(string, string) {
	t.Helper()
	db := database(t)
	q, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	must(t, db.InitializeFence(t.Context(), q, fence))
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	pin.GoverningCommit = e.ConfigCommit
	pin.EffectiveDigest = e.ConfigDigest
	pin.OpenMerge.Binding.EffectiveDigest = e.ConfigDigest
	events := []queue.Event{queue.ConfigApplied{Commit: e.ConfigCommit, Digest: e.ConfigDigest}, pin, queue.BaseMoved{SHA: land.ExpectedBaseSHA}}
	for _, p := range land.PRs {
		events = append(events, queue.PRObserved{PR: p.PR, HeadSHA: p.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: p.PR, At: e.ObservedAt.Add(-2 * time.Second)})
	}
	events = append(events, queue.BatchStarted{BatchID: land.BatchID, BaseSHA: land.ExpectedBaseSHA, PRs: land.PRs, At: e.ObservedAt.Add(-2 * time.Second)}, queue.TestRefBuilt{BatchID: land.BatchID, TestedSHA: land.TestedSHA, Method: land.Method, At: e.ObservedAt.Add(-time.Second)}, queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true})
	_, err = db.Append(t.Context(), q, events, "")
	must(t, err)
	cfg := queue.Config{ConfigDigest: e.ConfigDigest, MergeMethod: land.Method, BatchMaxSize: len(land.PRs), CheckTimeout: time.Hour}
	planned, err := db.Plan(t.Context(), q, cfg, time.Now())
	must(t, err)
	skip := []string{}
	for _, p := range planned {
		if _, ok := p.Action.(queue.Land); !ok {
			skip = append(skip, p.Key)
		}
	}
	claims, err := db.ClaimQueue(t.Context(), q, 1, time.Minute, time.Now(), skip...)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("real-git landing claim missing")
	}
	claim := claims[0]
	admitted, ok := claim.Action.(queue.Land)
	if !ok {
		t.Fatal("wrong real-git start action")
	}
	proceed, err := db.BeginConsequential(t.Context(), claim, cfg, time.Now())
	must(t, err)
	if !proceed {
		t.Fatal("real-git start did not begin")
	}
	state, _, err := db.Load(t.Context(), q)
	must(t, err)
	e.RequiredCheckSource = state.LandingCheckSources()
	start := queue.LandingStarted{BatchID: land.BatchID, AttemptID: e.AttemptID, FenceToken: fence, At: time.Now()}
	must(t, db.StartLanding(t.Context(), claim, cfg, cp.Policy{Digest: e.PolicyDigest}, nil, nil, start, e))
	return func(landed, newFence string) {
		t.Helper()
		_, err := db.Append(t.Context(), q, []queue.Event{queue.Landed{BatchID: admitted.BatchID, NewBaseSHA: landed, NewFence: newFence}}, "")
		must(t, err)
		records, err := db.EventsAfter(t.Context(), q, 0)
		must(t, err)
		n := len(records)
		got, ok := records[n-3].Event.(queue.LandingChecksRecorded)
		if !ok || !reflect.DeepEqual(got, e) {
			t.Fatal("real-git settlement lost exact checks")
		}
		if _, ok := records[n-2].Event.(queue.LandingStarted); !ok {
			t.Fatal("real-git checks/start not atomic neighbors")
		}
		if _, ok := records[n-1].Event.(queue.Landed); !ok {
			t.Fatal("real-git settlement missing")
		}
		t.Logf("checks=%d start=%d settlement=%d tested=%s landed=%s run=%d attempt=%d source=%s result=%s config=%s digest=%s", records[n-3].ID, records[n-2].ID, records[n-1].ID, e.TestedSHA, landed, *e.Checks[0].RunID, *e.Checks[0].Attempt, e.Checks[0].Source, e.Checks[0].Result, e.ConfigCommit, e.ConfigDigest)
	}
}
