// Package forge defines the forge API and git transport needed by the queue.
package forge

import (
	"context"
	"errors"
	"fmt"
)

// RepoRef identifies a repository within a forge installation.
type RepoRef struct{ Owner, Name string }

// PullRequest is an exact-head observation used to evaluate queue admission.
type PullRequest struct {
	Number         int
	HeadSHA        string
	HeadInBaseRepo bool
	Approvals      []Approval
	Closed         bool
}

// Approval pins a reviewer's approval and write access to the reviewed SHA.
type Approval struct {
	User     string
	SHA      string
	CanWrite bool
}

// CheckState normalizes forge check outcomes.
type CheckState string

// Normalized check states.
const (
	Pending CheckState = "pending"
	Success CheckState = "success"
	Failure CheckState = "failure"
)

// CheckRun is a named check on a particular commit.
type CheckRun struct {
	Name  string
	State CheckState
}

// Status is upserted by Context for one PR. State and Detail are queue outcomes.
type Status struct{ Context, State, Detail string }

// Driver exposes forge API operations. Mutations must converge when retried.
type Driver interface {
	PullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error)
	CheckRuns(ctx context.Context, repo RepoRef, sha string) ([]CheckRun, error)
	// DeleteRef succeeds even when ref is already missing.
	DeleteRef(ctx context.Context, repo RepoRef, ref string) error
	ReportStatus(ctx context.Context, repo RepoRef, pr int, status Status) error
	// Comment embeds key in a hidden marker and skips an existing keyed comment.
	Comment(ctx context.Context, repo RepoRef, pr int, key, body string) error
}

// MergeMethod describes the git shape of a tested commit.
type MergeMethod string

// Supported tested-commit shapes.
const (
	Merge  MergeMethod = "merge"
	Squash MergeMethod = "squash"
	Rebase MergeMethod = "rebase"
)

// BuildPR pins one PR head, independently of later pushes.
type BuildPR struct {
	Number  int
	HeadSHA string
}

// BuildRequest composes pinned heads on BaseSHA and pushes Ref. The top commit
// must carry BatchID, making the tested SHA unique across batches.
type BuildRequest struct {
	Ref, BaseSHA string
	PRs          []BuildPR
	Method       MergeMethod
	BatchID      string
}

// ErrConflict distinguishes a merge conflict from infrastructure failures.
var ErrConflict = errors.New("merge conflict")

// LandRequest leases both refs in one atomic push. NextFenceID is a unique opaque
// ID, not a SHA: git creates a fence commit with ExpectedFence as its parent and
// message "openmerge fence <id>" and returns its SHA. Each attempt and purpose
// must have a distinct ID; retries of that operation use the same ID.
type LandRequest struct {
	Base, ExpectedBaseSHA, TestedSHA  string
	Fence, ExpectedFence, NextFenceID string
}

// LandRejectedError reports an atomic lease refusal; neither ref was changed.
// AdvanceFence only fills ActualFence, since it does not read a base branch.
type LandRejectedError struct{ ActualBaseSHA, ActualFence string }

func (e *LandRejectedError) Error() string {
	return fmt.Sprintf("git lease rejected (base %q, fence %q)", e.ActualBaseSHA, e.ActualFence)
}

// Git builds exact tested commits and performs forge-enforced fenced writes.
type Git interface {
	BuildTestRef(ctx context.Context, repo RepoRef, req BuildRequest) (testedSHA string, err error)
	Land(ctx context.Context, repo RepoRef, req LandRequest) (newFenceSHA string, err error)
	// AdvanceFence leases expectedSHA and mints a child commit using nextID.
	AdvanceFence(ctx context.Context, repo RepoRef, fenceRef, expectedSHA, nextID string) (newSHA string, err error)
	// ReadFence reads one commit's SHA and ID from its "openmerge fence <id>"
	// message. A non-OpenMerge commit has an empty ID; a missing ref is an error.
	ReadFence(ctx context.Context, repo RepoRef, fenceRef string) (sha, id string, err error)
	ReadRef(ctx context.Context, repo RepoRef, ref string) (sha string, err error)
}
