package gitops

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// DeleteBranch leases the exact landed head so cleanup cannot remove newer work.
// Missing or changed branches converge without deleting their replacements.
func (r *Repo) DeleteBranch(ctx context.Context, repo forge.RepoRef, branch, expectedSHA string, opts forge.PushOptions) error {
	if err := forge.ValidateDeleteBranch(branch, expectedSHA); err != nil {
		return err
	}
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return err
	}
	defer unlock()
	ref, err := s.branch(ctx, branch)
	if err != nil {
		return err
	}
	values, err := s.read(ctx, ref)
	if err != nil {
		return err
	}
	if values[ref] != expectedSHA {
		return nil
	}
	if err := opts.CheckDispatch(ctx, repo, forge.PushDeleteBranch, []forge.PushRefIntent{{Ref: ref, OldSHA: expectedSHA, Delete: true}}); err != nil {
		return err
	}
	if err := s.resolveRemote(ctx, repo); err != nil {
		return err
	}
	if err := s.validatePush(ctx, repo, opts, forge.PushDeleteBranch, forge.PushRef{Ref: ref, OldSHA: expectedSHA}); err != nil {
		return err
	}
	err = s.pushLease(ctx, "--force-with-lease="+ref+":"+expectedSHA, "--", s.remote, ":"+ref)
	if errors.Is(err, forge.ErrLeaseRejected) {
		return nil
	}
	return err
}
