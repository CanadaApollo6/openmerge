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
	heads := make([]forge.BuildPR, len(prs))
	for i, pr := range prs {
		heads[i] = forge.BuildPR{Number: pr.PR, HeadSHA: pr.HeadSHA}
	}
	sha, err := x.Git.BuildTestRef(ctx, x.Repo, forge.BuildRequest{Ref: a.Ref, BaseSHA: a.BaseSHA, PRs: heads, Method: forge.FastForward, BatchID: a.BatchID})
	if errors.Is(err, forge.ErrNotFastForward) {
		return emit(queue.FastForwardBlocked{BatchID: a.BatchID})
	}
	if err != nil {
		return x.fastForwardBuildError(a.BatchID, err, emit)
	}
	if sha != prs[len(prs)-1].HeadSHA {
		return x.fastForwardBuildError(a.BatchID, fmt.Errorf("fast-forward returned %s instead of pinned head %s", sha, prs[len(prs)-1].HeadSHA), emit)
	}
	return emit(queue.FastForwardRefBuilt{BatchID: a.BatchID, TestedSHA: sha, At: x.now()})
}

func (x *Executor) fastForwardBuildError(batch string, err error, emit func(queue.Event) error) error {
	if emitErr := emit(queue.TestRefBuildFailed{BatchID: batch, At: x.now()}); emitErr != nil {
		return emitErr
	}
	return err
}

func dequeueSummary(reason queue.DequeueReason) string {
	if reason == queue.FastForwardRequired {
		return "Head cannot fast-forward onto the current base; update the branch and queue it again"
	}
	return ""
}
