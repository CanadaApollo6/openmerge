package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func receiptFact(t *testing.T, s State, b ApprovalBasis, p cp.Policy) brief.Fact {
	t.Helper()
	f, err := s.RecordedBrief(p, b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func realReceipt(t *testing.T, s State, b ApprovalBasis, p cp.Policy, id string, at time.Time) (ApprovalReceiptRecorded, cp.ReceiptIdentity) {
	t.Helper()
	f := receiptFact(t, s, b, p)
	current, err := brief.Identity(f)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.PrepareReceipt(p, b, person(2), id, at, current)
	if err != nil {
		t.Fatal(err)
	}
	return e, current
}
func TestReceiptWireFactGoldenAndRejectedBindings(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	f := receiptFact(t, s, b, p)
	e, current := realReceipt(t, s, b, p, "20", b.At.Add(time.Minute))
	_, raw, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_7A") == "1" {
		if err = os.WriteFile("testdata/wire/v1/event_approval_receipt_recorded.json", raw, 0644); err != nil {
			t.Fatal(err)
		}
		facts, _ := json.MarshalIndent(f, "", "  ")
		if err = os.WriteFile("testdata/brief-facts.json", facts, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/wire/v1/event_approval_receipt_recorded.json")
	if err != nil || !bytes.Equal(want, raw) {
		t.Fatal("receipt golden", err)
	}
	facts, _ := json.MarshalIndent(f, "", "  ")
	want, err = os.ReadFile("testdata/brief-facts.json")
	if err != nil || !bytes.Equal(want, facts) {
		t.Fatal("fact golden", err)
	}
	accepted := restart(t, Apply(s, e))
	v, _ := latest(accepted, b)
	if v.Event.Stance.Receipt == nil || *v.Event.Stance.Receipt != current {
		t.Fatal("receipt not counted verbatim")
	}
	// Late rejection never passes through PR 6's revision-bound stance DTO.
	mutations := map[string]func(*cp.ReceiptIdentity){"digest": func(r *cp.ReceiptIdentity) { r.BriefDigest = strings.Repeat("f", 64) }, "change": func(r *cp.ReceiptIdentity) { r.ChangeID = "other" }, "revision": func(r *cp.ReceiptIdentity) { r.Revision = 2 }, "head": func(r *cp.ReceiptIdentity) { r.Head = strings.Repeat("a", 40) }, "intent": func(r *cp.ReceiptIdentity) { r.IntentDigest = "old" }, "policy": func(r *cp.ReceiptIdentity) { r.PolicyDigest = "old" }, "generation": func(r *cp.ReceiptIdentity) { r.Generation++ }, "base": func(r *cp.ReceiptIdentity) { r.Base = "old" }, "merge-base": func(r *cp.ReceiptIdentity) { r.MergeBase = "old" }, "absent": func(r *cp.ReceiptIdentity) { *r = cp.ReceiptIdentity{} }}
	vectors := map[string]ApprovalReceiptRecorded{}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			supplied := current
			mutate(&supplied)
			rejected, err := s.PrepareReceipt(p, b, person(2), "21", b.At, supplied)
			if err != nil || rejected.Accepted || rejected.Supplied != supplied {
				t.Fatal("bad rejection", err)
			}
			vectors[name] = rejected
			_, raw, err := EncodeEvent(rejected)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			next := restart(t, Apply(accepted, decoded))
			v, _ := latest(next, b)
			if v.Event.Stance.Receipt == nil || *v.Event.Stance.Receipt != current {
				t.Fatal("rejection changed stance")
			}
			records := next.ReceiptRecords(b.ChangeID)
			if records["21"].Accepted || records["21"].CommentID != "21" {
				t.Fatal("rejection lost")
			}
			// A repeated comment cannot change acceptance or rebind its digest.
			duplicate := e
			duplicate.CommentID = "21"
			next = Apply(next, duplicate)
			if next.ReceiptRecords(b.ChangeID)["21"].Accepted {
				t.Fatal("comment rebound")
			}
		})
	}
	rejectedRaw, _ := json.Marshal(vectors)
	if os.Getenv("UPDATE_7A") == "1" {
		if err := os.WriteFile("testdata/receipt-rejections.json", rejectedRaw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	rejectedGolden, err := os.ReadFile("testdata/receipt-rejections.json")
	if err != nil || !bytes.Equal(rejectedRaw, rejectedGolden) {
		t.Fatal("rejected-binding vectors drift", err)
	}

}
func TestOlderReviewDismissalDoesNotBarrierLaterNative(t *testing.T) {
	for _, kind := range []cp.StanceKind{cp.Approve, cp.RequestChanges} {
		for _, source := range []forge.NativeTimeSource{forge.ReviewDismissalTime, forge.TimelineDismissalTime, forge.ObservationTimeFallback} {
			s, b, current, p, fresh := decisionSetup(t, ownerPool(2))
			old := native(b, 103, kind, b.At)
			approval := native(b, 120, cp.Approve, b.At.Add(10*time.Second))
			d := dismissal(b, 103, source, b.At.Add(5*time.Minute))
			permutations([]ReviewStanceObserved{old, approval, d}, func(events []ReviewStanceObserved) {
				next := s
				for _, e := range events {
					next = restart(t, Apply(next, e))
				}
				if !next.evaluateApprovalsTest(p, current, fresh, ownerPool(2)).Allowed {
					t.Fatalf("older dismissal blocked later review kind=%s source=%s", kind, source)
				}
			})
		}
	}
}
func TestReceiptGenerationIntentAndLegacyInertness(t *testing.T) {
	s, b, _, p, fresh := decisionSetup(t, ownerPool(2))
	e, current := realReceipt(t, s, b, p, "20", b.At.Add(time.Minute))
	s = Apply(s, e)
	if !s.evaluateApprovalsTest(p, current, fresh, ownerPool(2)).Allowed {
		t.Fatal("accepted receipt failed")
	}
	changed := Intent(b, "edited", "body")
	next := Apply(s, changed)
	if next.EvaluateApprovals(p, current, fresh).Allowed {
		t.Fatal("current intent digest not asserted")
	}
	f := receiptFact(t, next, b, p)
	if brief.ValidateReceipt(e.Supplied, f) == nil {
		t.Fatal("intent edit retained receipt")
	}
	c, _ := s.Classification(b.ChangeID, b.Revision)
	c.Evidence.Classification.Inputs.Generation++
	rewritten := Apply(s, c)
	f = receiptFact(t, rewritten, b, p)
	if brief.ValidateReceipt(e.Supplied, f) == nil {
		t.Fatal("identical paths after rewrite retained receipt")
	}
	cfg, legacy, now := chainState(t, 4, 1)
	legacy, batch := admitChain(t, cfg, legacy, now)
	before := legacy.Active.TestedSHA
	plan := Plan(cfg, legacy, now)
	// Receipt/intent side facts leave tested composition and CI actions alone.
	legacy.changeRevisions = s.changeRevisions
	legacy = Apply(legacy, e)
	legacy = Apply(legacy, changed)
	if legacy.Active.TestedSHA != before || legacy.Active.ID != batch.ID {
		t.Fatal("intent discarded tests")
	}
	after := Plan(cfg, legacy, now)
	raw, _ := json.Marshal(plan)
	other, _ := json.Marshal(after)
	if !bytes.Equal(raw, other) {
		t.Fatal("receipt altered planner")
	}
	if (State{}).receipts != nil {
		t.Fatal("legacy receipt projection not nil")
	}
}

func TestClassificationRetentionAndSnapshotBudget(t *testing.T) {
	first := classificationEvent(t)
	s := Apply(State{}, first)
	id := ChangeID(first.Evidence.Classification.Inputs.ChangeID)
	next := detachedClassification(first)
	next.Evidence.Classification.Inputs.Revision = 2
	s = Apply(s, next)
	if _, ok := s.Classification(id, 1); ok {
		t.Fatal("old revision retained in projection")
	}
	if _, ok := s.Classification(id, 2); !ok {
		t.Fatal("latest evidence missing")
	}
	s = Apply(s, first)
	if _, ok := s.Classification(id, 1); ok {
		t.Fatal("late evidence resurrected")
	}
	oversize := detachedClassification(next)
	oversize.Evidence.Classification.Inputs.Generation++
	oversize.Evidence.Classification.CodeOwners = []cp.CodeOwner{{Path: "docs/a.md", Side: cp.NewSide, Eligible: cp.IdentitySet{Reason: strings.Repeat("x", 16<<20)}}}
	var budget ClassificationSnapshotBudgetError
	if err := s.CheckClassificationAdmission(oversize); !errors.As(err, &budget) || budget.ChangeID != id {
		t.Fatal("missing typed per-change admission rejection", err)
	}
	// The prior state remains recoverable and does not admit the oversized fact.
	if _, err := EncodeSnapshot(s); err != nil {
		t.Fatal(err)
	}
	excessive := classificationEvent(t)
	excessive.Evidence.Classification.Files[0].Hunks.Hunks[0].Text = strings.Repeat("\x01", 700000)
	if excessive.Validate() == nil {
		t.Fatal("escaped hunk cap not enforced on persisted evidence")
	}
}

func TestBriefOwnerReasonCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool classification.OwnerResolver
		code string
	}{{"excluded", ownerPool(1), "excluded_owner"}, {"unknown", nil, "unknown_owners"}, {"writer eligibility", ownerPool(2), "unknown_eligibility"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, _, p, _ := decisionSetup(t, tc.pool)
			f := receiptFact(t, s, b, p)
			found := false
			for _, r := range f.Obligations {
				if r.Path != "" && r.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", tc.code, f.Obligations)
			}
		})
	}
	t.Run("mixed pool", func(t *testing.T) {
		s, b, _, p, _ := decisionSetup(t, ownerPool(1, 2))
		f := receiptFact(t, s, b, p)
		for _, r := range f.Obligations {
			if r.Code == "excluded_owner" {
				t.Fatal("mixed owner pool reported entirely excluded")
			}
		}
	})

	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	e, _ := s.Classification(b.ChangeID, b.Revision)
	e.Evidence.Classification.Inputs.Generation++
	e.Evidence.Classification.InventoryComplete = false
	e.Evidence.Inventory.Completeness = forge.DiffIncomplete
	e.Evidence.Classification.PathsSafe = false
	e.Evidence.Classification.HunksComplete = false
	e.Evidence.Classification.CodeOwnersKnown = false
	e.Evidence.Classification.PolicyOwnersKnown = false
	s = Apply(s, e)
	f := receiptFact(t, s, b, p)
	foundInventory := false
	for _, r := range f.Obligations {
		if r.Code == "unknown_inventory" {
			foundInventory = true
		}
	}
	if !foundInventory {
		t.Fatal("incomplete brief hid inventory obligation")
	}
}
