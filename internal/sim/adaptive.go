package sim

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// These oracles observe accepted transitions independently of the reducer's
// adaptation helpers. A smaller W may coexist with already-admitted work.
func (s *simulation) observeAdaptation(before queue.State, event queue.Event) {
	head := before.Active
	failed := false
	switch e := event.(type) {
	case queue.BatchStarted:
		if before.PendingBatchSize() == 0 {
			s.observeFreshBatch(len(e.PRs))
			if len(e.PRs) > s.expectedFreshSize(before) {
				s.violation = fmt.Errorf("fresh batch exceeded guarded adaptive size: %d > %d", len(e.PRs), s.expectedFreshSize(before))
			}
		}
	case queue.ChainedBatchStarted:
		if before.PendingBatchSize() == 0 {
			s.observeFreshBatch(len(e.PRs))
			if len(e.PRs) > s.expectedFreshSize(before) {
				s.violation = fmt.Errorf("fresh batch exceeded adaptive size: %d > %d", len(e.PRs), s.expectedFreshSize(before))
			}
		}
		if b := s.state.Batch(e.BatchID); b != nil && before.Batch(e.BatchID) == nil && len(before.Batches()) >= s.expectedWindow {
			s.violation = fmt.Errorf("admission exceeded adaptive window: %d >= %d", len(before.Batches()), s.expectedWindow)
		}
	case queue.Landed:
		if head != nil && head.ID == e.BatchID && head.Phase == queue.Landing && s.state.Batch(e.BatchID) == nil {
			s.expectedWindow = min(s.cfg.Planner.MaxDepth(), s.expectedWindow+1)
			for range head.PRs {
				s.oracleOutcome(false)
			}
		}
	case queue.Dequeued:
		for _, entry := range before.Entries {
			if entry.PR == e.PR && entry.AdmissionID == e.AdmissionID && !hasAdmission(s.state, entry.AdmissionID) && (e.Reason == queue.ChecksFailed || e.Reason == queue.ChecksTimeout) {
				s.oracleOutcome(true)
				failed = head != nil && len(head.PRs) == 1 && head.PRs[0].PR == e.PR
			}
		}
	case queue.ChecksFailureConfirmed:
		failed = head != nil && head.ID == e.BatchID && head.Phase == queue.Testing && head.InvalidationReason == "" && (s.state.Batch(e.BatchID) == nil || s.state.Active.CheckRetry != nil && s.state.Active.CheckRetry.Confirmed)
	case queue.BatchFailed:
		failed = head != nil && head.ID == e.BatchID && head.Phase == queue.Testing && !before.ChecksPassed(head.TestedSHA)
	case queue.ChecksCompleted:
		failed = !e.Success && head != nil && !head.Speculative && head.Phase == queue.Testing && head.TestedSHA == e.TestedSHA
	}
	if failed && !head.FromBisection && !s.windowFailures[head.ID] {
		s.expectedWindow = max(max(1, s.cfg.Planner.SpeculationMinDepth), s.expectedWindow/2)
		s.windowFailures[head.ID] = true
	}
}
func hasAdmission(s queue.State, id uint64) bool {
	for _, e := range s.Entries {
		if e.AdmissionID == id {
			return true
		}
	}
	return false
}
func (s *simulation) oracleOutcome(failed bool) {
	s.expectedOutcomes = append(s.expectedOutcomes, failed)
	if len(s.expectedOutcomes) > 200 {
		s.expectedOutcomes = s.expectedOutcomes[1:]
	}
}

// observeFreshBatch counts fresh batch starts for the lab: how many started at
// an admission window of 1, where the backlog guard applies, and how many of
// those exceeded the rational adaptive size, which only the guard allows.
func (s *simulation) observeFreshBatch(size int) {
	s.freshBatches++
	if s.expectedWindow == 1 {
		s.freshAtWindowOne++
		if size > s.expectedBatchSize() {
			s.guardRaised++
		}
	}
}

// expectedFreshSize bounds a fresh batch independently of the planner's guard:
// the rational adaptive size, raised when the backlog guard applies (a target
// is set, the guard is on and the event-oracle window is 1) to
// min(max, waiting), where waiting counts every unbatched trusted entry before
// the start (a superset of the planner's eligible candidates, so the bound is
// conservative).
func (s *simulation) expectedFreshSize(before queue.State) int {
	size := s.expectedBatchSize()
	if s.cfg.Lab != nil && s.cfg.Lab.Strategy.BacklogGuard {
		// The sim-only lab guard drops the target for a pass and may start a
		// full batch.
		return max(1, s.cfg.Planner.BatchMaxSize)
	}
	if s.cfg.Planner.BatchTargetPassRate == 0 || s.cfg.Planner.BatchBacklogGuardOff || s.expectedWindow != 1 {
		return size
	}
	batched := make(map[int]bool)
	for _, b := range before.Batches() {
		for _, pr := range b.PRs {
			batched[pr.PR] = true
		}
	}
	waiting := 0
	for _, entry := range before.Entries {
		if !batched[entry.PR] && !s.prs[entry.PR].untrusted {
			waiting++
		}
	}
	return max(size, min(max(1, s.cfg.Planner.BatchMaxSize), waiting))
}

func (s *simulation) expectedBatchSize() int {
	size := max(1, s.cfg.Planner.BatchMaxSize)
	if s.cfg.Planner.BatchTargetPassRate == 0 || len(s.expectedOutcomes) < 20 {
		return size
	}
	good := len(s.expectedOutcomes)
	for _, f := range s.expectedOutcomes {
		if f {
			good--
		}
	}
	rate, _ := new(big.Rat).SetString(strconv.FormatFloat(s.cfg.Planner.BatchTargetPassRate, 'g', -1, 64))
	probability := big.NewRat(1, 1)
	pass := big.NewRat(int64(good), int64(len(s.expectedOutcomes)))
	for k := 1; k <= size; k++ {
		probability.Mul(probability, pass)
		if probability.Cmp(rate) < 0 {
			return max(1, k-1)
		}
	}
	return size
}
