package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ErrLandingApprovals means the rejected composition and waits committed.
var ErrLandingApprovals = errors.New("landing approvals unmet")

// LandingDecisionGate is the dormant Stage A activation seam. ReadInputs must
// freshly acquire eligibility and read authoritative metadata from the immutable
// TESTED commits (the marker for fast-forward), in pinned member order. It must
// not synthesize metadata from the current classification. Commit binds the
// store claim and invokes Store.StartLanding; it never emits individual events.
// There is no forge I/O or git push inside Commit's transaction.
type LandingDecisionGate struct {
	Reject     func(context.Context, []queue.ApprovalEligibilityObserved, []string, queue.LandingStarted, queue.ApprovalGateRejected) error
	Policy     cp.Policy
	Load       func(context.Context) (queue.State, error)
	ReadInputs func(context.Context, queue.Land, queue.State) ([]queue.ApprovalEligibilityObserved, []*forge.ChangeMetadata, error)
	Commit     func(context.Context, []queue.ApprovalEligibilityObserved, []queue.LandingDecisionRecorded, queue.LandingStarted) error
}

func (g *LandingDecisionGate) start(ctx context.Context, land queue.Land, start queue.LandingStarted, now func() time.Time) error {
	if g.Policy.Approvals == nil || g.Load == nil || g.ReadInputs == nil || g.Commit == nil {
		return fmt.Errorf("incomplete landing decision gate")
	}
	state, err := g.Load(ctx)
	if err != nil {
		return err
	}
	fresh, metadata, err := g.ReadInputs(ctx, land, state)
	if err != nil {
		return err
	}
	// Eligibility timestamps and the lease check must use the end of preflight,
	// not the time before a potentially slow forge request.
	start.At = now()
	if len(fresh) != len(land.PRs) || len(metadata) != len(land.PRs) {
		return fmt.Errorf("incomplete landing evidence")
	}
	rejection, err := state.PrepareLandingRejection(g.Policy, land, fresh)
	if err != nil {
		return err
	}
	if len(rejection.Waiting) > 0 {
		if g.Reject == nil {
			return fmt.Errorf("approval rejection requires atomic persistence")
		}
		fingerprints := make([]string, len(fresh))
		for i, f := range fresh {
			fingerprints[i] = state.ApprovalFingerprint(f.Basis.ChangeID)
		}
		start.At = now()
		if err := g.Reject(ctx, fresh, fingerprints, start, rejection); err != nil {
			return err
		}
		return ErrLandingApprovals
	}
	decisions := make([]queue.LandingDecisionRecorded, len(fresh))
	for i, f := range fresh {
		decision, err := state.PrepareLandingDecision(g.Policy, f, start, land)
		if err != nil {
			return err
		}
		m := metadata[i]
		// ChangeMetadata deliberately has no generation. The snapshot records the
		// recorded generation, and verifies its labels against the tested trailers.
		if m == nil || m.ChangeID != string(f.Basis.ChangeID) || m.Revision != uint64(f.Basis.Revision) || !slices.Equal(m.Labels, decision.Approval.Labels) {
			return fmt.Errorf("tested classification trailers mismatch for %s", f.Basis.ChangeID)
		}
		if _, err := m.Trailers(); err != nil {
			return err
		}
		decisions[i] = decision
	}
	if err := state.ValidateLandingDecisions(g.Policy, land, start, fresh, decisions); err != nil {
		return err
	}
	start.At = now()
	return g.Commit(ctx, fresh, decisions, start)
}
