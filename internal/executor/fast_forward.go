package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (x *Executor) buildFastForward(ctx context.Context, a queue.BuildTestRef, emit func(queue.Event) error) error {
	// Ancestry is immutable for pinned SHAs. BuildTestRef rechecks the selected
	// chain and remote head pins before publishing it.
	count := 1
	for count < len(a.PRs) {
		previous, next := a.PRs[count-1].HeadSHA, a.PRs[count].HeadSHA
		ancestor, err := x.Git.IsAncestor(ctx, x.Repo, previous, next)
		if err != nil {
			return x.fastForwardBuildError(a.BatchID, err, emit)
		}
		if previous == next || !ancestor {
			break
		}
		count++
	}
	prs := a.PRs[:count]
	if err := emit(queue.FastForwardBatchSelected{BatchID: a.BatchID, PRs: prs}); err != nil {
		return err
	}
	if validator, ok := x.Driver.(forge.LinearHistoryReader); ok {
		required, err := validator.LinearHistoryRequired(ctx, x.Repo, x.Base)
		if ctx.Err() != nil {
			return x.fastForwardBuildError(a.BatchID, ctx.Err(), emit)
		}
		if err == nil && required {
			for _, pr := range prs {
				hasMerges, err := x.Git.HasMergeCommits(ctx, x.Repo, a.BaseSHA, pr.HeadSHA)
				if err != nil {
					return x.fastForwardBuildError(a.BatchID, err, emit)
				}
				if hasMerges {
					return emit(queue.FastForwardMergeCommitsBlocked{BatchID: a.BatchID, PR: pr.PR})
				}
			}
		}
	}
	selected := a
	selected.PRs = prs
	heads, err := x.buildPRs(selected)
	if err != nil {
		return x.fastForwardBuildError(a.BatchID, err, emit)
	}
	sha, err := x.Git.BuildTestRef(ctx, x.Repo, forge.BuildRequest{Ref: a.Ref, BaseSHA: a.BaseSHA, PRs: heads, Method: forge.FastForward, BatchID: a.BatchID})
	if errors.Is(err, forge.ErrNotFastForward) {
		return emit(queue.FastForwardBlocked{BatchID: a.BatchID})
	}
	if err != nil {
		return x.fastForwardBuildError(a.BatchID, err, emit)
	}
	if err := x.Git.VerifyFastForward(ctx, x.Repo, a.BaseSHA, sha, prs[len(prs)-1].HeadSHA); err != nil {
		return x.fastForwardBuildError(a.BatchID, fmt.Errorf("invalid fast-forward marker: %w", err), emit)
	}
	return emit(queue.FastForwardRefBuilt{BatchID: a.BatchID, TestedSHA: sha, HeadSHA: prs[len(prs)-1].HeadSHA, At: x.now()})
}

func (x *Executor) fastForwardBuildError(batch string, err error, emit func(queue.Event) error) error {
	if emitErr := emit(queue.TestRefBuildFailed{BatchID: batch, At: x.now()}); emitErr != nil {
		return emitErr
	}
	return err
}

func dequeueSummary(reason queue.DequeueReason) string {
	if reason == queue.MergeCommitsNotAllowed {
		return "Base requires linear history; remove merge commits from the branch and queue it again"
	}
	if reason == queue.FastForwardRequired {
		return "Head cannot fast-forward onto the current base; update the branch and queue it again"
	}
	return ""
}
