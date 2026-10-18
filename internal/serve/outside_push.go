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
