package sim

import (
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func faultMethod(action queue.Action) string {
	switch action.(type) {
	case queue.BuildTestRef:
		return "BuildTestRef"
	case queue.RefreshChecks:
		return "CheckRuns"
	case queue.Land:
		return "Land"
	case queue.ResolveLanding:
		return "ReadFence"
	case queue.DeleteRef:
		return "DeleteRef"
	case queue.ReportStatus, queue.Dequeue:
		return "ReportStatus"
	case queue.MarkLanded:
		return "MarkLanded"
	default:
		return ""
	}
}

// Builds get new batch keys after a failure, and uncertain lands must use
// ResolveLanding. Recovery therefore follows the logical operation, not just
// the original action key. A missed check read is also recovered by a terminal
// webhook followed by the definitive landing/dequeue event.
func faultScope(action queue.Action) string {
	switch a := action.(type) {
	case queue.BuildTestRef:
		return fmt.Sprintf("build/pr/%d", a.PRs[0].PR)
	case queue.RefreshChecks:
		return "checks/" + a.BatchID
	case queue.Land:
		return "landing/" + a.BatchID
	case queue.ResolveLanding:
		return "landing/" + a.BatchID
	default:
		return action.Key()
	}
}

func (s *simulation) injectFault(scope string) {
	s.result.FaultsInjected++
	s.pendingFaults[scope]++
}

func (s *simulation) recoverFaults(scope string) {
	s.result.FaultsRecovered += s.pendingFaults[scope]
	delete(s.pendingFaults, scope)
}
