package fake

import (
	"fmt"
	"maps"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// OpenPR creates a PR head based on baseSHA. PushPR creates subsequent heads.
func (f *Forge) OpenPR(repo forge.RepoRef, number int, baseSHA string, headInBaseRepo bool) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	base, ok := r.commits[baseSHA]
	if !ok {
		return forge.PullRequest{}, fmt.Errorf("unknown base commit %q", baseSHA)
	}
	if _, exists := r.prs[number]; exists {
		return forge.PullRequest{}, fmt.Errorf("PR %d already exists", number)
	}
	head := r.commit([]string{baseSHA}, append(slices.Clone(base.PRs), number), fmt.Sprintf("PR %d", number))
	pr := forge.PullRequest{Number: number, HeadSHA: head, HeadInBaseRepo: headInBaseRepo}
	r.prs[number] = pr
	return pr, nil
}

// PushPR adds a commit to a PR without re-approving the new head.
func (f *Forge) PushPR(repo forge.RepoRef, number int, message string) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return forge.PullRequest{}, fmt.Errorf("unknown PR %d", number)
	}
	pr.HeadSHA = r.commit([]string{pr.HeadSHA}, r.commits[pr.HeadSHA].PRs, message)
	r.prs[number] = pr
	pr.Approvals = slices.Clone(pr.Approvals)
	return pr, nil
}

// ClosePR marks a PR closed, preserving its head and reviews.
func (f *Forge) ClosePR(repo forge.RepoRef, number int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return fmt.Errorf("unknown PR %d", number)
	}
	pr.Closed = true
	r.prs[number] = pr
	return nil
}

// Approve upserts a reviewer's exact-SHA approval.
func (f *Forge) Approve(repo forge.RepoRef, number int, approval forge.Approval) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return fmt.Errorf("unknown PR %d", number)
	}
	pr.Approvals = slices.DeleteFunc(pr.Approvals, func(a forge.Approval) bool { return a.User == approval.User })
	pr.Approvals = append(pr.Approvals, approval)
	r.prs[number] = pr
	return nil
}

// SetCheckRuns replaces a commit's check snapshot, copying the caller's slice.
func (f *Forge) SetCheckRuns(repo forge.RepoRef, sha string, checks []forge.CheckRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(repo).checks[sha] = slices.Clone(checks)
}

// SetRef simulates external branch updates, including rewrites and rollbacks.
func (f *Forge) SetRef(repo forge.RepoRef, ref, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	if _, ok := r.commits[sha]; !ok {
		return fmt.Errorf("unknown commit %q", sha)
	}
	r.refs[ref] = sha
	return nil
}

// Commit returns a copy of a commit, including its content and all parents.
func (f *Forge) Commit(repo forge.RepoRef, sha string) (Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.repo(repo).commits[sha]
	if !ok {
		return Commit{}, fmt.Errorf("unknown commit %q", sha)
	}
	c.Parents, c.PRs = slices.Clone(c.Parents), slices.Clone(c.PRs)
	return c, nil
}

// History returns first-parent branch history, oldest first, including the root.
func (f *Forge) History(repo forge.RepoRef, ref string) ([]Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	sha, ok := r.refs[ref]
	if !ok {
		return nil, fmt.Errorf("unknown ref %q", ref)
	}
	var history []Commit
	for sha != "" {
		c := r.commits[sha]
		c.Parents, c.PRs = slices.Clone(c.Parents), slices.Clone(c.PRs)
		history = append(history, c)
		sha = ""
		if len(c.Parents) > 0 {
			sha = c.Parents[0]
		}
	}
	slices.Reverse(history)
	return history, nil
}

// Statuses returns a snapshot keyed by context name.
func (f *Forge) Statuses(repo forge.RepoRef, pr int) map[string]forge.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.repo(repo).statuses[pr])
}

// Comments returns the posted bodies, including hidden markers, keyed by action.
func (f *Forge) Comments(repo forge.RepoRef, pr int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.repo(repo).comments[pr])
}
