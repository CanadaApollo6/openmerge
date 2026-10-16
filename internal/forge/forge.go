// Package forge defines the forge API and git transport needed by the queue.
package forge

import (
	"context"
	"errors"
	"time"
)

// RepoRef identifies a repository by its opaque forge-relative path, including
// nested groups (for example, group/subgroup/name). Forge-neutral code must
// not split it; concrete drivers interpret the path for their forge.
type RepoRef struct{ Path string }

// PullRequest is an exact-head observation used to evaluate queue admission.
type PullRequest struct {
	Number         int
	HeadSHA        string
	Base           string
	Draft          bool
	Author         string
	AuthorID       int64
	AuthorEmail    string // forge-provided commit email (GitHub numeric no-reply)
	Title, Body    string
	HeadBranch     string
	HeadInBaseRepo bool
	Approvals      []Approval
	Closed         bool
}

// PullRequestSummary is the lightweight reconciliation view of an open PR.
type PullRequestSummary struct {
	Number    int
	HeadSHA   string
	UpdatedAt time.Time
}

// CommitAuthor associates an immutable commit with its linked forge user.
// Empty Login and zero ID mean no linked user; git email is then the fallback.
type CommitAuthor struct {
	SHA, Login string
	ID         int64
}

// Comment is an issue-thread comment available for command discovery.
type Comment struct {
	ID        int64
	Author    string
	Body      string
	CreatedAt time.Time
}

// Approval pins a reviewer's approval and write access to the reviewed SHA.
// A driver that cannot attribute an approval to a commit may report it at the
// current head only when the forge is configured to reset approvals on push.
// Without that guarantee it must report no approvals (fail closed). Each driver
// must document whether it uses commit attribution or requires approval resets.
type Approval struct {
	User     string
	SHA      string
	CanWrite bool
}

// CheckState normalizes forge check outcomes.
type CheckState string

// Normalized check states.
const (
	Pending   CheckState = "pending"
	Success   CheckState = "success"
	Failure   CheckState = "failure"
	Cancelled CheckState = "cancelled"
	Skipped   CheckState = "skipped"
	Neutral   CheckState = "neutral"
)

// CheckRun is a named check on a particular commit. ID increases monotonically
// for each new run of a name from one Source; updates retain their ID.
// Sources identify independent producers whose IDs cannot be compared.
type CheckRun struct {
	ID     uint64
	Name   string
	Source string
	State  CheckState
}

// StatusState is the normalized queue lifecycle, independent of forge terminology.
type StatusState string

// Normalized queue lifecycle states.
const (
	Queued   StatusState = "queued"
	Testing  StatusState = "testing"
	Landing  StatusState = "landing"
	Merged   StatusState = "merged"
	Dequeued StatusState = "dequeued"
	Blocked  StatusState = "blocked"
	Paused   StatusState = "paused"
)

// Status is upserted as the driver's OpenMerge check on one PR head commit.
// Drivers map queued/testing/landing to pending, merged to success, dequeued to
// cancelled, blocked to failure, and paused to action-required. Commit-status
// APIs without cancelled/action-required use failure and describe the state. Reason is a stable machine-readable dequeue/block reason;
// Summary is human-readable. Neither is used as a forge check conclusion.
type Status struct {
	State   StatusState
	Reason  string
	Summary string
}

// ErrFileNotFound identifies an absent repository configuration file.
var ErrFileNotFound = errors.New("file does not exist at ref")

// Driver exposes forge API operations. Mutations must converge when retried.
type Driver interface {
	// ReadFile reads a file at an explicit branch or commit; missing is an error.
	ReadFile(ctx context.Context, repo RepoRef, ref, path string) ([]byte, error)
	PullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error)
	CommitAuthors(ctx context.Context, repo RepoRef, number int) ([]CommitAuthor, error)
	// HeadBranchForDeletion freshly checks a closed PR's pinned same-repository
	// head. Empty means keep it (base/default/protected, changed, or used as
	// an open PR's base or head). Errors fail closed. The caller must lease
	// HeadSHA on deletion.
	HeadBranchForDeletion(ctx context.Context, repo RepoRef, number int, headSHA, base string) (string, error)
	// ListOpenPullRequests filters by target branch; an empty base lists all.
	ListOpenPullRequests(ctx context.Context, repo RepoRef, base string) ([]PullRequestSummary, error)
	// ListComments returns comments created at or after since; zero means all comments.
	ListComments(ctx context.Context, repo RepoRef, pr int, since time.Time) ([]Comment, error)
	// CanWrite freshly authorizes a user. Conditional revalidation is allowed;
	// cached grants without a round trip to the forge are not.
	CanWrite(ctx context.Context, repo RepoRef, user string) (bool, error)
	CheckRuns(ctx context.Context, repo RepoRef, sha string) ([]CheckRun, error)
	// DeleteRef succeeds even when ref is already missing.
	DeleteRef(ctx context.Context, repo RepoRef, ref string) error
	ReportStatus(ctx context.Context, repo RepoRef, pr int, headSHA string, s Status) error
	// MarkLanded idempotently closes a PR with a link to landedSHA, or uses a
	// native mark-merged operation. It must not synthesize another git merge.
	MarkLanded(ctx context.Context, repo RepoRef, pr int, landedSHA string) error
	// Comment embeds key in a hidden marker and skips an existing keyed comment
	// only when that comment was authored by this driver's own identity.
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

// BuildPR pins an admitted PR head. A build rejects a head that no longer
// matches that PR rather than substituting a different commit.
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

// ErrConflict distinguishes a content conflict from an infrastructure failure.
var ErrConflict = errors.New("merge conflict")

// ErrLeaseRejected reports an atomic lease refusal. A rejected git push provides
// no actual ref values; callers must read the fence to settle the attempt.
var ErrLeaseRejected = errors.New("git lease rejected")

// LandRequest leases both refs in one atomic push. NextFenceID is land/<attempt>
// where the attempt ID is random and durably recorded before this call. The new
// fence's parent is ExpectedFence and its message is
// "openmerge fence land/<attempt> tested=<TestedSHA> nonce=<random-hex>".
// Each push mints a fresh nonce of at least 128 bits, even for the same attempt.
// PRs pin the landed heads;
// Land must reject the request without changing refs if any pin differs from
// that PR's actual head.
type LandRequest struct {
	Base, ExpectedBaseSHA, TestedSHA  string
	Fence, ExpectedFence, NextFenceID string
	PRs                               []BuildPR
}

// Git builds exact tested commits and performs forge-enforced fenced writes.
type Git interface {
	BuildTestRef(ctx context.Context, repo RepoRef, req BuildRequest) (testedSHA string, err error)
	Land(ctx context.Context, repo RepoRef, req LandRequest) (newFenceSHA string, err error)
	// EnsureFence creates the initial fence only if absent, safely under
	// concurrency, otherwise returning the existing SHA without changing it.
	EnsureFence(ctx context.Context, repo RepoRef, fenceRef string) (sha string, err error)
	// AdvanceFence leases expectedSHA and mints a child with nextID and a fresh nonce.
	AdvanceFence(ctx context.Context, repo RepoRef, fenceRef, expectedSHA, nextID string) (newSHA string, err error)
	// ReadFence reads identity and tested SHA from an exact single-line land or
	// abandon message, validating the 32-hex nonce and 40/64-hex tested SHA.
	// Unrecognized messages have empty identity fields; a missing ref is an error.
	ReadFence(ctx context.Context, repo RepoRef, fenceRef string) (sha, id, testedSHA string, err error)
	ReadRef(ctx context.Context, repo RepoRef, ref string) (sha string, err error)
	IsAncestor(ctx context.Context, repo RepoRef, ancestor, descendant string) (bool, error)
	// DeleteBranch deletes only expectedSHA; missing or changed refs are no-ops.
	DeleteBranch(ctx context.Context, repo RepoRef, branch, expectedSHA string) error
}
