package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Approval reconciliation can resolve many identities per PR. Publish each
// completed source read, so one large set does not look like one stalled unit.
type healthIdentityReader struct {
	forge.IdentityReader
	w *worker
}

func (r healthIdentityReader) ResolveUser(ctx context.Context, name string) (forge.UserIdentity, error) {
	value, err := r.IdentityReader.ResolveUser(ctx, name)
	r.w.publishProgress(time.Now())
	return value, err
}
func (r healthIdentityReader) ResolveTeam(ctx context.Context, name string) (forge.TeamIdentity, error) {
	value, err := r.IdentityReader.ResolveTeam(ctx, name)
	r.w.publishProgress(time.Now())
	return value, err
}
func (r healthIdentityReader) TeamMembers(ctx context.Context, team forge.TeamIdentity) ([]forge.UserIdentity, error) {
	value, err := r.IdentityReader.TeamMembers(ctx, team)
	r.w.publishProgress(time.Now())
	return value, err
}
func (r healthIdentityReader) WriteGrant(ctx context.Context, repo forge.RepoRef, user forge.UserIdentity) (forge.WriteGrant, error) {
	value, err := r.IdentityReader.WriteGrant(ctx, repo, user)
	r.w.publishProgress(time.Now())
	return value, err
}

type healthReviewReader struct {
	forge.NativeReviewReader
	w *worker
}

func (r healthReviewReader) NativeReviews(ctx context.Context, repo forge.RepoRef, number int) (forge.NativeReviews, error) {
	value, err := r.NativeReviewReader.NativeReviews(ctx, repo, number)
	r.w.publishProgress(time.Now())
	return value, err
}
