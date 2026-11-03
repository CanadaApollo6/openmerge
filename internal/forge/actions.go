package forge

import (
	"context"
	"errors"
	"time"
)

// ErrActionsUnavailable means Actions metadata is absent or the installation
// lacks actions: read. It is diagnostic missing evidence, not a failed command.
var ErrActionsUnavailable = errors.New("actions unavailable: requires actions: read; run may be deleted or expired")

// ActionsReader exposes only CI metadata reads, one page per call. Implementations
// use 100 entries per page. NextPage is zero at the end; Total attests completeness.
// DefaultBranchActions returns only default-branch push runs in [since, until).
type ActionsReader interface {
	DefaultBranch(context.Context, RepoRef) (string, error)
	ActionsRun(context.Context, RepoRef, uint64) (ActionsRun, error)
	ActionsJobs(context.Context, RepoRef, uint64, uint64, int) (ActionsJobsPage, error)
	DefaultBranchActions(context.Context, RepoRef, string, time.Time, time.Time, int) (ActionsRunsPage, error)
	ReadFile(context.Context, RepoRef, string, string) ([]byte, error)
}

// ActionsJobsPage is one attempt-specific jobs page.
type ActionsJobsPage struct {
	Jobs     []ActionsJob
	Total    int
	NextPage int
}

// ActionsRunsPage is one default-branch workflow runs page.
type ActionsRunsPage struct {
	Runs     []ActionsRun
	Total    int
	NextPage int
}

// ActionsRun records one workflow attempt. JobsComplete attests that every page was
// recorded using the attempt-specific jobs endpoint.
type ActionsRun struct {
	ID           uint64        `json:"id"`
	WorkflowID   uint64        `json:"workflow_id"`
	Path         string        `json:"path"`
	Attempt      uint64        `json:"run_attempt"`
	Event        string        `json:"event"`
	HeadSHA      string        `json:"head_sha"`
	HeadBranch   string        `json:"head_branch"`
	CreatedAt    time.Time     `json:"created_at"`
	JobsComplete bool          `json:"jobs_complete"`
	Jobs         []ActionsJob  `json:"jobs"`
	Graph        *ActionsGraph `json:"graph,omitempty"`
}

// ActionsGraph is recorded from the workflow at HeadSHA. Keys and edges use expanded
// jobs-API display names (including matrix members). Required is recorded from
// the required-check policy at that revision, not inferred from job success.
type ActionsGraph struct {
	HeadSHA  string              `json:"head_sha"`
	Source   string              `json:"source"`
	Needs    map[string][]string `json:"needs"`
	Required []string            `json:"required"`
}

// ActionsJob retains Actions timing, outcome, and runner identity for one job.
type ActionsJob struct {
	ID          uint64        `json:"id"`
	Name        string        `json:"name"`
	Attempt     uint64        `json:"run_attempt"`
	CreatedAt   time.Time     `json:"created_at"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Conclusion  string        `json:"conclusion"`
	Labels      []string      `json:"labels"`
	Steps       []ActionsStep `json:"steps"`
}

// ActionsStep retains metadata from one jobs-API step.
type ActionsStep struct {
	Name        string    `json:"name"`
	Number      int       `json:"number"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Conclusion  string    `json:"conclusion"`
}
