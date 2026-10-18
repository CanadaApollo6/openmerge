package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReceiptWakesApprovalWaitAndPreflights(t *testing.T) {
	s, b, _, p, fresh := decisionSetup(t, ownerPool(2))
	s.BaseSHA = s.changeRevisions.snapshot()[12].Basis.BaseSHA
	s = Apply(s, PRObserved{PR: 12, HeadSHA: b.Head, Eligible: true, Trusted: true})
	s = Apply(s, EnqueueRequested{PR: 12, At: b.At})
	s = Apply(s, fresh)
	cfg := Config{BatchMaxSize: 1, MergeMethod: Merge, CheckTimeout: time.Minute, RefreshInterval: time.Second}
	s = waitBuild(t, s, cfg, b.At)
	s = rejectWait(t, s, s.Active, b)
	s = restart(t, s)
	receipt, _ := realReceipt(t, s, b, p, "20", b.At.Add(time.Minute))
	before := s.ApprovalFingerprint(b.ChangeID)
	if err := s.CheckReceiptAdmission(receipt); err != nil {
		t.Fatal(err)
	}
	if len(s.ApprovalWaits()) != 1 || s.ApprovalFingerprint(b.ChangeID) != before {
		t.Fatal("preflight changed original wait")
	}
	next := restart(t, Apply(s, receipt))
	if len(next.ApprovalWaits()) != 0 || next.ApprovalFingerprint(b.ChangeID) == before {
		t.Fatal("accepted receipt did not wake wait")
	}
	// Rejected receipt audit evidence is not a decision input.
	rejected := receipt
	rejected.CommentID = "21"
	rejected.Supplied.BriefDigest = "bad"
	rejected.Accepted = false
	rejected.ReasonCode = "receipt_digest_malformed"
	if err := s.CheckReceiptAdmission(rejected); err != nil {
		t.Fatal(err)
	}
	if got := restart(t, Apply(s, rejected)); len(got.ApprovalWaits()) != 1 || got.ApprovalFingerprint(b.ChangeID) != before {
		t.Fatal("rejected receipt woke wait")
	}
	changed := Intent(b, "changed", "body")
	if err := s.CheckApprovalAdmission(changed); err != nil {
		t.Fatal(err)
	}
	if got := restart(t, Apply(s, changed)); len(got.ApprovalWaits()) != 0 {
		t.Fatal("changed intent did not wake wait")
	}
	classified, _ := s.Classification(b.ChangeID, b.Revision)
	classified.Evidence.Classification.Inputs.Generation++
	if err := s.CheckClassificationAdmission(classified); err != nil {
		t.Fatal(err)
	}
	if got := restart(t, Apply(s, classified)); len(got.ApprovalWaits()) != 0 {
		t.Fatal("changed classification did not wake wait")
	}
}

func TestReceiptSnapshotUpgradesMainV5WithApprovalWait(t *testing.T) {
	s, cfg, bases := waitQueue(t, 1, 1, 1)
	s = waitBuild(t, s, cfg, bases[0].At)
	s = rejectWait(t, s, s.Active, bases[0])
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Version"] = json.RawMessage(`5`)
	delete(fields, "Receipts")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.receipts != nil || len(restored.ApprovalWaits()) != 1 || restored.ApprovalFingerprint(bases[0].ChangeID) != s.ApprovalFingerprint(bases[0].ChangeID) {
		t.Fatal("v5 upgrade lost wait or invented receipts")
	}
	restart(t, restored)
}
