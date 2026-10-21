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
// freshly acquire eligibility. ReadTestedMessages reads messages from the immutable
// TESTED commits (the marker for fast-forward), in pinned member order. It must
// not synthesize metadata from the current classification. Commit binds the
// store claim and invokes Store.StartLanding; it never emits individual events.
// There is no forge I/O or git push inside Commit's transaction.
type LandingDecisionGate struct {
	// ValidateChecks budgets authority evidence before any check-policy rejection.
	// The same final attempt ID is reused when the landing gate later commits.
	ValidateChecks func(context.Context, queue.Land, []forge.CheckRun, time.Time, string) error
	// ReadInputsWithChecks opts into the exact provenance-filtered, retry-aware
	// read and its observation time. It replaces ReadInputs only on authority
	// paths; legacy approval paths retain their fresh-read callback. The final
	// attempt ID is supplied before evidence is budgeted or conditions evaluated.
	ReadInputsWithChecks func(context.Context, queue.Land, queue.State, []forge.CheckRun, time.Time, string) ([]queue.ApprovalEligibilityObserved, error)
	// ReloadAfterInputs folds evidence journaled by the read before evaluating.
	ReloadAfterInputs  bool
	Reject             func(context.Context, []queue.ApprovalEligibilityObserved, []string, queue.LandingStarted, queue.ApprovalGateRejected) error
	Policy             cp.Policy
	Load               func(context.Context) (queue.State, error)
	ReadInputs         func(context.Context, queue.Land, queue.State) ([]queue.ApprovalEligibilityObserved, error)
	ReadTestedMessages func(context.Context, queue.Land) ([]string, error)
	Commit             func(context.Context, []queue.ApprovalEligibilityObserved, []queue.LandingDecisionRecorded, queue.LandingStarted) error
}

func (g *LandingDecisionGate) start(ctx context.Context, land queue.Land, start queue.LandingStarted, now func() time.Time, checks []forge.CheckRun, checksAt time.Time) error {
	if !g.Policy.Enabled() || g.Load == nil || (g.ReadInputs == nil && g.ReadInputsWithChecks == nil) || g.ReadTestedMessages == nil || g.Commit == nil {
		return fmt.Errorf("incomplete landing decision gate")
	}
	state, err := g.Load(ctx)
	if err != nil {
		return err
	}
	var fresh []queue.ApprovalEligibilityObserved
	if g.ReadInputsWithChecks != nil {
		fresh, err = g.ReadInputsWithChecks(ctx, land, state, checks, checksAt, start.AttemptID)
	} else {
		fresh, err = g.ReadInputs(ctx, land, state)
	}
	if err != nil {
		return err
	}
	if g.ReloadAfterInputs {
		state, err = g.Load(ctx)
		if err != nil {
			return err
		}
	}
	messages, err := g.ReadTestedMessages(ctx, land)
	if err != nil {
		return err
	}
	var metadata []*forge.ChangeMetadata
	for _, message := range messages {
		members, err := forge.ParseChangeTrailers(message)
		if err != nil {
			return err
		}
		metadata = append(metadata, members...)
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
