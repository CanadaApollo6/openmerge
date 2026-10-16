// Package fake implements a concurrent in-memory forge and a tiny commit graph.
// Commit content is the sorted set of PR numbers present in that commit.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Commit is an immutable snapshot of a fake git commit.
type Commit struct {
	SHA     string
	Parents []string
	PRs     []int
	Message string
}

type repository struct {
	refs     map[string]string
	commits  map[string]Commit
	prs      map[int]forge.PullRequest
	checks   map[string][]forge.CheckRun
	statuses map[int]map[string]forge.Status
	comments map[int]map[string]string
}

// Forge implements both forge interfaces. Use New to construct it. All helpers
// are synchronized too; callbacks run without the lock and may call the forge.
type Forge struct {
	mu        sync.Mutex
	repos     map[forge.RepoRef]*repository
	conflicts map[[2]int]bool
	failures  map[string][]error
	onTestRef func(string, []int)
}

var (
	_ forge.Driver = (*Forge)(nil)
	_ forge.Git    = (*Forge)(nil)
)

// New returns an empty forge, with no repositories or refs.
func New() *Forge {
	return &Forge{
		repos: make(map[forge.RepoRef]*repository), conflicts: make(map[[2]int]bool),
		failures: make(map[string][]error),
	}
}

func (f *Forge) repo(repo forge.RepoRef) *repository {
	r := f.repos[repo]
	if r == nil {
		r = &repository{
			refs: make(map[string]string), commits: make(map[string]Commit),
			prs: make(map[int]forge.PullRequest), checks: make(map[string][]forge.CheckRun),
			statuses: make(map[int]map[string]forge.Status), comments: make(map[int]map[string]string),
		}
		f.repos[repo] = r
	}
	return r
}

// Init creates missing base and fence roots and returns their current SHAs.
func (f *Forge) Init(repo forge.RepoRef, base, fenceRef string) (baseSHA, fenceSHA string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	if r.refs[base] == "" {
		r.refs[base] = r.commit(nil, nil, "initial base")
	}
	if r.refs[fenceRef] == "" {
		r.refs[fenceRef] = r.commit(nil, nil, "initial fence")
	}
	return r.refs[base], r.refs[fenceRef]
}

func (r *repository) commit(parents []string, prs []int, message string) string {
	prs = slices.Clone(prs)
	slices.Sort(prs)
	prs = slices.Compact(prs)
	c := Commit{Parents: slices.Clone(parents), PRs: prs, Message: message}
	// These fields contain no values that JSON cannot encode.
	data, _ := json.Marshal(c)
	c.SHA = fmt.Sprintf("%x", sha256.Sum256(data))
	r.commits[c.SHA] = c
	return c.SHA
}

func (f *Forge) failure(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pending := f.failures[method]; len(pending) > 0 {
		err := pending[0]
		f.failures[method] = pending[1:]
		return err
	}
	return nil
}

// FailNext queues a pre-operation failure, keyed by the interface method name.
func (f *Forge) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[method] = append(f.failures[method], err)
}

// SetConflict makes a pair of PRs incompatible, in either order or against base.
func (f *Forge) SetConflict(prA, prB int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conflicts[pair(prA, prB)] = true
}

func pair(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

// OnTestRef installs a callback for newly pushed test refs. prs contains only
// the request's PRs, in request order; the commit contains inherited content too.
func (f *Forge) OnTestRef(fn func(sha string, prs []int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onTestRef = fn
}

// BuildTestRef composes merge commits, one squash, or rebased PR commits. Each
// shape's top commit contains the batch ID. Failed builds never move the ref.
func (f *Forge) BuildTestRef(ctx context.Context, repo forge.RepoRef, req forge.BuildRequest) (string, error) {
	f.mu.Lock()
	sha, prs, changed, err := f.build(ctx, f.repo(repo), req)
	hook := f.onTestRef
	f.mu.Unlock()
	if err == nil && changed && hook != nil {
		hook(sha, prs)
	}
	return sha, err
}

func (f *Forge) build(ctx context.Context, r *repository, req forge.BuildRequest) (string, []int, bool, error) {
	if err := f.failure(ctx, "BuildTestRef"); err != nil {
		return "", nil, false, err
	}
	base, ok := r.commits[req.BaseSHA]
	if !ok {
		return "", nil, false, fmt.Errorf("unknown base commit %q", req.BaseSHA)
	}
	if req.Ref == "" || req.BatchID == "" || len(req.PRs) == 0 {
		return "", nil, false, errors.New("test ref, batch ID, and PRs are required")
	}
	if req.Method != forge.Merge && req.Method != forge.Squash && req.Method != forge.Rebase {
		return "", nil, false, fmt.Errorf("unsupported merge method %q", req.Method)
	}
	content := slices.Clone(base.PRs)
	prs := make([]int, 0, len(req.PRs))
	for _, pr := range req.PRs {
		if _, ok := r.commits[pr.HeadSHA]; !ok {
			return "", nil, false, fmt.Errorf("unknown PR head %q", pr.HeadSHA)
		}
		for _, present := range content {
			if f.conflicts[pair(pr.Number, present)] {
				return "", nil, false, fmt.Errorf("PRs %d and %d: %w", pr.Number, present, forge.ErrConflict)
			}
		}
		content = append(content, pr.Number)
		prs = append(prs, pr.Number)
	}
	top := req.BaseSHA
	if req.Method == forge.Squash {
		heads := make([]string, 0, len(req.PRs))
		for _, pr := range req.PRs {
			heads = append(heads, pr.HeadSHA)
		}
		top = r.commit([]string{top}, content, fmt.Sprintf("squash %v\nOpenMerge-Batch: %s", heads, req.BatchID))
	} else {
		content = slices.Clone(base.PRs)
		for i, pr := range req.PRs {
			content = append(content, pr.Number)
			parents := []string{top}
			if req.Method == forge.Merge {
				parents = append(parents, pr.HeadSHA)
			}
			message := fmt.Sprintf("%s PR %d %s", req.Method, pr.Number, pr.HeadSHA)
			if i == len(req.PRs)-1 {
				message += "\nOpenMerge-Batch: " + req.BatchID
			}
			top = r.commit(parents, content, message)
		}
	}
	changed := r.refs[req.Ref] != top
	r.refs[req.Ref] = top
	return top, prs, changed, nil
}

// Land checks both leases and updates both refs under one lock.
func (f *Forge) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "Land"); err != nil {
		return "", err
	}
	r := f.repo(repo)
	if r.refs[req.Base] != req.ExpectedBaseSHA || r.refs[req.Fence] != req.ExpectedFence {
		return "", &forge.LandRejectedError{ActualBaseSHA: r.refs[req.Base], ActualFence: r.refs[req.Fence]}
	}
	if req.Base == req.Fence {
		return "", errors.New("base and fence must be distinct refs")
	}
	if _, ok := r.commits[req.TestedSHA]; !ok {
		return "", fmt.Errorf("unknown tested commit %q", req.TestedSHA)
	}
	next, err := r.fence(req.ExpectedFence, req.NextFenceID)
	if err != nil {
		return "", err
	}
	r.refs[req.Base], r.refs[req.Fence] = req.TestedSHA, next
	return next, nil
}

func (r *repository) fence(expected, id string) (string, error) {
	if id == "" {
		return "", errors.New("next fence ID is required")
	}
	if _, ok := r.commits[expected]; !ok {
		return "", fmt.Errorf("unknown fence commit %q", expected)
	}
	return r.commit([]string{expected}, nil, "openmerge fence "+id), nil
}

// AdvanceFence advances only when the current fence matches expectedSHA.
func (f *Forge) AdvanceFence(ctx context.Context, repo forge.RepoRef, fenceRef, expectedSHA, nextID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "AdvanceFence"); err != nil {
		return "", err
	}
	r := f.repo(repo)
	if r.refs[fenceRef] != expectedSHA {
		return "", &forge.LandRejectedError{ActualFence: r.refs[fenceRef]}
	}
	next, err := r.fence(expectedSHA, nextID)
	if err != nil {
		return "", err
	}
	r.refs[fenceRef] = next
	return next, nil
}

// ReadFence returns the current fence SHA and the ID recorded in its message.
func (f *Forge) ReadFence(ctx context.Context, repo forge.RepoRef, fenceRef string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReadFence"); err != nil {
		return "", "", err
	}
	r := f.repo(repo)
	sha, ok := r.refs[fenceRef]
	if !ok {
		return "", "", fmt.Errorf("unknown ref %q", fenceRef)
	}
	id, ok := strings.CutPrefix(r.commits[sha].Message, "openmerge fence ")
	if !ok {
		id = ""
	}
	return sha, id, nil
}

// ReadRef returns an error when the ref does not exist.
func (f *Forge) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReadRef"); err != nil {
		return "", err
	}
	sha, ok := f.repo(repo).refs[ref]
	if !ok {
		return "", fmt.Errorf("unknown ref %q", ref)
	}
	return sha, nil
}

// DeleteRef is idempotent, including when the repository has no such ref.
func (f *Forge) DeleteRef(ctx context.Context, repo forge.RepoRef, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "DeleteRef"); err != nil {
		return err
	}
	delete(f.repo(repo).refs, ref)
	return nil
}

// PullRequest returns a snapshot; approvals may refer to an earlier head.
func (f *Forge) PullRequest(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "PullRequest"); err != nil {
		return forge.PullRequest{}, err
	}
	pr, ok := f.repo(repo).prs[number]
	if !ok {
		return forge.PullRequest{}, fmt.Errorf("unknown PR %d", number)
	}
	pr.Approvals = slices.Clone(pr.Approvals)
	return pr, nil
}

// CheckRuns returns a snapshot. Missing checks are represented by an empty list.
func (f *Forge) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CheckRuns"); err != nil {
		return nil, err
	}
	return slices.Clone(f.repo(repo).checks[sha]), nil
}

// ReportStatus upserts one context on one PR, preserving other contexts.
func (f *Forge) ReportStatus(ctx context.Context, repo forge.RepoRef, pr int, status forge.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReportStatus"); err != nil {
		return err
	}
	r := f.repo(repo)
	if r.statuses[pr] == nil {
		r.statuses[pr] = make(map[string]forge.Status)
	}
	r.statuses[pr][status.Context] = status
	return nil
}

// Comment stores the hidden key marker atomically with the comment body.
func (f *Forge) Comment(ctx context.Context, repo forge.RepoRef, pr int, key, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "Comment"); err != nil {
		return err
	}
	r := f.repo(repo)
	if r.comments[pr] == nil {
		r.comments[pr] = make(map[string]string)
	}
	if _, exists := r.comments[pr][key]; !exists {
		r.comments[pr][key] = body + "\n<!-- openmerge:" + key + " -->"
	}
	return nil
}
