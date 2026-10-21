package store

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestAtomicOwnerDecisionWithoutApprovals(t *testing.T) {
	s := newStore(t)
	q, claim, c, _ := decisionLand(t, s)
	c.Policy.Approvals = nil
	c.Policy.RequirePolicyOwners = true
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	decisions, err := state.PrepareLandingDecision(c.Policy, c.Fresh, c.Start, c.Land)
	must(t, err)
	before := eventCount(t, s, q)
	// The final-start rejection must roll back eligibility and owner decisions.
	calls := 0
	authorize := s.authorizeStart
	err = s.startLanding(t.Context(), claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{decisions}, c.Start, func(ctx context.Context, tx pgx.Tx, e emission, event queue.Event) (bool, error) {
		calls++
		if calls == 2 {
			return true, nil
		}
		return authorize(ctx, tx, e, event)
	})
	if !errors.Is(err, ErrStale) || eventCount(t, s, q) != before {
		t.Fatal("partial owner decision committed", err)
	}
	must(t, commitDecision(t, s, claim, c, decisions))
	if eventCount(t, s, q) != before+3 {
		t.Fatal("expected atomic eligibility, decision, start")
	}
}
