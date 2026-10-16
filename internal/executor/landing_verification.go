package executor

import (
	"context"
	"fmt"
)

// ErrLandingNotOnBase reports a tested commit absent from the freshly read base.
// AttemptID is empty for MarkLanded actions, which do not carry an attempt ID.
type ErrLandingNotOnBase struct {
	TestedSHA, BaseTip, AttemptID string
}

func (e *ErrLandingNotOnBase) Error() string {
	return fmt.Sprintf("landing attempt %q: tested SHA %q is not on base tip %q", e.AttemptID, e.TestedSHA, e.BaseTip)
}

func (x *Executor) verifyLanding(ctx context.Context, tested, attemptID string) error {
	tip, err := x.Git.ReadRef(ctx, x.Repo, x.Base)
	if err != nil {
		return err
	}
	if tip == tested {
		return nil
	}
	contains, err := x.Git.IsAncestor(ctx, x.Repo, tested, tip)
	if err != nil {
		return err
	}
	if !contains {
		return &ErrLandingNotOnBase{TestedSHA: tested, BaseTip: tip, AttemptID: attemptID}
	}
	return nil
}
