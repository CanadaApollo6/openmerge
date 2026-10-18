package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func outsidePush(ctx context.Context, git forge.Git, repo forge.RepoRef, previous, tip string) queue.OutsidePushRecorded {
	event := queue.OutsidePushRecorded{PreviousSHA: previous, NewSHA: tip, VerificationBeforeLanding: "none", At: time.Now().UTC()}
	ancestor, err := git.IsAncestor(ctx, repo, previous, tip)
	if err == nil {
		event.AncestryKnown, event.Rewrite = true, !ancestor
	}
	if reader, ok := git.(forge.CommitHistoryReader); ok {
		commits, truncated, err := reader.CommitsBetween(ctx, repo, previous, tip, queue.OutsidePushCommitLimit)
		if err == nil {
			event.HistoryKnown, event.Commits, event.Truncated = true, commits, truncated
		}
	}
	return event
}

// auditBaseMove runs after configuration publication and block-clearing. Audit
// errors are operational diagnostics only; they never become reconcile errors.
func (s *Server) auditBaseMove(ctx context.Context, w *worker, basis queue.State, moveID int64, tip string) {
	if basis.Active != nil && basis.Active.Phase == queue.Landing {
		return
	}
	outside := queue.OutsidePushRecorded{}
	if basis.BaseSHA != "" && basis.BaseSHA != tip {
		outside = outsidePush(ctx, w.Git, w.repo(), basis.BaseSHA, tip)
	}
	s.auditError(w, s.store.RecordBaseMove(ctx, w.id, basis, moveID, outside))
}

func (s *Server) auditError(w *worker, err error) {
	if err != nil {
		s.options.Logger.Warn("outside push audit", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
	}
}

// auditOutsidePushes is a separate, best-effort step after the cycle's queue
// work, including settlement. Durable receipts make retries/restarts harmless.
func (s *Server) auditOutsidePushes(ctx context.Context, w *worker) {
	moves, err := s.store.AbandonedBaseMoves(ctx, w.id)
	if err != nil {
		s.auditError(w, err)
		return
	}
	for _, move := range moves {
		// Avoid repeating git reads for receipts already committed by any server.
		seen, err := s.store.OutsidePushRecorded(ctx, w.id, move.ID)
		if err != nil {
			s.auditError(w, err)
			continue
		}
		if seen {
			continue
		}
		outside := outsidePush(ctx, w.Git, w.repo(), move.PreviousSHA, move.NewSHA)
		s.auditError(w, s.store.RecordAbandonedBaseMove(ctx, w.id, move, outside))
	}
	s.auditError(w, s.auditLandingRewrite(ctx, w))
}

// A successful fence proves the landing happened even if ancestry was pruned.
// Neither missing history nor the audit itself can authorize settlement.
func (s *Server) auditLandingRewrite(ctx context.Context, w *worker) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	b := state.Active
	if b == nil || b.Phase != queue.Landing || !state.SourceBlocked(queue.LandingNotOnBase) {
		return nil
	}
	previous, receiptID, err := s.store.LandingRewriteTip(ctx, w.id, b.LandingAttempt.ID)
	if err != nil {
		return err
	}
	_, id, tested, err := w.Git.ReadFence(ctx, w.repo(), "openmerge/fence/"+w.Key.BaseBranch)
	if err != nil {
		return err
	}
	if id != "land/"+b.LandingAttempt.ID || tested != b.TestedSHA {
		return nil
	}
	tip, err := w.Git.ReadRef(ctx, w.repo(), w.Key.BaseBranch)
	if err != nil {
		return err
	}
	first := previous == ""
	if first {
		previous = b.LandingCommit()
	}
	if tip == previous {
		return nil
	}
	outside := outsidePush(ctx, w.Git, w.repo(), previous, tip)
	if first && outside.AncestryKnown && !outside.Rewrite {
		return nil
	}
	return s.store.RecordLandingRewrite(ctx, w.id, state, receiptID, outside)
}
