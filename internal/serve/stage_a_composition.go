package serve

import (
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func readRebaseComposition(ctx context.Context, w *worker, build queue.BuildTestRef, limit int) (queue.RebaseCompositionObserved, error) {
	observed := queue.RebaseCompositionObserved{BatchID: build.BatchID, PRs: build.PRs, Limit: limit}
	reader, ok := w.Git.(forge.CommitHistoryReader)
	if !ok {
		return observed, store.LandingDecisionRequiredError{}
	}
	for _, pin := range build.PRs {
		source, truncated, err := reader.CommitsBetween(ctx, w.repo(), build.BaseSHA, pin.HeadSHA, min(limit, forge.MaxCommitHistoryCommits))
		if err != nil {
			return observed, err
		}
		n := len(source)
		if truncated {
			n = limit + 1
		}
		if n == 0 {
			return observed, fmt.Errorf("empty rebase source history for PR #%d; use squash or merge", pin.PR)
		}
		observed.Counts = append(observed.Counts, n)
	}
	return observed, observed.Validate()
}
