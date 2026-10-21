package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func outsidePush(ctx context.Context, history forge.OutsidePushHistory, repo forge.RepoRef, previous, tip string) queue.OutsidePushRecorded {
	event := queue.OutsidePushRecorded{PreviousSHA: previous, NewSHA: tip, VerificationBeforeLanding: "none", At: time.Now().UTC()}
	if history == nil {
		return event
	}
	ancestor, err := history.IsAncestor(ctx, repo, previous, tip)
	if err == nil {
		event.AncestryKnown, event.Rewrite = true, !ancestor
	}
	commits, truncated, err := history.CommitsBetween(ctx, repo, previous, tip, queue.OutsidePushCommitLimit)
	if err == nil {
		event.HistoryKnown, event.Commits, event.Truncated = true, commits, truncated
	}
	return event
}

// outsidePushLoop owns all audit I/O. It never takes a worker mutex or execution
// slot, and holds no DB connection while enriching immutable journal candidates.
func (s *Server) outsidePushLoop(ctx context.Context) {
	for {
		for _, w := range s.workers() {
			auditCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
			err := s.auditOutsidePushes(auditCtx, w.id, w.Key)
			cancel()
			// Warning rendering has its own read deadline and never waits for worker
			// ownership. Acknowledgements are reflected on the next audit pass.
			warningErr := s.refreshOutsideWarnings(ctx, w)
			if warningErr != nil && ctx.Err() == nil {
				s.options.Logger.Warn("outside push warnings", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", warningErr)
			}
			if err != nil && ctx.Err() == nil {
				s.options.Logger.Warn("outside push audit", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
			}
		}
		timer := time.NewTimer(s.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// refreshOutsideWarnings publishes complete ledger snapshots. Failed reads
// preserve the last complete counts with an explicit queue-local error. Warning
// availability does not change health; its DB probe reports database outages.
func (s *Server) refreshOutsideWarnings(ctx context.Context, w *worker) error {
	if !w.hasAuthorityPin.Load() {
		return nil
	}
	summary, err := s.store.ReadOutsidePushWarnings(ctx, w.id)
	w.outsideWarnings.Store(&summary)
	return err
}

func (s *Server) auditOutsidePushes(ctx context.Context, id store.QueueID, key store.QueueKey) error {
	audit, err := s.store.ReadOutsidePushAudit(ctx, id)
	if err != nil {
		return err
	}
	var history forge.OutsidePushHistory
	if len(audit.Moves) > 0 && s.options.OutsidePushHistory != nil {
		history, err = s.options.OutsidePushHistory(ctx, key)
		if err != nil {
			s.options.Logger.Warn("outside push history unavailable", "repository", key.Repository, "error", err)
			history = nil
		}
	}
	events := make([]queue.OutsidePushRecorded, len(audit.Moves))
	for i, move := range audit.Moves {
		events[i] = outsidePush(ctx, history, forge.RepoRef{Path: key.Repository}, move.PreviousSHA, move.NewSHA)
		events[i].At = move.At
		events[i].PossibleStaleObservation = move.PossibleStaleObservation
	}
	// Enrichment timing out means unknown evidence, not a skipped transition.
	// Persistence has its own short deadline, with no git work under the lock.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = s.store.CommitOutsidePushAudit(commitCtx, id, audit, events)
	return err
}

func (s *Server) initializeOutsideWarnings(ctx context.Context, w *worker) {
	if err := s.refreshOutsideWarnings(ctx, w); err != nil {
		s.options.Logger.Warn("outside push warnings; preserving queue registration", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
	}
}
