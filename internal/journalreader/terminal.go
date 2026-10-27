package journalreader

import (
	"strconv"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func retireTerminal(s pj.State, id uint64, event queue.Event, before, after queue.State) pj.State {
	// A definitive successful landing needs its later landed companion; all other
	// discarded/rebuilt attempts are terminal non-landed work.
	if outcome, landed := event.(queue.Landed); landed {
		if old := before.Batch(outcome.BatchID); old != nil && old.Phase == queue.Landing && after.Batch(outcome.BatchID) == nil && after.BaseSHA == outcome.NewBaseSHA {
			s = pj.AwaitLandedOwned(s, id, old.ID, old.LandingAttempt.ID)
		}
	} else {
		for _, old := range before.Batches() {
			next := after.Batch(old.ID)
			if next == nil || old.LandingAttempt.ID != "" && old.LandingAttempt.ID != next.LandingAttempt.ID {
				s = pj.RetireOwned(s, id, old.ID, nil)
			}
		}
	}
	// Close/head/lifecycle changes cannot resolve an uncertain fenced push.
	// After Landed, preserve it until the landed companion completes recovery.
	protected := func(change string) bool {
		for _, batch := range after.Batches() {
			if batch.Phase != queue.Landing {
				continue
			}
			for _, pr := range batch.PRs {
				current, _ := after.PRRevision(pr.PR)
				if string(current.ChangeID) == change {
					return true
				}
			}
		}
		return false
	}
	_, lifecycleChanged := event.(queue.LifecycleChanged)
	_, abandoned := event.(queue.LandingAbandoned)
	if (lifecycleChanged || abandoned) && after.LifecycleState() == "retired" {
		return pj.RetireOwned(s, id, "", func(r p.InventoryRequest) bool { return !protected(r.ChangeID) })
	}
	var pr int
	var superseded bool
	switch e := event.(type) {
	case queue.Dequeued:
		admitted := false
		for _, entry := range before.Entries {
			if entry.PR == e.PR && entry.AdmissionID == e.AdmissionID {
				admitted = true
			}
		}
		if !admitted {
			return s
		}
		pr = e.PR
	case queue.PRClosed:
		pr = e.PR
	case queue.PRObserved:
		pr, superseded = e.PR, true
	case queue.ChangeRevisionObserved:
		pr, superseded = e.PR, true
	default:
		return s
	}
	old, known := before.PRRevision(pr)
	if !known || old.ChangeID == "" {
		return s
	}
	current, _ := after.PRRevision(pr)
	if superseded && old == current {
		return s
	}
	if _, dequeue := event.(queue.Dequeued); dequeue {
		// Ignore stale dequeue acknowledgements, just as the queue fold does.
		for _, entry := range after.Entries {
			if entry.PR == pr {
				return s
			}
		}
	}
	return pj.RetireOwned(s, id, "", func(r p.InventoryRequest) bool {
		return !protected(r.ChangeID) && r.ChangeID == string(old.ChangeID) && (!superseded || r.Head != current.HeadSHA || r.Revision != strconv.FormatUint(uint64(current.Revision), 10))
	})
}
