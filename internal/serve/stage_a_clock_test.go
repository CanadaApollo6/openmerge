package serve

import (
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestStageASequentialClockSkewRecordsDenial(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", false)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	facts, _ := state.ApprovalFacts(r.ChangeID)
	positive := *facts.Eligibility
	positive.Groups = map[string]cp.IdentitySet{"reviewers": {Resolved: true, Fresh: true, IDs: []string{"github.com:user:21"}}}
	positive.Basis = approvalBasis(r)
	positive.Basis.At = time.Now().Add(time.Hour)
	first, err := s.store.BeginApprovalRead(t.Context(), w.id, []queue.ChangeID{r.ChangeID})
	must(t, err)
	err = s.recordStageA(first.Context(t.Context()), w, "positive-skew", positive, positive)
	must(t, err)
	read, err := s.store.BeginApprovalRead(t.Context(), w.id, []queue.ChangeID{r.ChangeID})
	must(t, err)
	negative := positive
	negative.Basis = approvalBasis(r)
	negative.Groups = map[string]cp.IdentitySet{"reviewers": {Resolved: false, IDs: []string{}, Reason: "unavailable"}}
	err = s.recordStageA(read.Context(t.Context()), w, "clock-skew", negative, negative)
	if err != nil {
		t.Fatalf("later nonoverlapping acquisition rejected solely due to clock skew: %v", err)
	}
	current, _ := shadowState(t, s, w).ApprovalFacts(r.ChangeID)
	if current.Eligibility.Groups["reviewers"].Resolved {
		t.Fatal("later denial ignored")
	}
}
