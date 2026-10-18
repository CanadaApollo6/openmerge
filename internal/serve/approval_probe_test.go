package serve

import (
	"context"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"strings"
	"testing"
	"time"
)

func TestApprovalIdleReconciliationProbesChangesOnly(t *testing.T) {
	s, w, _, d := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, false))
	pr := w.prs[1]
	id, err := queue.PRChangeID("7", repo.Path, "main", "", 1)
	must(t, err)
	now := time.Now().UTC()
	basis := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: pr.HeadSHA, ReadID: "gate", At: now}
	eligibility := queue.ApprovalEligibilityObserved{Basis: basis, Actors: map[string]cp.Actor{}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}, Known: true, Reasons: []string{}}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	events := []queue.Event{
		queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 1, HeadSHA: pr.HeadSHA, Basis: queue.RevisionObservationBasis{BaseSHA: state.BaseSHA, At: now.Add(-time.Hour)}},
		queue.EnqueueRequested{PR: 1, At: now}, queue.Intent(basis, "title", "body"), eligibility,
		queue.BatchStarted{BatchID: "b1", BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, At: now},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: strings.Repeat("e", 40), Method: queue.Merge, At: now},
	}
	_, err = s.store.Append(t.Context(), w.id, events, "")
	must(t, err)
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ApprovalGateRejected{BatchID: "b1", TestedSHA: state.Active.TestedSHA, PRs: state.Active.PRs, Waiting: []queue.ApprovalWait{{Basis: basis, Fingerprint: state.ApprovalFingerprint(id), Reasons: []string{"approval-required"}}}}, queue.BatchesInvalidated{BatchIDs: []string{"b1"}, Reason: "approval-wait"}}, "")
	must(t, err)
	state, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active != nil || len(state.ApprovalWaits()) != 1 {
		t.Fatal("fixture not idle waiting")
	}
	reads := 0
	w.approvalInputProbe = func(_ context.Context, b queue.ApprovalBasis) (queue.ApprovalEligibilityObserved, error) {
		reads++
		fresh := eligibility
		fresh.Basis = b
		fresh.Basis.At = now.Add(time.Duration(reads) * time.Second)
		fresh.Basis.ReadID = "probe"
		return fresh, nil
	}
	pulls := d.pulls
	for i := 0; i < 3; i++ {
		must(t, s.reconcile(t.Context(), w, false))
	}
	state, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if reads != 3 || before != after || d.pulls != pulls || state.Active != nil || len(state.ApprovalWaits()) != 1 {
		t.Fatal("unchanged idle polls wrote observations, read PRs or built work", before, after, reads)
	}
	w.approvalInputProbe = func(_ context.Context, b queue.ApprovalBasis) (queue.ApprovalEligibilityObserved, error) {
		fresh := eligibility
		fresh.Basis = b
		fresh.Basis.At = now.Add(time.Minute)
		fresh.Basis.ReadID = "changed-probe"
		fresh.Known = false
		fresh.Reasons = []string{"membership unavailable"}
		return fresh, nil
	}
	must(t, s.reconcile(t.Context(), w, false))
	state, changed, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if changed != after+1 || len(state.ApprovalWaits()) != 0 || state.Active != nil {
		t.Fatal("permission-only change did not record exactly one read-only wake", after, changed)
	}
}
