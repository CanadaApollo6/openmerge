package queue

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

func TestRequireNoneBriefAndReceipt(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	p.Approvals.Rules = []cp.Rule{{When: cp.Match{Labels: []string{"docs"}}, Require: cp.Requirement{Selector: cp.None}}}
	f := receiptFact(t, s, b, p)
	if !f.ApprovalsRequired || len(f.Obligations) != 0 {
		t.Fatal(f)
	}
	identity, err := brief.Identity(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, bare := range []bool{false, true} {
		var e ApprovalReceiptRecorded
		if bare {
			e, err = s.PrepareBareReceipt(p, b, person(2), "99", b.At, identity.BriefDigest)
		} else {
			e, err = s.PrepareReceipt(p, b, person(2), "99", b.At, identity)
		}
		if err != nil || !e.Accepted || s.CheckReceiptAdmission(e) != nil {
			t.Fatal(e, err)
		}
		if !restart(t, Apply(s, e)).ReceiptRecords(b.ChangeID)["99"].Accepted {
			t.Fatal("receipt lost")
		}
	}
}

func TestBriefPolicyDigestMismatch(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	for _, digest := range []string{"", "different-live-policy"} {
		p.Digest = digest
		var mismatch BriefPolicyMismatchError
		if _, err := s.RecordedBrief(p, b); !errors.As(err, &mismatch) || mismatch.Supplied != digest || mismatch.Recorded == "" {
			t.Fatal(err)
		}
		if _, err := s.PrepareBareReceipt(p, b, person(2), "99", b.At, "bad"); !errors.As(err, &mismatch) {
			t.Fatal(err)
		}
	}
}

func TestBriefFreshProgressOverlay(t *testing.T) {
	s, b, _, p, fresh := decisionSetup(t, ownerPool(2))
	recorded := receiptFact(t, s, b, p)
	identity, err := brief.Identity(recorded)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := s.evaluateApprovalsTest(p, identity, fresh, ownerPool(2))
	if !evaluation.Allowed {
		t.Fatal(evaluation)
	}
	overlaid := brief.OverlayProgress(recorded, evaluation)
	for _, row := range overlaid.Obligations {
		if !row.Met || row.Unresolved || row.Code != "met" {
			t.Fatal(row)
		}
	}
	after, err := brief.Identity(overlaid)
	if err != nil || identity != after {
		t.Fatal("progress changed identity", err)
	}
	for _, row := range recorded.Obligations {
		if row.Met {
			t.Fatal("overlay mutated recorded rows")
		}
	}
	// Fresh membership can disagree with recorded pools without entering Build.
	changed := s.evaluateApprovalsTest(p, identity, fresh, ownerPool(9))
	overlaid = brief.OverlayProgress(recorded, changed)
	after, err = brief.Identity(overlaid)
	if err != nil || identity != after {
		t.Fatal("fresh owner pool changed digest", err)
	}
	missing := brief.OverlayProgress(recorded, cp.Evaluation{})
	for _, row := range missing.Obligations {
		if row.Met || !row.Unresolved {
			t.Fatal("missing progress retained success")
		}
	}
}

func TestMalformedReceiptAudit(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	for _, raw := range []string{"", "BAD", strings.Repeat("g", 64), strings.Repeat("x", 3000), strings.Repeat("é", 600), "bad\xff"} {
		e, err := s.PrepareBareReceipt(p, b, person(2), "99", b.At, raw)
		if err != nil || e.Accepted || e.ReasonCode != "receipt_digest_malformed" || len(e.Supplied.BriefDigest) > 1024 {
			t.Fatal(e, err)
		}
		if len(raw) <= 1024 && raw != "bad\xff" && e.Supplied.BriefDigest != raw {
			t.Fatal("raw audit changed")
		}
		if err = s.CheckReceiptAdmission(e); err != nil {
			t.Fatal(err)
		}
		if got := restart(t, Apply(s, e)).ReceiptRecords(b.ChangeID)["99"]; got != e {
			t.Fatal("rejection lost")
		}
	}
	// Malformed attempts share the same cap as valid-but-mismatched digests.
	for i := 0; i < maxRejectedReceiptsPerActor; i++ {
		e, err := s.PrepareBareReceipt(p, b, person(2), strconv.Itoa(100+i), b.At, "BAD")
		if err != nil {
			t.Fatal(err)
		}
		s = Apply(s, e)
	}
	e, err := s.PrepareBareReceipt(p, b, person(2), "999", b.At, "BAD")
	if err != nil || s.CheckReceiptAdmission(e) == nil {
		t.Fatal("malformed cap bypassed", err)
	}
}

func TestOlderReceiptRevisionBoundary(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	receipt, _ := realReceipt(t, s, b, p, "99", b.At)
	s = Apply(s, receipt)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: strings.Repeat("d", 40), Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: b.At.Add(time.Hour)}})
	s = restart(t, s)
	for _, mutate := range []func(*ApprovalReceiptRecorded){
		func(e *ApprovalReceiptRecorded) {
			e.Basis.Head = strings.Repeat("e", 40)
			e.Current.Head = e.Basis.Head
			e.Supplied.Head = e.Basis.Head
		},
		func(e *ApprovalReceiptRecorded) { e.BoundaryAt = e.BoundaryAt.Add(time.Second) },
	} {
		raw, err := EncodeSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		bad, err := DecodeSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		e := receipt
		mutate(&e)
		bad.receipts[b.ChangeID]["99"] = e
		if _, err := EncodeSnapshot(bad); err == nil {
			t.Fatal("old revision contradiction accepted")
		}
	}
}
