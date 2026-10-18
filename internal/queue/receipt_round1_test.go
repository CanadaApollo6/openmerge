package queue

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
)

func TestReceiptChronologyParsingAndDeferral(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	f := receiptFact(t, s, b, p)
	identity, _ := brief.Identity(f)
	boundary, _ := s.approvalBinding(b)
	for _, offset := range []time.Duration{-time.Hour, 0, 3 * time.Second, 3*time.Second + time.Nanosecond} {
		e, err := s.PrepareReceipt(p, b, person(2), "99", boundary.Basis.At.Add(offset), identity)
		if err != nil || e.Accepted != (offset > 3*time.Second) {
			t.Fatalf("chronology %v: %+v %v", offset, e, err)
		}
		if !e.Accepted && e.ReasonCode != "receipt_before_revision" {
			t.Fatal(e.ReasonCode)
		}
		next := restart(t, Apply(s, e))
		if next.ReceiptRecords(b.ChangeID)["99"].Accepted != e.Accepted {
			t.Fatal("outcome lost")
		}
	}
	for _, value := range []string{identity.BriefDigest, " \n" + identity.BriefDigest + "\t"} {
		e, err := s.PrepareBareReceipt(p, b, person(2), "99", b.At, value)
		if err != nil || !e.Accepted || e.Supplied != identity {
			t.Fatal(e, err)
		}
	}
	for _, value := range []string{"", identity.BriefDigest[:63], strings.ToUpper(identity.BriefDigest), strings.Repeat("g", 64), identity.BriefDigest + "0"} {
		if _, err := ParseReceiptDigest(value); err == nil {
			t.Fatalf("parsed %q", value)
		}
	}
	e, err := s.PrepareBareReceipt(p, b, person(2), "99", b.At, strings.Repeat("f", 64))
	if err != nil || e.Accepted || e.ReasonCode != "receipt_digest_mismatch" {
		t.Fatal(e, err)
	}
	missing := s
	missing.approvalFacts = nil
	if _, err := missing.PrepareBareReceipt(p, b, person(2), "99", b.At, identity.BriefDigest); err == nil {
		t.Fatal("missing evidence must defer")
	}
	supplied := identity
	supplied.Head = strings.Repeat("x", 1025)
	if _, err := s.PrepareReceipt(p, b, person(2), "99", b.At, supplied); err == nil {
		t.Fatal("unbounded supplied audit data")
	}
	for _, mutate := range []func(*ApprovalReceiptRecorded){
		func(e *ApprovalReceiptRecorded) { e.Basis.Revision++; e.Current.Revision++ },
		func(e *ApprovalReceiptRecorded) {
			e.Basis.Head = strings.Repeat("c", 40)
			e.Current.Head = e.Basis.Head
		},
		func(e *ApprovalReceiptRecorded) { e.BoundaryAt = e.BoundaryAt.Add(time.Second) },
	} {
		bad := e
		mutate(&bad)
		if err := bad.Validate(); err != nil {
			t.Fatal("test must isolate revision cross-check", err)
		}
		next := Apply(s, e)
		next.receipts[b.ChangeID][e.CommentID] = bad
		if _, err := EncodeSnapshot(next); err == nil {
			t.Fatal("receipt basis contradicts revision")
		}
	}
}

func TestReceiptRejectionActorCap(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	for i := 0; i < maxRejectedReceiptsPerActor; i++ {
		e, err := s.PrepareBareReceipt(p, b, person(2), fmt.Sprint(100+i), b.At, strings.Repeat("f", 64))
		if err != nil || s.CheckReceiptAdmission(e) != nil {
			t.Fatal(err)
		}
		s = Apply(s, e)
	}
	e, _ := s.PrepareBareReceipt(p, b, person(2), "999", b.At, strings.Repeat("f", 64))
	if s.CheckReceiptAdmission(e) == nil {
		t.Fatal("actor cap not preflighted")
	}
	if len(Apply(s, e).ReceiptRecords(b.ChangeID)) != maxRejectedReceiptsPerActor {
		t.Fatal("actor exhausted reserve")
	}
	e.Actor = person(3)
	if s.CheckReceiptAdmission(e) != nil {
		t.Fatal("other actor blocked")
	}
	restart(t, Apply(s, e))
}

func TestRecordedBriefIgnoresFreshMembership(t *testing.T) {
	s, b, _, p, fresh := decisionSetup(t, ownerPool(2))
	f := receiptFact(t, s, b, p)
	before, _ := brief.Identity(f)
	fresh.OwnerPools = map[string]cp.IdentitySet{}
	stored, _ := s.Classification(b.ChangeID, b.Revision)
	classification.ResolveOwnerEligibility(stored.Evidence, func(owner classification.Owner) cp.IdentitySet {
		pool := ownerPool(2, 9)(owner)
		fresh.OwnerPools[owner.Label()] = pool
		return pool
	})
	s.EvaluateApprovals(p, before, fresh)
	s = Apply(s, fresh)
	after, _ := brief.Identity(receiptFact(t, s, b, p))
	if before != after {
		t.Fatal("fresh membership entered recorded identity")
	}
	c, _ := s.Classification(b.ChangeID, b.Revision)
	c.Evidence.Classification.Inputs.Generation++
	c.Evidence.Classification.CodeOwners[0].Eligible.IDs = append(c.Evidence.Classification.CodeOwners[0].Eligible.IDs, person(9).ID)
	s = Apply(s, c)
	changed, _ := brief.Identity(receiptFact(t, s, b, p))
	if changed.BriefDigest == before.BriefDigest {
		t.Fatal("recorded membership omitted")
	}
}

// Each generated transition begins at a reachable near-capacity state, then
// must encode, decode, and re-encode byte-identically. Seeds cross capacity with
// receipt positives, native noise, barriers, duplicate receipts and rejections.
func FuzzReceiptSnapshotReachability(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 0})
	f.Add([]byte{1, 1, 1, 0, 2, 4})
	f.Add([]byte{2, 3, 0, 5})
	f.Fuzz(func(t *testing.T, actions []byte) {
		if len(actions) > 16 {
			actions = actions[:16]
		}
		s, b, _, p, _ := decisionSetup(t, ownerPool(2))
		receipt, _ := realReceipt(t, s, b, p, "99", b.At)
		s = Apply(s, receipt)
		for i := 0; i < maxApprovalSources-3; i++ {
			s = Apply(s, native(b, int64(10000+i), cp.Approve, b.At.Add(time.Minute)))
		}
		s = restart(t, s)
		for i, action := range actions {
			id := int64(20000 + i)
			switch action % 6 {
			case 0:
				e, _ := realReceipt(t, s, b, p, fmt.Sprint(id), b.At.Add(2*time.Minute))
				s = Apply(s, e)
			case 1:
				s = Apply(s, native(b, id, cp.Approve, b.At.Add(3*time.Minute)))
			case 2:
				s = Apply(s, command(b, id, cp.Withdraw, b.At.Add(4*time.Minute)))
			case 3:
				s = Apply(s, native(b, id, cp.RequestChanges, b.At.Add(5*time.Minute)))
			case 4:
				s = Apply(s, receipt)
			case 5:
				e := receipt
				e.CommentID = fmt.Sprint(id)
				e.Supplied.BriefDigest = strings.Repeat("f", 64)
				e.Accepted = false
				e.ReasonCode = "receipt_digest_mismatch"
				s = Apply(s, e)
			}
			s = restart(t, s)
			got := s.ReceiptRecords(b.ChangeID)[receipt.CommentID]
			if !got.Accepted || got.Supplied != receipt.Supplied {
				t.Fatal("accepted pin lost")
			}
			raw, _ := EncodeSnapshot(s)
			decoded, err := DecodeSnapshot(raw)
			again, _ := EncodeSnapshot(decoded)
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatal("reachable state not round-trippable", err)
			}
		}
	})
}

func TestReceiptCannotBindBarrierSource(t *testing.T) {
	for _, kind := range []cp.StanceKind{cp.Withdraw, cp.RequestChanges} {
		s, b, _, p, _ := decisionSetup(t, ownerPool(2))
		e, _ := realReceipt(t, s, b, p, "99", b.At)
		barrier := e.stance()
		barrier.Stance.Kind = kind
		if err := barrier.Validate(); err != nil {
			t.Fatal(err)
		}
		s = Apply(s, barrier)
		if s.CheckReceiptAdmission(e) == nil {
			t.Fatal("receipt bound to barrier source")
		}
		s = restart(t, Apply(s, e))
		if _, ok := s.ReceiptRecords(b.ChangeID)[e.CommentID]; ok {
			t.Fatal("accepted record has no approval source")
		}
	}
}
