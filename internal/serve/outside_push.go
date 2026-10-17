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
