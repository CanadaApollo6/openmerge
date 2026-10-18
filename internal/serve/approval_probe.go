package serve

import (
	"context"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"strconv"
	"time"
)

// probeApprovalInputs runs on reconciliation, including idle queues. 11a installs
// the fresh read-only eligibility reader after ingestion is available. It must
// not build refs or use cached permission/membership evidence.
func (s *Server) probeApprovalInputs(ctx context.Context, w *worker, state queue.State) error {
	if w.stageA != nil {
		w.approvalInputProbe = func(ctx context.Context, b queue.ApprovalBasis) (queue.ApprovalEligibilityObserved, error) {
			current, _, err := s.store.Load(ctx, w.id)
			if err != nil {
				return queue.ApprovalEligibilityObserved{}, err
			}
			b.At = time.Now().UTC()
			b.ReadID = strconv.FormatInt(b.At.UnixNano(), 10)
			return s.readStageAInputs(ctx, w, current, b)
		}
	}
	if w.approvalInputProbe == nil {
		return nil
	}
	for _, wait := range state.ApprovalWaits() {
		fresh, err := w.approvalInputProbe(ctx, wait.Basis)
		if err != nil {
			return err
		}
		// Reload after I/O: another observation or gate may have replaced this wait.
		current, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return err
		}
		event, changed := current.PrepareApprovalInputChange(fresh)
		if changed {
			if err := s.recordStageA(ctx, w, "stage-a/probe/"+string(event.Basis.ChangeID), event.Fingerprint, event); err != nil {
				return err
			}
		}
	}
	return nil
}
