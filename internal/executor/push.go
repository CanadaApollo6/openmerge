package executor

import (
	"context"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// PushOptionsLoader is supplied by serve after resolving and durably pinning
// governing policy. It is transient, independent of deployed action payloads.
type PushOptionsLoader func(context.Context, forge.PushOperation, []forge.PushRefIntent) (forge.PushOptions, error)

func (x *Executor) pushOptions(ctx context.Context, operation forge.PushOperation, refs []forge.PushRefIntent) (forge.PushOptions, error) {
	if x.PreparePush == nil {
		return forge.PushOptions{}, nil
	}
	return x.PreparePush(ctx, operation, refs)
}

func (x *Executor) buildWithValidation(ctx context.Context, req forge.BuildRequest) (string, error) {
	if x.PreparePush == nil {
		return x.Git.BuildTestRef(ctx, x.Repo, req, forge.PushOptions{})
	}
	ref, err := forge.BranchRef(req.Ref)
	if err != nil {
		return "", err
	}
	opts, err := x.pushOptions(ctx, forge.PushBuild, []forge.PushRefIntent{{Ref: ref}})
	if err != nil {
		return "", err
	}
	return x.Git.BuildTestRef(ctx, x.Repo, req, opts)
}

func (x *Executor) landWithValidation(ctx context.Context, req forge.LandRequest) (string, error) {
	if x.PreparePush == nil {
		return x.Git.Land(ctx, x.Repo, req, forge.PushOptions{})
	}
	base, err := forge.BranchRef(req.Base)
	if err != nil {
		return "", err
	}
	fence, err := forge.BranchRef(req.Fence)
	if err != nil {
		return "", err
	}
	landed := req.TestedSHA
	if req.FastForwardHeadSHA != "" {
		landed = req.FastForwardHeadSHA
	}
	opts, err := x.pushOptions(ctx, forge.PushLand, []forge.PushRefIntent{{Ref: base, OldSHA: req.ExpectedBaseSHA, NewSHA: landed}, {Ref: fence, OldSHA: req.ExpectedFence}})
	if err != nil {
		return "", err
	}
	return x.Git.Land(ctx, x.Repo, req, opts)
}

func (x *Executor) advanceWithValidation(ctx context.Context, ref, old, id string) (string, error) {
	if x.PreparePush == nil {
		return x.Git.AdvanceFence(ctx, x.Repo, ref, old, id, forge.PushOptions{})
	}
	full, err := forge.BranchRef(ref)
	if err != nil {
		return "", err
	}
	opts, err := x.pushOptions(ctx, forge.PushAdvanceFence, []forge.PushRefIntent{{Ref: full, OldSHA: old}})
	if err != nil {
		return "", err
	}
	return x.Git.AdvanceFence(ctx, x.Repo, ref, old, id, opts)
}

func (x *Executor) deleteWithValidation(ctx context.Context, branch, old string) error {
	if x.PreparePush == nil {
		return x.Git.DeleteBranch(ctx, x.Repo, branch, old, forge.PushOptions{})
	}
	full, err := forge.BranchRef(branch)
	if err != nil {
		return err
	}
	opts, err := x.pushOptions(ctx, forge.PushDeleteBranch, []forge.PushRefIntent{{Ref: full, OldSHA: old, Delete: true}})
	if err != nil {
		return err
	}
	return x.Git.DeleteBranch(ctx, x.Repo, branch, old, opts)
}
