package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestCheckAttemptRunTupleAndEventTie(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	observation := func(runs ...queue.CheckRun) queue.ChecksObserved {
		return queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: runs}
	}
	run := func(id, attempt uint64, state string) queue.CheckRun {
		return queue.CheckRun{ID: id, Attempt: attempt, Name: "unit", Source: "app:1", State: state}
	}
	// The greatest attempt of an older run must not promote its event above
	// another event with the same newest run and a greater attempt of THAT run.
	evidenceInsert(t, f, observation(run(71, 2, "failure")))
	malformed, err := json.Marshal(map[string]any{"version": 1, "type": "checks_observed", "data": observation(run(70, 99, "success"), run(71, 1, "success"))})
	must(t, err)
	_, err = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'checks_observed',$1,'2026-10-22T12:00:00Z')`, malformed)
	must(t, err)
	// Equal run/attempt tuples are broken by event ID (latest retained read).
	selected := evidenceInsert(t, f, observation(run(71, 2, "success")))
	boundary := insert(t, f.admin, 1, "2026-10-22T12:00:01Z")
	id, attempt := uint64(71), uint64(2)
	c := queue.LandingChecksRecorded{BatchID: "batch", TestedSHA: "tested", Checks: []queue.LandingCheck{{Name: "unit", Source: "app:1", RunID: &id, Attempt: &attempt, Result: forge.CheckState("success")}}}
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: boundary}}, owner: EvidenceOwner{QueueID: 1, EventID: boundary}, refs: []Reference{}}
		if e := r.checkHistory(c); e != nil {
			return e
		}
		if r.refs[0].Source == nil || r.refs[0].Source.ID != selected || r.refs[0].RunID != 71 || r.refs[0].Attempt != 2 {
			t.Fatal("inconsistent run/event ordering", r.refs)
		}
		return nil
	}))
}

func TestEvidenceIdentityComponentBoundaries(t *testing.T) {
	// Both pairs formerly joined to a/b/c; the last two collided with /later.
	identities := []Identity{evidenceIdentity("a/b", "c"), evidenceIdentity("a", "b/c"), evidenceIdentity("a", "b", "later"), evidenceIdentity("a", "b/later")}
	seen := map[Identity]bool{}
	for _, id := range identities {
		if seen[id] {
			t.Fatal("identity collision", id)
		}
		seen[id] = true
	}
}

func TestCheckAttemptNewestRunWithinEvent(t *testing.T) {
	runs := []queue.CheckRun{{ID: 71, Attempt: 2, Name: "unit", Source: "app:1"}, {ID: 70, Attempt: 99, Name: "unit", Source: "app:1"}, {ID: 71, Attempt: 1, Name: "unit", Source: "app:1"}}
	got := newestCheckRun(runs, "unit", "app:1")
	if got == nil || got.ID != 71 || got.Attempt != 2 {
		t.Fatal(got)
	}
	// The shared event codec forbids multiple runs for the same name/source.
	// Keep that wire contract intact and reject malformed retained payloads.
	if _, _, err := queue.EncodeEvent(queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: runs}); err == nil {
		t.Fatal("duplicate run identity admitted")
	}
}

// An event holding several runs of one check is malformed for the codec, but
// the SQL still orders it by its own newest run tuple. When that tuple governs
// the boundary, the malformed observation is selected and rejected; a pick of
// its older run would instead silently select the valid earlier event.
func TestCheckAttemptMultiRunEventOrderedByItsNewestRun(t *testing.T) {
	run := func(id, attempt uint64) queue.CheckRun {
		return queue.CheckRun{ID: id, Attempt: attempt, Name: "unit", Source: "app:1", State: "success"}
	}
	for name, runs := range map[string][]queue.CheckRun{
		"newer-run-id":   {run(70, 1), run(72, 1)},
		"higher-attempt": {run(71, 1), run(71, 3)},
	} {
		t.Run(name, func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			valid := evidenceInsert(t, f, queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: []queue.CheckRun{run(71, 2)}})
			malformed, err := json.Marshal(map[string]any{"version": 1, "type": "checks_observed", "data": queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: runs}})
			must(t, err)
			_, err = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'checks_observed',$1,'2026-10-22T12:00:00Z')`, malformed)
			must(t, err)
			boundary := insert(t, f.admin, 1, "2026-10-22T12:00:01Z")
			id, attempt := uint64(71), uint64(2)
			c := queue.LandingChecksRecorded{BatchID: "batch", TestedSHA: "tested", Checks: []queue.LandingCheck{{Name: "unit", Source: "app:1", RunID: &id, Attempt: &attempt, Result: forge.CheckState("success")}}}
			var resolveErr error
			var refs []Reference
			must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
				r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: boundary}}, owner: EvidenceOwner{QueueID: 1, EventID: boundary}, refs: []Reference{}}
				resolveErr = r.checkHistory(c)
				refs = r.refs
				return nil
			}))
			if resolveErr != errSource {
				t.Fatalf("got %v; selected %+v instead of rejecting the governing malformed observation (valid=%d)", resolveErr, refs, valid)
			}
		})
	}
}
