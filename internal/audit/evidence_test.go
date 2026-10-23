package audit

import (
	"context"
	"encoding/json"
	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestEvidenceReferenceComparatorAndAbsence(t *testing.T) {
	owner := EvidenceOwner{QueueID: 1, EventID: 12, BatchID: "batch", AttemptID: "attempt", ChangeID: "change", Revision: 2, Head: "head", TestedSHA: "tested"}
	a := Reference{Kind: "eligibility", Status: "not_recorded", Owner: owner, ExpectedType: "approval_eligibility_observed", Identity: "read"}
	b := a
	b.Identity = "other"
	refs := normalizeReferences([]Reference{a, b, a})
	if len(refs) != 2 || refs[0].Source != nil || !reflect.DeepEqual(refs, normalizeReferences([]Reference{b, a})) {
		t.Fatal("total order/dedup or explicit absence changed")
	}
	if got := evidenceKinds("unknown_historical"); len(got) != 0 {
		t.Fatal(got)
	}
	for _, typ := range []string{"approval_eligibility_observed", "change_classified", "change_accountability_observed", "approval_receipt_recorded", "policy_authority_applied", "landing_checks_recorded", "landed"} {
		if len(evidenceKinds(typ)) != 1 {
			t.Fatal(typ)
		}
	}
}

func evidenceInsert(t *testing.T, f *fixture, e queue.Event) int64 {
	t.Helper()
	typ, raw, err := queue.EncodeEvent(e)
	must(t, err)
	var id int64
	must(t, f.admin.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,$1,$2,'2026-10-22T12:00:00Z') RETURNING id`, typ, raw).Scan(&id))
	return id
}

func TestEvidenceNewestCheckAttemptAtBoundary(t *testing.T) {
	for _, states := range [][2]string{{"success", "failure"}, {"failure", "success"}} {
		t.Run(states[0]+"_to_"+states[1], func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			observed := func(attempt uint64, state string) queue.ChecksObserved {
				return queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: []queue.CheckRun{{ID: 71, Attempt: attempt, WorkflowRunID: 81, Name: "unit", Source: "app:1", State: state}}, Terminal: true, Success: state == "success", OriginalDeadline: time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC), Timeout: time.Hour}
			}
			evidenceInsert(t, f, observed(1, states[1]))
			at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			request := queue.CheckRerunRequested{BatchID: "batch", TestedSHA: "tested", Generation: 7, Runs: observed(0, "failure").Runs, OriginalDeadline: at.Add(time.Hour), At: at, Deadline: at.Add(time.Hour), Timeout: time.Hour}
			request.Runs[0].Attempt = 1
			evidenceInsert(t, f, request)
			evidenceInsert(t, f, queue.CheckRerunDispatchProgress{BatchID: "batch", TestedSHA: "tested", Generation: 7, Target: queue.CheckRerunTarget{WorkflowRunID: 81, Attempt: 1, Accepted: true, At: at}})
			evidenceInsert(t, f, queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "tested", Generation: 7})
			// Another producer's newer retry cannot supply this run's generation.
			request.Generation = 8
			request.Runs[0].Name = "other"
			request.Runs[0].WorkflowRunID = 99
			evidenceInsert(t, f, request)
			first := evidenceInsert(t, f, observed(2, states[0]))
			_ = first
			oldRun := observed(99, states[1])
			oldRun.Runs[0].ID = 70
			evidenceInsert(t, f, oldRun)
			boundary := insert(t, f.admin, 1, "2026-10-22T12:00:01Z")
			later := evidenceInsert(t, f, observed(3, states[1]))
			run, attempt, workflow := uint64(71), uint64(2), uint64(81)
			c := queue.LandingChecksRecorded{BatchID: "batch", AttemptID: "land", TestedSHA: "tested", Checks: []queue.LandingCheck{{Name: "unit", Source: "app:1", RunID: &run, Attempt: &attempt, WorkflowRunID: &workflow, Result: forge.CheckState(states[0])}}}
			must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
				r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: boundary, Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main"}}}, owner: EvidenceOwner{QueueID: 1, EventID: boundary}, refs: []Reference{}}
				if err := r.checkHistory(c); err != nil {
					return err
				}
				if len(r.refs) != 5 || r.refs[0].Source == nil || r.refs[0].Source.ID != first || r.refs[0].Attempt != 2 || r.refs[4].Source == nil || r.refs[4].Source.ID != later || r.refs[4].Relation != "excluded_later" {
					t.Fatal(r.refs)
				}
				for _, ref := range r.refs[1:4] {
					if ref.Status != "recorded" || ref.Generation != 7 {
						t.Fatal("retry provenance crossed producer/generation", ref)
					}
				}
				c.Checks[0].Result = forge.CheckState(states[1])
				if r.checkHistory(c) == nil {
					t.Fatal("accepted result from later attempt")
				}
				c.Checks[0].Result = forge.CheckState(states[0])
				attempt = 3
				if r.checkHistory(c) == nil {
					t.Fatal("accepted mismatched attempt")
				}
				attempt = 2
				run = 99
				if r.checkHistory(c) == nil {
					t.Fatal("accepted mismatched run")
				}
				run = 71
				workflow = 99
				if r.checkHistory(c) == nil {
					t.Fatal("accepted mismatched workflow")
				}
				return nil
			}))
		})
	}
}

func TestEvidenceApprovalFamiliesAndRequiredMissing(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	_, err := f.admin.Exec(t.Context(), `UPDATE queues SET installation='7',repository='Owner/Repo',base='release' WHERE id=1`)
	must(t, err)
	var input struct {
		Events []json.RawMessage
		Policy cp.Policy
		Fresh  queue.ApprovalEligibilityObserved
		Land   queue.Land
		Start  queue.LandingStarted
	}
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	must(t, json.Unmarshal(raw, &input))
	var state queue.State
	for _, raw := range input.Events {
		v, err := queue.DecodeEvent(raw)
		must(t, err)
		if receipt, ok := v.(queue.ApprovalReceiptRecorded); ok {
			fact, err := state.RecordedBrief(input.Policy, receipt.Basis)
			must(t, err)
			identity, err := brief.Identity(fact)
			must(t, err)
			v, err = state.PrepareBareReceipt(input.Policy, receipt.Basis, receipt.Actor, receipt.CommentID, receipt.CreatedAt, identity.BriefDigest)
			must(t, err)
		}
		evidenceInsert(t, f, v)
		state = queue.Apply(state, v)
	}
	decision, err := state.PrepareLandingDecision(input.Policy, input.Fresh, input.Start, input.Land)
	must(t, err)
	classified, ok := state.Classification(input.Fresh.Basis.ChangeID, input.Fresh.Basis.Revision)
	if !ok {
		t.Fatal("classification fixture missing")
	}
	raw, err = os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	v, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := v.(queue.PolicyAuthorityApplied)
	pin.GoverningCommit = classified.Evidence.Classification.Inputs.GoverningCommit
	evidenceInsert(t, f, pin)
	eligibilityID := evidenceInsert(t, f, input.Fresh)
	decisionID := evidenceInsert(t, f, decision)
	checks := queue.LandingChecksRecorded{BatchID: input.Start.BatchID, AttemptID: input.Start.AttemptID, ConfigCommit: "config", ConfigDigest: strings.Repeat("a", 64), PolicyDigest: input.Policy.Digest, TestedSHA: input.Land.TestedSHA, LandedSHA: input.Land.TestedSHA, Method: input.Land.Method, TestRef: input.Land.Ref, RequiredCheckSource: state.LandingCheckSources(), ObservedAt: input.Start.At, Checks: []queue.LandingCheck{}, CheckOrder: []int{}, Expressions: []string{condition.All().String()}, Inputs: []condition.ObservedInput{}, Members: []queue.LandingCheckMember{{PR: input.Land.PRs[0].PR, HeadSHA: input.Land.PRs[0].HeadSHA, Conditions: queue.LandingCheckCondition{Expression: 0, Inputs: []int{}}}}}
	evidenceInsert(t, f, checks)
	evidenceInsert(t, f, input.Start)
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		must(t, tx.QueryRow(ctx, `SELECT payload FROM events WHERE id=$1`, decisionID).Scan(&raw))
		refs, sources, err := resolveEvidence(ctx, tx, sourceEvent{source: Source{QueueID: 1, ID: decisionID, Type: "landing_decision_recorded", Queue: Key{Installation: "7", Repository: "Owner/Repo", Base: "release"}}, payload: raw})
		if err != nil {
			return err
		}
		if !validReferences(refs) || len(sources) < 6 {
			t.Fatal(refs)
		}
		kinds := map[string]bool{}
		for _, ref := range refs {
			kinds[ref.Kind] = true
		}
		for _, kind := range []string{"eligibility", "classification", "accountability", "receipt", "authority", "check", "settlement"} {
			if !kinds[kind] {
				t.Fatal("missing family", kind)
			}
		}
		return nil
	}))
	_, err = f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T13:00:00Z' WHERE id=$1`, decisionID)
	must(t, err)
	target := filepath.Join(t.TempDir(), "approval")
	_, err = f.reader.Export(t.Context(), Request{Installation: "7", Repository: "Owner/Repo", From: "2026-10-22T13:00:00Z", Until: "2026-10-22T13:00:01Z"}, target)
	must(t, err)
	_, err = f.reader.Replay(t.Context(), filepath.Join(target, "manifest.json"), filepath.Join(t.TempDir(), "approval-replay"))
	must(t, err)
	_, err = f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=$1`, eligibilityID)
	must(t, err)
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		must(t, tx.QueryRow(ctx, `SELECT payload FROM events WHERE id=$1`, decisionID).Scan(&raw))
		if _, _, err := resolveEvidence(ctx, tx, sourceEvent{source: Source{QueueID: 1, ID: decisionID, Type: "landing_decision_recorded"}, payload: raw}); err == nil {
			t.Fatal("required retained eligibility absence accepted")
		}
		return nil
	}))
}

func TestEvidenceTypedSourceRejectsPayloadMismatch(t *testing.T) {
	_, raw, err := queue.EncodeEvent(queue.Landed{BatchID: "batch", NewBaseSHA: "sha", NewFence: "fence"})
	must(t, err)
	if _, err := decodeEvidence(&sourceEvent{source: Source{Type: "approval_eligibility_observed"}, payload: raw}); err != errSource {
		t.Fatal("source/payload type mismatch was not sanitized", err)
	}
}

func TestEvidenceSettlementAttemptSegments(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	first := evidenceInsert(t, f, queue.LandingStarted{BatchID: "batch", AttemptID: "one"})
	land1 := evidenceInsert(t, f, queue.Landed{BatchID: "batch", NewBaseSHA: "first", NewFence: "f1"})
	second := evidenceInsert(t, f, queue.LandingStarted{BatchID: "batch", AttemptID: "two"})
	land2 := evidenceInsert(t, f, queue.Landed{BatchID: "batch", NewBaseSHA: "second", NewFence: "f2"})
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		for _, pair := range [][2]int64{{first, land1}, {second, land2}} {
			var raw []byte
			must(t, tx.QueryRow(ctx, `SELECT payload FROM events WHERE id=$1`, pair[0]).Scan(&raw))
			refs, _, err := resolveEvidenceAt(ctx, tx, sourceEvent{source: Source{QueueID: 1, ID: pair[0], Type: "landing_started"}, payload: raw}, land2)
			if err != nil {
				return err
			}
			found := false
			for _, ref := range refs {
				if ref.ExpectedType == "landed" {
					found = ref.Source != nil && ref.Source.ID == pair[1]
				}
			}
			if !found {
				t.Fatal("settlement crossed attempt boundary", refs)
			}
		}
		return nil
	}))
}

func TestEvidenceRequiredRetainedSourceAndHistoricalAbsence(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	ownerID := insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		owner := EvidenceOwner{QueueID: 1, EventID: ownerID, BatchID: "missing-batch", AttemptID: "attempt"}
		r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: ownerID}}, owner: owner, refs: []Reference{}}
		if _, err := r.lookup("check", "batch_started", "missing-batch", `payload->'data'->>'batch_id'=$3`, true, "missing-batch"); err == nil {
			t.Fatal("required retained batch absence accepted")
		}
		if _, err := r.lookup("check", "checks_observed", "missing-batch/tested", `payload->'data'->>'batch_id'=$3`, false, "missing-batch"); err != nil {
			return err
		}
		if len(r.refs) != 1 || r.refs[0].Owner != owner || r.refs[0].Status != "not_recorded" || r.refs[0].Source != nil || r.refs[0].ExpectedType != "checks_observed" {
			t.Fatal("historical absence lost its explicit expected identity")
		}
		return nil
	}))
}
