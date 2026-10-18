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

// auditLandingRewrite uses fresh fence/base reads after verification failed. A
// successful fence proves the previous tip was landed; absence from today's base
// proves a rewrite removed it. This evidence cannot authorize settlement.
func (s *Server) auditLandingRewrite(ctx context.Context, w *worker) error {
	state, watermark, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	b := state.Active
	if b == nil || b.Phase != queue.Landing {
		return nil
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
	if tip == b.LandingCommit() {
		return nil
	}
	outside := outsidePush(ctx, w.Git, w.repo(), b.LandingCommit(), tip)
	if !outside.AncestryKnown || !outside.Rewrite {
		return nil
	}
	return s.store.RecordLandingRewrite(ctx, w.id, watermark, b.LandingAttempt.ID, outside)
}

// BaseMoved remains observable during an unsettled attempt. If that attempt is
// abandoned, its original base supplies the outside-push baseline even when the
// observed tip has already been applied. Commit the audit with abandonment so a
// restart cannot lose it. Successful settlement supplies its own landed baseline.
func (s *Server) emitAbandonedBase(ctx context.Context, w *worker, abandoned queue.LandingAbandoned, emit func(queue.Event) error) error {
	state, watermark, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	b := state.Active
	if b == nil || b.ID != abandoned.BatchID || state.BaseSHA == b.BaseSHA || state.BaseSHA == "" {
		return emit(abandoned)
	}
	outside := outsidePush(ctx, w.Git, w.repo(), b.BaseSHA, state.BaseSHA)
	return s.store.RecordAbandonedBaseMove(ctx, w.id, watermark, abandoned, outside)
}
