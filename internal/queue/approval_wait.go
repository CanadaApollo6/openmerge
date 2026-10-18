package queue

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
)

// ApprovalWait binds a rejected member to the exact decision inputs read at the
// gate. Reasons are stable policy codes, not acquisition errors or prose.
type ApprovalWait struct {
	Basis       ApprovalBasis `json:"basis"`
	Fingerprint string        `json:"fingerprint"`
	Reasons     []string      `json:"reasons"`
}

// Validate requires a current binding, digest and canonical reason list.
func (w ApprovalWait) Validate() error {
	if w.Basis.Validate() != nil || !validApprovalFingerprint(w.Fingerprint) || len(w.Reasons) == 0 || len(w.Reasons) > 128 {
		return fmt.Errorf("invalid approval wait")
	}
	for i, r := range w.Reasons {
		if !stableApprovalReason(r) || i > 0 && w.Reasons[i-1] >= r {
			return fmt.Errorf("waiting reasons must be sorted and distinct")
		}
	}
	return nil
}

// ApprovalGateRejected discards one tested composition and its speculative
// suffix. Only Waiting members are withheld from subsequent compositions.
type ApprovalGateRejected struct {
	BatchID   string         `json:"batch_id"`
	TestedSHA string         `json:"tested_sha"`
	PRs       []BatchPR      `json:"prs"`
	Waiting   []ApprovalWait `json:"waiting"`
}

func (ApprovalGateRejected) queueEvent() {}

// Validate requires the exact nonempty composition and distinct rejected members.
func (e ApprovalGateRejected) Validate() error {
	if requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA) != nil || validateBatchPRs(e.PRs) != nil || len(e.PRs) > 128 || len(e.Waiting) == 0 || len(e.Waiting) > len(e.PRs) {
		return fmt.Errorf("invalid rejected composition")
	}
	seen := map[ChangeID]bool{}
	for _, w := range e.Waiting {
		if w.Validate() != nil || seen[w.Basis.ChangeID] {
			return fmt.Errorf("invalid rejected member")
		}
		seen[w.Basis.ChangeID] = true
	}
	return nil
}

// ApprovalInputsChanged journals a permission/membership probe only when its
// freshly observed decision inputs differ. PreviousFingerprint fences races
// with a later gate; Basis and Eligibility must describe the same current read.
type ApprovalInputsChanged struct {
	Basis               ApprovalBasis               `json:"basis"`
	PreviousFingerprint string                      `json:"previous_fingerprint"`
	Fingerprint         string                      `json:"fingerprint"`
	Eligibility         ApprovalEligibilityObserved `json:"eligibility"`
}

func (ApprovalInputsChanged) queueEvent() {}

// Validate requires changed fingerprints bound to one fresh eligibility read.
func (e ApprovalInputsChanged) Validate() error {
	if e.Basis.Validate() != nil || e.Eligibility.Validate() != nil || e.Basis != e.Eligibility.Basis || !validApprovalFingerprint(e.PreviousFingerprint) || !validApprovalFingerprint(e.Fingerprint) || e.Fingerprint == e.PreviousFingerprint {
		return fmt.Errorf("invalid changed approval inputs")
	}
	return nil
}

// A same-input poll still advances acquisition freshness. A delayed changed
// read must not overwrite that newer evidence and wake a wait spuriously.
func (s State) currentApprovalInputBasis(b ApprovalBasis) bool {
	if s.CheckApprovalBoundary(b) != nil {
		return false
	}
	r, _ := s.approvalBinding(b)
	if b.At.Before(r.Basis.At) {
		return false
	}
	if f, ok := s.approvalFacts[b.ChangeID]; ok && f.Eligibility != nil && f.Eligibility.Basis.Revision == b.Revision && b.At.Before(f.Eligibility.Basis.At) {
		return false
	}
	return true
}

func stableApprovalReason(reason string) bool {
	if len(reason) == 0 || len(reason) > 96 || reason[0] < 'a' || reason[0] > 'z' {
		return false
	}
	for _, r := range reason {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validApprovalFingerprint(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32
}
func (s State) hasApprovalWaitEntry(w ApprovalWait) bool {
	for _, entry := range s.Entries {
		r, _ := s.PRRevision(entry.PR)
		if r.ChangeID == w.Basis.ChangeID && r.Revision == w.Basis.Revision && entry.HeadSHA == w.Basis.Head {
			return true
		}
	}
	return false
}

func cloneApprovalWaits(waits map[ChangeID]ApprovalWait) map[ChangeID]ApprovalWait {
	out := maps.Clone(waits)
	for id, w := range out {
		w.Reasons = slices.Clone(w.Reasons)
		out[id] = w
	}
	return out
}

// ApprovalWaits returns detached waits in original admission order. Reconciliation
// probes these even when there is no active batch; planning performs no I/O.
func (s State) ApprovalWaits() []ApprovalWait {
	var out []ApprovalWait
	for _, entry := range s.Entries {
		r, _ := s.PRRevision(entry.PR)
		if w, ok := s.approvalWaits[r.ChangeID]; ok {
			w.Reasons = slices.Clone(w.Reasons)
			out = append(out, w)
		}
	}
	return out
}
func (s State) approvalWaiting(pr BatchPR) bool {
	if len(s.approvalWaits) == 0 {
		return false
	}
	r, _ := s.PRRevision(pr.PR)
	w, ok := s.approvalWaits[r.ChangeID]
	return ok && r.Revision == w.Basis.Revision && pr.HeadSHA == w.Basis.Head
}
func (s *State) pruneWaitingGroups() {
	if len(s.approvalWaits) == 0 {
		return
	}
	var groups []pendingGroup
	for _, g := range s.pending {
		prs := slices.DeleteFunc(slices.Clone(g.prs), s.approvalWaiting)
		if len(prs) > 0 {
			groups = append(groups, pendingGroup{prs: prs, bisection: g.bisection})
		}
	}
	s.pending = groups
}
func (s State) refreshApprovalWaits() State {
	for id, w := range s.approvalWaits {
		if !s.hasApprovalWaitEntry(w) || s.CheckApprovalBoundary(w.Basis) != nil || s.ApprovalFingerprint(id) != w.Fingerprint {
			s.approvalWaits = cloneApprovalWaits(s.approvalWaits)
			delete(s.approvalWaits, id)
		}
	}
	return s
}
func (s State) rejectApprovalGate(e ApprovalGateRejected) State {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil || b == nil || b.Phase != Testing || b.InvalidationReason != "" || b.TestedSHA != e.TestedSHA || !slices.Equal(b.PRs, e.PRs) {
		return s
	}
	for _, w := range e.Waiting {
		if !s.currentApprovalInputBasis(w.Basis) || s.ApprovalFingerprint(w.Basis.ChangeID) != w.Fingerprint {
			return s
		}
		member := false
		for _, p := range s.batchRevisions[b.ID] {
			r, _ := s.PRRevision(p.PR)
			member = member || r.ChangeID == w.Basis.ChangeID && p.Revision == w.Basis.Revision && p.HeadSHA == w.Basis.Head
		}
		if !member {
			return s
		}
	}
	s = cloneState(s)
	if s.approvalWaits == nil {
		s.approvalWaits = map[ChangeID]ApprovalWait{}
	}
	for _, w := range e.Waiting {
		w.Reasons = slices.Clone(w.Reasons)
		s.approvalWaits[w.Basis.ChangeID] = w
	}
	s.invalidateFrom(s.Batch(e.BatchID), "approval-wait")
	s.pruneWaitingGroups()
	return s
}

// PrepareApprovalInputChange is the read-only reconciliation seam. Equal polls
// return no event and never mint test refs or refresh CI timers. 11a supplies the
// gate reader; no production approval acquisition is enabled here.
func (s State) PrepareApprovalInputChange(fresh ApprovalEligibilityObserved) (ApprovalInputsChanged, bool) {
	w, ok := s.approvalWaits[fresh.Basis.ChangeID]
	if !ok || fresh.Validate() != nil || !s.currentApprovalInputBasis(fresh.Basis) || fresh.Basis.At.Before(w.Basis.At) {
		return ApprovalInputsChanged{}, false
	}
	next, _ := s.observeApproval(fresh)
	fingerprint := next.ApprovalFingerprint(fresh.Basis.ChangeID)
	if fingerprint == w.Fingerprint {
		return ApprovalInputsChanged{}, false
	}
	return ApprovalInputsChanged{fresh.Basis, w.Fingerprint, fingerprint, fresh}, true
}
func (s State) changeApprovalInputs(e ApprovalInputsChanged) State {
	if e.Validate() != nil {
		return s
	}
	prepared, ok := s.PrepareApprovalInputChange(e.Eligibility)
	if !ok || prepared.PreviousFingerprint != e.PreviousFingerprint || prepared.Fingerprint != e.Fingerprint {
		return s
	}
	next, _ := s.observeApproval(e.Eligibility)
	return next.refreshApprovalWaits()
}
func validateApprovalWaits(s State) error {
	if len(s.approvalWaits) > maxSnapshotEntries {
		return fmt.Errorf("too many approval waits")
	}
	for id, w := range s.approvalWaits {
		if w.Validate() != nil || id != w.Basis.ChangeID || !s.hasApprovalWaitEntry(w) || s.CheckApprovalBoundary(w.Basis) != nil || s.ApprovalFingerprint(id) != w.Fingerprint {
			return fmt.Errorf("invalid snapshot approval wait")
		}
	}
	return nil
}
