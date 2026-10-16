package fake

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// OpenPR creates a PR head based on baseSHA. PushPR creates subsequent heads.
func (f *Forge) OpenPR(repo forge.RepoRef, number int, baseSHA string, headInBaseRepo bool) (forge.PullRequest, error) {
	return f.OpenPRWithFiles(repo, number, baseSHA, headInBaseRepo, map[string]string{fmt.Sprintf("pr-%d.txt", number): fmt.Sprintf("PR %d", number)})
}

// OpenPRWithFiles creates a PR adding or changing the supplied files.
func (f *Forge) OpenPRWithFiles(repo forge.RepoRef, number int, baseSHA string, headInBaseRepo bool, changes map[string]string) (forge.PullRequest, error) {
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
	files := maps.Clone(base.Files)
	for path, value := range changes {
		files[path] = value
	}
	head := r.commit([]string{baseSHA}, append(slices.Clone(base.PRs), number), fmt.Sprintf("PR %d", number), files)
	baseRef := ""
	for ref, sha := range r.refs {
		if sha == baseSHA && (baseRef == "" || ref < baseRef) {
			baseRef = ref
		}
	}
	pr := forge.PullRequest{Number: number, HeadSHA: head, Base: baseRef, Author: "contributor", AuthorID: 12, AuthorEmail: "12+contributor@users.noreply.github.com", Title: fmt.Sprintf("PR %d", number), HeadBranch: fmt.Sprintf("contributor:pr-%d", number), HeadInBaseRepo: headInBaseRepo, LabelsKnown: true, FilesKnown: true, ConflictKnown: true}
	for path, content := range changes {
		if old, exists := base.Files[path]; !exists || old != content {
			pr.Files = append(pr.Files, path)
		}
	}
	slices.Sort(pr.Files)
	if len(pr.Files) > forge.MaxChangedFiles {
		pr.Files, pr.FilesKnown = nil, false
	}
	r.prs[number] = pr
	r.updated[number] = time.Now().UTC()
	r.prBases[number] = baseSHA
	r.prCommits[number] = []string{head}
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
	files := maps.Clone(r.commits[pr.HeadSHA].Files)
	files[fmt.Sprintf("pr-%d-commit-%d.txt", number, len(r.prCommits[number])+1)] = message
	pr.HeadSHA = r.commit([]string{pr.HeadSHA}, r.commits[pr.HeadSHA].PRs, message, files)
	r.prCommits[number] = append(r.prCommits[number], pr.HeadSHA)
	r.prs[number] = pr
	r.updated[number] = time.Now().UTC()
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
	r.updated[number] = time.Now().UTC()
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
	r.permissions[approval.User] = approval.CanWrite
	r.prs[number] = pr
	r.updated[number] = time.Now().UTC()
	return nil
}

// SetCanWrite changes current command and review authorization.
func (f *Forge) SetCanWrite(repo forge.RepoRef, user string, write bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(repo).permissions[user] = write
}

// SetPRMetadata changes the base, draft flag, and author of an existing PR.
func (f *Forge) SetPRMetadata(repo forge.RepoRef, number int, base string, draft bool, author string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return fmt.Errorf("unknown PR %d", number)
	}
	pr.Base, pr.Draft, pr.Author = base, draft, author
	r.prs[number] = pr
	r.updated[number] = time.Now().UTC()
	return nil
}

// SetPRConditions changes label and mergeability observations. nil conflict
// means the forge has not computed mergeability yet; nil labels is unknown.
func (f *Forge) SetPRConditions(repo forge.RepoRef, number int, labels []string, conflict *bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return fmt.Errorf("unknown PR %d", number)
	}
	pr.Labels, pr.LabelsKnown = slices.Clone(labels), labels != nil
	pr.ConflictKnown, pr.Conflict = conflict != nil, false
	if conflict != nil {
		pr.Conflict = *conflict
	}
	r.prs[number] = pr
	r.updated[number] = time.Now().UTC()
	return nil
}

// SetCheckRuns replaces a commit's check snapshot, copying the caller's slice.
func (f *Forge) SetCheckRuns(repo forge.RepoRef, sha string, checks []forge.CheckRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	checks = slices.Clone(checks)
	for i := range checks {
		if checks[i].Source == "" {
			checks[i].Source = "fake:ci"
		}
	}
	f.repo(repo).checks[sha] = checks
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

// PushBase fast-forwards a branch with an external commit, retaining its PRs.
func (f *Forge) PushBase(repo forge.RepoRef, ref, message string, changes map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	base, ok := r.commits[r.refs[ref]]
	if !ok {
		return "", fmt.Errorf("unknown base ref %q", ref)
	}
	files := maps.Clone(base.Files)
	for path, value := range changes {
		files[path] = value
	}
	sha := r.commit([]string{base.SHA}, base.PRs, message, files)
	r.refs[ref] = sha
	return sha, nil
}

// PushRef creates a branch or fast-forwards it, like an ordinary git push.
func (f *Forge) PushRef(repo forge.RepoRef, ref, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	if _, ok := r.commits[sha]; !ok {
		return fmt.Errorf("unknown commit %q", sha)
	}
	if current, exists := r.refs[ref]; exists && !r.isAncestor(current, sha) {
		return forge.ErrLeaseRejected
	}
	r.refs[ref] = sha
	return nil
}

// CommitCount counts commits reachable from head but not base, like rev-list.
func (f *Forge) CommitCount(repo forge.RepoRef, base, head string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	for _, sha := range []string{base, head} {
		if _, ok := r.commits[sha]; !ok {
			return 0, fmt.Errorf("unknown commit %q", sha)
		}
	}
	count := 0
	pending := []string{head}
	seen := make(map[string]bool)
	for len(pending) > 0 {
		sha := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[sha] || r.isAncestor(sha, base) {
			continue
		}
		seen[sha] = true
		count++
		pending = append(pending, r.commits[sha].Parents...)
	}
	return count, nil
}

// Commit returns a copy of a commit, including its content and all parents.
func (f *Forge) Commit(repo forge.RepoRef, sha string) (Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.repo(repo).commits[sha]
	if !ok {
		return Commit{}, fmt.Errorf("unknown commit %q", sha)
	}
	c.Parents, c.PRs, c.Files = slices.Clone(c.Parents), slices.Clone(c.PRs), maps.Clone(c.Files)
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
		c.Parents, c.PRs, c.Files = slices.Clone(c.Parents), slices.Clone(c.PRs), maps.Clone(c.Files)
		history = append(history, c)
		sha = ""
		if len(c.Parents) > 0 {
			sha = c.Parents[0]
		}
	}
	slices.Reverse(history)
	return history, nil
}

// Statuses returns a snapshot keyed by head SHA.
func (f *Forge) Statuses(repo forge.RepoRef, pr int) map[string]forge.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.repo(repo).statuses[pr])
}

// Comments returns the posted bodies, including hidden markers, keyed by action.
func (f *Forge) Comments(repo forge.RepoRef, pr int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[string]string)
	for _, c := range f.repo(repo).comments[pr] {
		if c.Author == Identity {
			result[c.Key] = c.Body
		}
	}
	return result
}

// AddComment posts a comment as an arbitrary identity, for dedupe tests.
func (f *Forge) AddComment(repo forge.RepoRef, pr int, author, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	r.addComment(pr, Comment{Author: author, Body: body})
}

// CommentRecords returns all comments, including comments by other identities.
func (f *Forge) CommentRecords(repo forge.RepoRef, pr int) []Comment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.repo(repo).comments[pr])
}

// ContainsPR checks the PR's actual changed file content, rather than ancestry.
func (f *Forge) ContainsPR(repo forge.RepoRef, sha string, number int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return false
	}
	commit, ok := r.commits[sha]
	if !ok {
		return false
	}
	base := r.commits[r.prBases[number]]
	for path, value := range r.commits[pr.HeadSHA].Files {
		if before, existed := base.Files[path]; !existed || before != value {
			if got, exists := commit.Files[path]; !exists || got != value {
				return false
			}
		}
	}
	return true
}

// DisableFenceLeaseForTesting deliberately breaks Land's fence lease so simulator
// mutation tests can prove they detect an abandoned push. Never use in production.
func (f *Forge) DisableFenceLeaseForTesting() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableFenceLeaseForTesting = true
}
