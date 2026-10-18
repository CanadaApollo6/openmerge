package fake

import (
	"context"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.NativeReviewReader = (*Forge)(nil)

type nativeReviewKey struct {
	repo   forge.RepoRef
	number int
}

func cloneNativeReviews(value forge.NativeReviews) forge.NativeReviews {
	value.Reviews = slices.Clone(value.Reviews)
	value.Comments = slices.Clone(value.Comments)
	for i := range value.Reviews {
		if value.Reviews[i].Dismissal != nil {
			dismissal := *value.Reviews[i].Dismissal
			value.Reviews[i].Dismissal = &dismissal
		}
	}
	return value
}

// SetNativeReviews configures source observations independently of legacy
// approvals/comments and identity resolution. An unconfigured read is unknown.
// Use FailNext("NativeReviews", forge.ErrNativeReviewsIncomplete) for truncation.
func (f *Forge) SetNativeReviews(repo forge.RepoRef, number int, value forge.NativeReviews) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nativeReviews == nil {
		f.nativeReviews = map[nativeReviewKey]forge.NativeReviews{}
	}
	f.nativeReviews[nativeReviewKey{repo, number}] = cloneNativeReviews(value)
}

// NativeReviews returns an isolated observation or a configured read failure.
func (f *Forge) NativeReviews(ctx context.Context, repo forge.RepoRef, number int) (forge.NativeReviews, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "NativeReviews"); err != nil {
		return forge.NativeReviews{}, err
	}
	value, ok := f.nativeReviews[nativeReviewKey{repo, number}]
	if !ok {
		return forge.NativeReviews{}, forge.ErrNativeReviewsUnavailable
	}
	return cloneNativeReviews(value), nil
}
