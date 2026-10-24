package audit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// A single unrelated retained event claimed as a dependency is outside the
// closure of every scope root, even though it is real, in-watermark and
// correctly hashed. The same artifact without it replays.
func TestReplayRejectsUnreferencedExtraDependency(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	rootID := insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	extraID := insert(t, f.admin, 1, "2026-10-22T14:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	exact := buildReplayArtifact(t, f, original, []sourceEvent{replayEvent(t, f, rootID)}, []string{"scope"})
	if _, err = f.reader.Replay(t.Context(), filepath.Join(exact, "manifest.json"), filepath.Join(t.TempDir(), "exact")); err != nil {
		t.Fatalf("exact closure rejected: %v", err)
	}
	extra := buildReplayArtifact(t, f, original,
		[]sourceEvent{replayEvent(t, f, rootID), replayEvent(t, f, extraID)}, []string{"scope", "dependency"})
	if _, err = f.reader.Replay(t.Context(), filepath.Join(extra, "manifest.json"), filepath.Join(t.TempDir(), "extra")); err != errIntegrity {
		t.Fatalf("got %v want integrity rejection of an unreferenced dependency", err)
	}
}

func TestEvidenceIdentityIsCanonicalStringTuple(t *testing.T) {
	// Arity and component boundaries are both part of the identity.
	distinct := []Identity{evidenceIdentity("b"), evidenceIdentity("b", "a"), evidenceIdentity("b/a"), evidenceIdentity(`b","a`), evidenceIdentity("")}
	seen := map[Identity]bool{}
	for _, id := range distinct {
		if seen[id] || !validIdentity(id) {
			t.Fatalf("identity %q collides or is invalid", id)
		}
		seen[id] = true
	}
	if got := evidenceIdentity("a&<b>"); got != `["a&<b>"]` {
		t.Fatalf("identity escaped payload text: %s", got)
	}
	for _, raw := range []string{"", "batch", "b/a", "[]", "null", `"b"`, `[1]`, `["b",1]`, `[ "b"]`, `["b"] `, "[\"\\u0062\"]", `{"b":"a"}`} {
		if validIdentity(Identity(raw)) {
			t.Fatalf("accepted non-canonical identity %q", raw)
		}
	}
	ref := Reference{Kind: "settlement", Relation: "relied_on", Status: "not_recorded", Owner: EvidenceOwner{QueueID: 1, EventID: 8}, ExpectedType: "landed", Identity: evidenceIdentity("batch")}
	if !validReferences([]Reference{ref}) {
		t.Fatal("canonical tuple identity rejected")
	}
	ref.Identity = "batch"
	if validReferences([]Reference{ref}) {
		t.Fatal("legacy bare identity accepted")
	}
}

// Identities copy payload strings. An identity whose reference exceeds the
// canonical bound is a sanitized error, not a panic.
func TestNormalizeReferencesRejectsExcessiveIdentity(t *testing.T) {
	ref := Reference{Kind: "check", Relation: "relied_on", Status: "not_recorded", Owner: EvidenceOwner{QueueID: 1, EventID: 8}, ExpectedType: "checks_observed", Identity: evidenceIdentity(strings.Repeat("\"", MaxBytes/2))}
	refs, err := normalizeReferences([]Reference{ref})
	if err != errCanonical || refs != nil {
		t.Fatalf("got %v, %v want canonical rejection", refs, err)
	}
	if validReferences([]Reference{ref}) {
		t.Fatal("unencodable reference validated")
	}
}

// Dispatch provenance follows the selected request generation. A newer retry
// of another check in the same batch dispatches a later generation; neither
// its progress nor its result may be attributed to this check.
func TestCheckAttemptDispatchFollowsRequestGeneration(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	run := func(name string, workflow uint64, attempt uint64, state string) queue.CheckRun {
		return queue.CheckRun{ID: 71, Attempt: attempt, WorkflowRunID: workflow, Name: name, Source: "app:1", State: state}
	}
	request := func(generation uint64, r queue.CheckRun) queue.CheckRerunRequested {
		return queue.CheckRerunRequested{BatchID: "batch", TestedSHA: "tested", Generation: generation, Runs: []queue.CheckRun{r}, OriginalDeadline: at.Add(time.Hour), At: at, Deadline: at.Add(time.Hour), Timeout: time.Hour}
	}
	requested := evidenceInsert(t, f, request(7, run("unit", 81, 1, "failure")))
	evidenceInsert(t, f, queue.CheckRerunDispatchProgress{BatchID: "batch", TestedSHA: "tested", Generation: 7, Target: queue.CheckRerunTarget{WorkflowRunID: 81, Attempt: 1, Accepted: true, At: at}})
	evidenceInsert(t, f, queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "tested", Generation: 7})
	observed := evidenceInsert(t, f, queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: []queue.CheckRun{run("unit", 81, 2, "success")}, Terminal: true, Success: true, OriginalDeadline: at.Add(time.Hour), Timeout: time.Hour})
	evidenceInsert(t, f, request(8, queue.CheckRun{ID: 90, Attempt: 1, WorkflowRunID: 99, Name: "other", Source: "app:1", State: "failure"}))
	evidenceInsert(t, f, queue.CheckRerunDispatchProgress{BatchID: "batch", TestedSHA: "tested", Generation: 8, Target: queue.CheckRerunTarget{WorkflowRunID: 99, Attempt: 1, Accepted: true, At: at}})
	evidenceInsert(t, f, queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "tested", Generation: 8, Unsupported: true})
	// Equal generation/workflow candidates use latest event ID before the owner.
	progress := evidenceInsert(t, f, queue.CheckRerunDispatchProgress{BatchID: "batch", TestedSHA: "tested", Generation: 7, Target: queue.CheckRerunTarget{WorkflowRunID: 81, Attempt: 1, Accepted: true, At: at}})
	dispatched := evidenceInsert(t, f, queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "tested", Generation: 7})
	boundary := insert(t, f.admin, 1, "2026-10-22T12:00:01Z")
	// Still newer matching events after the owner must not replace either edge.
	evidenceInsert(t, f, queue.CheckRerunDispatchProgress{BatchID: "batch", TestedSHA: "tested", Generation: 7, Target: queue.CheckRerunTarget{WorkflowRunID: 81, Attempt: 1, Accepted: true, At: at}})
	evidenceInsert(t, f, queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "tested", Generation: 7})
	id, attempt, workflow := uint64(71), uint64(2), uint64(81)
	c := queue.LandingChecksRecorded{BatchID: "batch", AttemptID: "land", TestedSHA: "tested", Checks: []queue.LandingCheck{{Name: "unit", Source: "app:1", RunID: &id, Attempt: &attempt, WorkflowRunID: &workflow, Result: forge.CheckState("success")}}}
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: boundary}}, owner: EvidenceOwner{QueueID: 1, EventID: boundary}, refs: []Reference{}}
		if e := r.checkHistory(c); e != nil {
			return e
		}
		want := map[string]int64{"checks_observed": observed, "check_rerun_requested": requested, "check_rerun_dispatch_progress": progress, "check_rerun_dispatched": dispatched}
		if len(r.refs) != 5 {
			t.Fatal("check history reference count", r.refs)
		}
		for _, ref := range r.refs[:4] {
			if ref.Source == nil || ref.Source.ID != want[ref.ExpectedType] {
				t.Fatalf("%s attributed to the wrong retry: %+v", ref.ExpectedType, ref)
			}
			if ref.ExpectedType != "checks_observed" && ref.Generation != 7 {
				t.Fatalf("%s generation %d want 7", ref.ExpectedType, ref.Generation)
			}
		}
		return nil
	}))
}

// Range, not closure, owns the scope label: an out-of-range retained record
// relabelled as a scope root is self-consistent under exact closure.
func TestReplayRejectsOutOfRangeRecordClaimedAsScope(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	rootID := insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	laterID := insert(t, f.admin, 1, "2026-10-22T14:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	artifact := buildReplayArtifact(t, f, original,
		[]sourceEvent{replayEvent(t, f, rootID), replayEvent(t, f, laterID)}, []string{"scope", "scope"})
	if _, err = f.reader.Replay(t.Context(), filepath.Join(artifact, "manifest.json"), filepath.Join(t.TempDir(), "replay")); err != errIntegrity {
		t.Fatalf("got %v want integrity rejection of an out-of-range scope claim", err)
	}
}

// Several member decisions share one attempt. Each event resolves against the
// retained item with its own event ID, never another member's decision.
func TestRound3DecisionOwnerSelectsItsOwnRetainedItem(t *testing.T) {
	fx := round3ApprovalFixture(t)
	remedy := func(evidence string) queue.LandingDecisionRecorded {
		d := fx.decision
		d.Variant, d.Approval = "generated_remedy", nil
		d.Remedy = &queue.GeneratedRemedyDecision{ChangeID: fx.decision.Approval.Basis.ChangeID, Revision: fx.decision.Approval.Basis.Revision, Head: fx.decision.Approval.Basis.Head, Suspect: fx.decision.TestedSHA, Evidence: []string{evidence}, OnRed: "revert"}
		return d
	}
	first, second := remedy("first"), remedy("second")
	firstID := evidenceInsert(t, fx.f, first)
	evidenceInsert(t, fx.f, second)
	resolve := func(id int64, d queue.LandingDecisionRecorded) error {
		_, raw, err := queue.EncodeEvent(d)
		must(t, err)
		var resolveErr error
		must(t, fx.f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			var watermark int64
			must(t, tx.QueryRow(ctx, `SELECT max(id) FROM events WHERE queue_id=1`).Scan(&watermark))
			_, _, resolveErr = resolveEvidenceAt(ctx, tx, sourceEvent{source: Source{QueueID: 1, ID: id, Type: "landing_decision_recorded", Queue: Key{Installation: "7", Repository: "Owner/Repo", Base: "release"}}, payload: raw}, watermark)
			return nil
		}))
		return resolveErr
	}
	if err := resolve(firstID, first); err != nil {
		t.Fatalf("member decision rejected beside a later member: %v", err)
	}
	if err := resolve(fx.decisionID, fx.decision); err != nil {
		t.Fatalf("approval decision rejected beside remedy members: %v", err)
	}
	if err := resolve(firstID, second); err == nil {
		t.Fatal("accepted the later member's payload under the first member's event ID")
	}
}

// An openmerge pin governs an approval only for the governing commit its
// classification was made under; a codec-valid pin for another commit fails.
func TestRound3OpenMergePinMustMatchClassificationGoverningCommit(t *testing.T) {
	fx := round3ApprovalFixture(t)
	if _, _, err := round3Resolve(t, fx, fx.decisionID, ""); err != nil {
		t.Fatalf("matching governing commit rejected: %v", err)
	}
	pin := fx.events["policy_authority_applied"][0].(queue.PolicyAuthorityApplied)
	pin.GoverningCommit = strings.Repeat("e", len(pin.GoverningCommit))
	_, raw, err := queue.EncodeEvent(pin)
	must(t, err)
	_, err = fx.f.admin.Exec(t.Context(), `UPDATE events SET payload=$2 WHERE id=$1`, fx.pinID, raw)
	must(t, err)
	if _, _, err := round3Resolve(t, fx, fx.decisionID, ""); err != errSource {
		t.Fatalf("got %v want rejection of a pin for another governing commit", err)
	}
}
