package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

type round3EvidenceFixture struct {
	f                       *fixture
	decision                queue.LandingDecisionRecorded
	decisionID, checksID    int64
	startID, pinID, freshID int64
	ids                     map[string][]int64
	events                  map[string][]queue.Event
}

func round3ApprovalFixture(t *testing.T) *round3EvidenceFixture {
	t.Helper()
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
	fx := &round3EvidenceFixture{f: f, ids: map[string][]int64{}, events: map[string][]queue.Event{}}
	var state queue.State
	add := func(e queue.Event) int64 {
		typ, _, e2 := queue.EncodeEvent(e)
		must(t, e2)
		id := evidenceInsert(t, f, e)
		fx.ids[typ] = append(fx.ids[typ], id)
		fx.events[typ] = append(fx.events[typ], e)
		return id
	}
	for _, raw := range input.Events {
		v, e := queue.DecodeEvent(raw)
		must(t, e)
		if receipt, ok := v.(queue.ApprovalReceiptRecorded); ok {
			fact, e := state.RecordedBrief(input.Policy, receipt.Basis)
			must(t, e)
			identity, e := brief.Identity(fact)
			must(t, e)
			v, e = state.PrepareBareReceipt(input.Policy, receipt.Basis, receipt.Actor, receipt.CommentID, receipt.CreatedAt, identity.BriefDigest)
			must(t, e)
		}
		add(v)
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
	fx.pinID = add(pin)
	fx.freshID = add(input.Fresh)
	fx.decision = decision
	fx.decisionID = add(decision)
	checks := queue.LandingChecksRecorded{BatchID: input.Start.BatchID, AttemptID: input.Start.AttemptID, ConfigCommit: "config", ConfigDigest: strings.Repeat("a", 64), PolicyDigest: input.Policy.Digest, TestedSHA: input.Land.TestedSHA, LandedSHA: decision.LandedSHA, Method: input.Land.Method, TestRef: input.Land.Ref, RequiredCheckSource: state.LandingCheckSources(), ObservedAt: input.Start.At, Checks: []queue.LandingCheck{}, CheckOrder: []int{}, Expressions: []string{condition.All().String()}, Inputs: []condition.ObservedInput{}, Members: []queue.LandingCheckMember{{PR: input.Land.PRs[0].PR, HeadSHA: input.Land.PRs[0].HeadSHA, Conditions: queue.LandingCheckCondition{Expression: 0, Inputs: []int{}}}}}
	fx.checksID = add(checks)
	fx.startID = add(input.Start)
	return fx
}

type round3WantReference struct {
	kind, typ, identity string
	sourceID            int64
}

func round3ReferenceKey(kind, typ, identity string) string {
	return kind + "\x00" + typ + "\x00" + identity
}

func round3ExpectedReferences(t *testing.T, fx *round3EvidenceFixture) []round3WantReference {
	t.Helper()
	d, a := fx.decision, fx.decision.Approval
	wants := []round3WantReference{
		{"authority", "policy_authority_applied", evidenceIdentity("governing"), fx.pinID}, {"classification", "change_revision_observed", evidenceIdentity(string(a.Basis.ChangeID), fmt.Sprint(a.Basis.Revision)), fx.ids["change_revision_observed"][0]},
		{"check", "batch_started", evidenceIdentity(d.BatchID), fx.ids["batch_started"][0]}, {"eligibility", "approval_eligibility_observed", evidenceIdentity(a.EligibilityReadID), fx.freshID},
		{"classification", "change_classified", evidenceIdentity(a.Identity.ChangeID, fmt.Sprint(a.Identity.Revision), fmt.Sprint(a.Identity.Generation)), fx.ids["change_classified"][0]}, {"accountability", "change_accountability_observed", evidenceIdentity(string(a.Basis.ChangeID), fmt.Sprint(a.Basis.Revision)), fx.ids["change_accountability_observed"][0]},
		{"check", "landing_checks_recorded", evidenceIdentity(d.BatchID, d.AttemptID), fx.checksID}, {"settlement", "landing_started", evidenceIdentity(d.BatchID, d.AttemptID), fx.startID},
		{"settlement", "landed", evidenceIdentity(d.BatchID), 0}, {"settlement", "landing_abandoned", evidenceIdentity(d.BatchID), 0},
	}
	for _, src := range a.Sources {
		if src.Receipt == nil {
			identity := evidenceIdentity(string(src.Source.Channel), src.Source.ID)
			var id int64
			for i, event := range fx.events["review_stance_observed"] {
				stance := event.(queue.ReviewStanceObserved)
				if stance.Basis.ReadID == src.Basis.ReadID && stance.Stance.Source.Channel == src.Source.Channel && stance.Stance.Source.ID == src.Source.ID {
					id = fx.ids["review_stance_observed"][i]
				}
			}
			if id == 0 {
				t.Fatal("fixture stance source missing", src.Source)
			}
			wants = append(wants, round3WantReference{"receipt", "review_stance_observed", identity, id})
			continue
		}
		var id int64
		for i, event := range fx.events["approval_receipt_recorded"] {
			receipt := event.(queue.ApprovalReceiptRecorded)
			if receipt.Basis.ReadID == src.Receipt.Basis.ReadID && receipt.CommentID == src.Source.ID {
				id = fx.ids["approval_receipt_recorded"][i]
			}
		}
		if id == 0 {
			t.Fatal("fixture receipt source missing", src.Source)
		}
		wants = append(wants, round3WantReference{"receipt", "approval_receipt_recorded", evidenceIdentity(string(src.Source.Channel), src.Source.ID), id})
	}
	return wants
}

func round3Resolve(t *testing.T, fx *round3EvidenceFixture, eventID int64, dropType string) ([]Reference, []sourceEvent, error) {
	t.Helper()
	var refs []Reference
	var sources []sourceEvent
	var resolveErr error
	must(t, fx.f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var typ string
		var raw []byte
		must(t, tx.QueryRow(ctx, `SELECT type,payload FROM events WHERE id=$1`, eventID).Scan(&typ, &raw))
		var maxID int64
		must(t, tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=1`).Scan(&maxID))
		resolverTx := tx
		if dropType != "" {
			resolverTx = &round3DropLookupTx{Tx: tx, typ: dropType}
		}
		refs, sources, resolveErr = resolveEvidenceAt(ctx, resolverTx, sourceEvent{source: Source{QueueID: 1, ID: eventID, Type: typ, Queue: Key{Installation: "7", Repository: "Owner/Repo", Base: "release"}}, payload: raw}, maxID)
		return nil
	}))
	return refs, sources, resolveErr
}

func TestRound3DecisionReferencesAreExactByFamily(t *testing.T) {
	fx := round3ApprovalFixture(t)
	refs, sources, err := round3Resolve(t, fx, fx.decisionID, "")
	must(t, err)
	if !validReferences(refs) {
		t.Fatal("invalid references", refs)
	}
	owner := EvidenceOwner{QueueID: 1, EventID: fx.decisionID, BatchID: fx.decision.BatchID, AttemptID: fx.decision.AttemptID, ChangeID: string(fx.decision.Approval.Basis.ChangeID), Revision: uint64(fx.decision.Approval.Basis.Revision), Head: fx.decision.Approval.Basis.Head, TestedSHA: fx.decision.TestedSHA}
	wants := round3ExpectedReferences(t, fx)
	if len(refs) != len(wants) || len(sources) != len(wants)-2 {
		t.Fatalf("references=%d sources=%d wants=%d", len(refs), len(sources), len(wants))
	}
	got := map[string]Reference{}
	for _, ref := range refs {
		key := round3ReferenceKey(ref.Kind, ref.ExpectedType, ref.Identity)
		if _, duplicate := got[key]; duplicate {
			t.Fatalf("duplicate exact identity: %q", key)
		}
		got[key] = ref
	}
	for _, want := range wants {
		key := round3ReferenceKey(want.kind, want.typ, want.identity)
		ref, ok := got[key]
		if !ok || ref.Owner != owner || ref.Relation != "relied_on" {
			t.Fatalf("missing or misowned family reference %q: %+v", key, ref)
		}
		if want.sourceID == 0 {
			if ref.Status != "not_recorded" || ref.Source != nil {
				t.Fatalf("expected explicit absence for %q: %+v", key, ref)
			}
		} else if ref.Status != "recorded" || ref.Source == nil || ref.Source.ID != want.sourceID || ref.Source.Type != want.typ || ref.Source.QueueID != owner.QueueID || ref.Source.Queue != (Key{Installation: "7", Repository: "Owner/Repo", Base: "release"}) || ref.Source.RecordedAt != "2026-10-22T12:00:00.000000Z" {
			t.Fatalf("wrong retained source for %q: %+v", key, ref)
		}
		delete(got, key)
	}
	if len(got) != 0 {
		t.Fatal("unexpected family references", got)
	}
}

type round3DropLookupTx struct {
	pgx.Tx
	typ string
}

func (tx *round3DropLookupTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "WITH retained_events AS NOT MATERIALIZED") && len(args) > 1 && args[1] == tx.typ {
		return &round3EmptyRows{}, nil
	}
	return tx.Tx.Query(ctx, sql, args...)
}

type round3EmptyRows struct{ pgx.Rows }

func (*round3EmptyRows) Next() bool        { return false }
func (*round3EmptyRows) Err() error        { return nil }
func (*round3EmptyRows) Close()            {}
func (*round3EmptyRows) Scan(...any) error { return nil }

func TestRound3RequiredFamilyLookupsRejectMissingIdentity(t *testing.T) {
	fx := round3ApprovalFixture(t)
	cases := []struct {
		name string
		id   int64
		typ  string
	}{
		{"decision revision", fx.decisionID, "change_revision_observed"}, {"decision batch", fx.decisionID, "batch_started"},
		{"eligibility", fx.decisionID, "approval_eligibility_observed"}, {"classified", fx.decisionID, "change_classified"},
		{"accountability", fx.decisionID, "change_accountability_observed"}, {"review stance", fx.decisionID, "review_stance_observed"},
		{"approval receipt", fx.decisionID, "approval_receipt_recorded"}, {"attempt checks under openmerge", fx.decisionID, "landing_checks_recorded"},
		{"attempt start", fx.decisionID, "landing_started"}, {"start owning decision", fx.startID, "landing_decision_recorded"},
		{"check basis batch", fx.checksID, "batch_started"}, {"check basis test ref", fx.checksID, "test_ref_built"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := round3Resolve(t, fx, tc.id, tc.typ)
			if err == nil {
				t.Fatalf("accepted missing %s identity", tc.typ)
			}
		})
	}

	t.Run("remedy revision", func(t *testing.T) {
		d := fx.decision
		d.Variant, d.Approval = "generated_remedy", nil
		d.Remedy = &queue.GeneratedRemedyDecision{ChangeID: fx.decision.Approval.Basis.ChangeID, Revision: fx.decision.Approval.Basis.Revision, Head: fx.decision.Approval.Basis.Head, Suspect: fx.decision.TestedSHA, Evidence: []string{"retained check regression"}, OnRed: "revert"}
		id := evidenceInsert(t, fx.f, d)
		if _, _, err := round3Resolve(t, fx, id, "change_revision_observed"); err == nil {
			t.Fatal("accepted missing remedy revision")
		}
	})
}

func TestRound3OptionalAuthorityAndChecksAbsence(t *testing.T) {
	for _, mode := range []string{"no-pin", "github-pin"} {
		t.Run(mode, func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			if mode == "no-pin" {
				must(t, deleteRound3Event(t, fx, fx.pinID))
			} else {
				var raw []byte
				must(t, fx.f.admin.QueryRow(t.Context(), `SELECT payload FROM events WHERE id=$1`, fx.pinID).Scan(&raw))
				v, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := v.(queue.PolicyAuthorityApplied)
				pin.Authority, pin.OpenMerge = "github", nil
				_, raw, err = queue.EncodeEvent(pin)
				must(t, err)
				_, err = fx.f.admin.Exec(t.Context(), `UPDATE events SET payload=$2 WHERE id=$1`, fx.pinID, raw)
				must(t, err)
			}
			must(t, deleteRound3Event(t, fx, fx.checksID))
			refs, _, err := round3Resolve(t, fx, fx.decisionID, "")
			must(t, err)
			find := func(typ string) Reference {
				for _, ref := range refs {
					if ref.ExpectedType == typ {
						return ref
					}
				}
				t.Fatalf("missing optional reference %s", typ)
				return Reference{}
			}
			if ref := find("policy_authority_applied"); ref.Status != "not_recorded" || ref.Source != nil {
				t.Fatalf("authority pin absence not explicit: %+v", ref)
			}
			if ref := find("landing_checks_recorded"); ref.Status != "not_recorded" || ref.Source != nil {
				t.Fatalf("optional check absence not explicit: %+v", ref)
			}
		})
	}
}

func TestRound3DecisionPayloadMustMatchOwningEvent(t *testing.T) {
	fx := round3ApprovalFixture(t)
	other := fx.decision
	other.LandedSHA = strings.Repeat("f", 40)
	otherID := evidenceInsert(t, fx.f, other)
	var raw []byte
	must(t, fx.f.admin.QueryRow(t.Context(), `SELECT payload FROM events WHERE id=$1`, fx.decisionID).Scan(&raw))
	var resolveErr error
	must(t, fx.f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var maxID int64
		must(t, tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=1`).Scan(&maxID))
		_, _, resolveErr = resolveEvidenceAt(ctx, tx, sourceEvent{source: Source{QueueID: 1, ID: otherID, Type: "landing_decision_recorded", Queue: Key{Installation: "7", Repository: "Owner/Repo", Base: "release"}}, payload: raw}, maxID)
		return nil
	}))
	if resolveErr == nil {
		t.Fatal("accepted a decision payload owned by a different event identity")
	}
}

func TestRound3DuplicateImmutableIdentityRejectedRegardlessOfRequiredness(t *testing.T) {
	fx := round3ApprovalFixture(t)
	batch := fx.events["batch_started"][0].(queue.BatchStarted)
	evidenceInsert(t, fx.f, batch)
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required-%t", required), func(t *testing.T) {
			var resolveErr error
			must(t, fx.f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
				var maxID int64
				must(t, tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=1`).Scan(&maxID))
				r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: fx.startID}}, owner: EvidenceOwner{QueueID: 1, EventID: fx.startID}, maxID: maxID, refs: []Reference{}, sources: []sourceEvent{}}
				_, resolveErr = r.lookup("check", "batch_started", batch.BatchID, `payload->'data'->>'batch_id'=$3`, required, batch.BatchID)
				return nil
			}))
			if resolveErr == nil {
				t.Fatal("accepted duplicate retained identity")
			}
		})
	}
}

func deleteRound3Event(t *testing.T, fx *round3EvidenceFixture, id int64) error {
	t.Helper()
	var typ string
	var raw []byte
	var at time.Time
	if err := fx.f.admin.QueryRow(t.Context(), `SELECT type,payload,recorded_at FROM events WHERE id=$1`, id).Scan(&typ, &raw, &at); err != nil {
		return err
	}
	if _, err := fx.f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=$1`, id); err != nil {
		return err
	}
	t.Cleanup(func() {
		_, err := fx.f.admin.Exec(context.Background(), `INSERT INTO events(id,queue_id,type,payload,recorded_at) VALUES($1,1,$2,$3,$4)`, id, typ, raw, at)
		must(t, err)
	})
	return nil
}

func TestRound3StartReferencesExactOwningDecision(t *testing.T) {
	fx := round3ApprovalFixture(t)
	refs, sources, err := round3Resolve(t, fx, fx.startID, "")
	must(t, err)
	if len(refs) != 3 || len(sources) != 1 {
		t.Fatal("start closure count", refs, sources)
	}
	found := false
	for _, ref := range refs {
		if ref.ExpectedType == "landing_decision_recorded" {
			found = true
			if ref.Identity != evidenceIdentity(fmt.Sprint(fx.decisionID)) || ref.Status != "recorded" || ref.Source == nil || ref.Source.ID != fx.decisionID || ref.Owner.EventID != fx.startID {
				t.Fatal("wrong decision edge", ref)
			}
		}
	}
	if !found {
		t.Fatal("owning decision edge dropped")
	}
}
