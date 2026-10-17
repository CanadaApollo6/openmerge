// Package fake implements a concurrent in-memory forge and a tiny commit graph.
// Commits retain file snapshots, PR content markers, and all parent edges.
package fake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Commit is an immutable snapshot of a fake git commit.
type Commit struct {
	SHA     string
	Parents []string
	PRs     []int
	Message string
	Files   map[string]string
}

type repository struct {
	deltas        map[string]fileDelta
	materialized  []string
	defaultBranch string
	refs          map[string]string
	commits       map[string]Commit
	prs           map[int]forge.PullRequest
	checks        map[string][]forge.CheckRun
	reruns        map[string]rerunRecord
	statuses      map[int]map[string]forge.Status
	comments      map[int][]Comment
	prCommits     map[int][]string
	prBases       map[int]string
	authors       map[string]forge.CommitAuthor
	protected     map[string]bool
	linearHistory map[string]bool
	nonce         func() string
	permissions   map[string]bool
	updated       map[int]time.Time
	nextComment   int64
}

// Forge implements both forge interfaces. Use New to construct it. All helpers
// are synchronized too; callbacks run without the lock and may call the forge.
// Approvals retain their exact reviewed SHA across pushes; no approval-reset
// configuration is required.
type Forge struct {
	mu                          sync.Mutex
	repos                       map[forge.RepoRef]*repository
	failures                    map[string][]error
	onTestRef                   func(string, []int)
	disableFenceLeaseForTesting bool
	nonce                       func() string
}

var (
	_ forge.Driver        = (*Forge)(nil)
	_ forge.Git           = (*Forge)(nil)
	_ forge.CheckRerunner = (*Forge)(nil)
)

// New returns an empty forge, with no repositories or refs.
func New() *Forge {
	return newForge(func() string {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			panic(err)
		}
		return fmt.Sprintf("%x", nonce)
	})
}

// NewDeterministic uses unique counter nonces for repeatable simulations.
// New uses crypto/rand nonces, as the real Git transport does.
func NewDeterministic() *Forge {
	var next uint64
	return newForge(func() string {
		next++
		return fmt.Sprintf("%032x", next)
	})
}

func newForge(nonce func() string) *Forge {
	return &Forge{
		repos:    make(map[forge.RepoRef]*repository),
		failures: make(map[string][]error),
		nonce:    nonce,
	}
}

func (f *Forge) repo(repo forge.RepoRef) *repository {
	r := f.repos[repo]
	if r == nil {
		r = &repository{
			nonce: f.nonce, permissions: make(map[string]bool), updated: make(map[int]time.Time), authors: make(map[string]forge.CommitAuthor), protected: make(map[string]bool),
			refs: make(map[string]string), commits: make(map[string]Commit),
			prs: make(map[int]forge.PullRequest), checks: make(map[string][]forge.CheckRun), reruns: make(map[string]rerunRecord),
			statuses: make(map[int]map[string]forge.Status), comments: make(map[int][]Comment), prCommits: make(map[int][]string), prBases: make(map[int]string),
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
	if r.defaultBranch == "" {
		r.defaultBranch = base
	}
	if r.refs[base] == "" {
		r.refs[base] = r.commit(nil, nil, "initial base")
	}
	if r.refs[fenceRef] == "" {
		r.refs[fenceRef] = r.mintFence(nil, "initial", "")
	}
	return r.refs[base], r.refs[fenceRef]
}

func (r *repository) commit(parents []string, prs []int, message string, files ...map[string]string) string {
	content := map[string]string{}
	if len(files) > 0 {
		content = maps.Clone(files[0])
	} else if len(parents) > 0 {
		content = maps.Clone(r.files(parents[0]))
	}
	prs = slices.Clone(prs)
	slices.Sort(prs)
	prs = slices.Compact(prs)
	c := Commit{Parents: slices.Clone(parents), PRs: prs, Message: message, Files: content}
	// These fields contain no values that JSON cannot encode.
	var delta fileDelta
	if r.deltas == nil {
		data, _ := json.Marshal(c)
		c.SHA = fmt.Sprintf("%x", sha256.Sum256(data))
	} else {
		delta = r.treeDelta(c)
		c.SHA = compactSHA(c, delta)
	}
	r.commits[c.SHA] = c
	if r.deltas != nil {
		r.compactTree(c, delta)
	}
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

// OnTestRef installs a callback for newly pushed test refs. prs contains only
// the request's PRs, in request order; the commit contains inherited content too.
func (f *Forge) OnTestRef(fn func(sha string, prs []int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onTestRef = fn
}

// BuildTestRef composes merge/squash/rebase commits with a batch ID, or verifies
// and publishes an empty marker over a fast-forward chain. Failed builds never move the ref.
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
	_, ok := r.commits[req.BaseSHA]
	if !ok {
		return "", nil, false, fmt.Errorf("unknown base commit %q", req.BaseSHA)
	}
	if req.Ref == "" || !forge.ValidBatchID(req.BatchID) || len(req.PRs) == 0 {
		return "", nil, false, errors.New("test ref, valid batch ID, and PRs are required")
	}
	if req.Method != forge.Merge && req.Method != forge.Squash && req.Method != forge.Rebase && req.Method != forge.FastForward {
		return "", nil, false, fmt.Errorf("unsupported merge method %q", req.Method)
	}
	content := slices.Clone(r.prsFor(req.BaseSHA))
	files := maps.Clone(r.files(req.BaseSHA))
	prs := make([]int, 0, len(req.PRs))
	top := req.BaseSHA
	for i, pr := range req.PRs {
		actual, ok := r.prs[pr.Number]
		if !ok || actual.HeadSHA != pr.HeadSHA {
			return "", nil, false, fmt.Errorf("PR %d head %q does not match actual head %q", pr.Number, pr.HeadSHA, actual.HeadSHA)
		}
		if req.Method == forge.FastForward {
			if top == pr.HeadSHA || !r.isAncestor(top, pr.HeadSHA) {
				return "", nil, false, fmt.Errorf("PR #%d: %w", pr.Number, forge.ErrNotFastForward)
			}
			top = pr.HeadSHA
			prs = append(prs, pr.Number)
			continue
		}
		var added []int
		if !slices.Contains(content, pr.Number) {
			added = []int{pr.Number}
		}
		edits := make(map[string]string)
		content = append(content, pr.Number)
		prs = append(prs, pr.Number)
		for j, original := range r.prCommits[pr.Number] {
			c := r.commits[original]
			// Replay each file delta against the evolving test tree. A different edit
			// to the same file is a conflict, just as with these add-file fixtures in git.
			for path, value := range r.files(original) {
				before, existed := r.files(c.Parents[0])[path]
				if existed && before == value {
					continue
				}
				current, present := files[path]
				if (present != existed || current != before) && (!present || current != value) {
					return "", nil, false, fmt.Errorf("PR %d file %s: %w", pr.Number, path, forge.ErrConflict)
				}
				if !present || current != value {
					edits[path] = value
				}
				files[path] = value
			}
			if req.Method == forge.Rebase {
				message := fmt.Sprintf("rebase PR %d commit %s", pr.Number, original)
				if i == len(req.PRs)-1 && j == len(r.prCommits[pr.Number])-1 {
					message += "\nOpenMerge-Batch: " + req.BatchID
				}
				top = r.buildCommit([]string{top}, content, message, files, edits, added)
				clear(edits)
				added = nil
			}
		}
		if req.Method == forge.Merge || req.Method == forge.Squash {
			message := fmt.Sprintf("%s PR %d %s", req.Method, pr.Number, pr.HeadSHA)
			if i == len(req.PRs)-1 {
				message += "\nOpenMerge-Batch: " + req.BatchID
			}
			parents := []string{top}
			if req.Method == forge.Merge {
				parents = append(parents, pr.HeadSHA)
			}
			top = r.buildCommit(parents, content, message, files, edits, added)
		}
	}
	if req.Method == forge.FastForward {
		top = r.commit([]string{top}, r.prsFor(top), fmt.Sprintf("OpenMerge fast-forward test %s\n\nOpenMerge-Batch: %s", r.nonce(), req.BatchID), r.files(top))
	}
	if req.Method != forge.FastForward {
		r.materializeTip(top, files, content)
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
	landed := req.TestedSHA
	if req.FastForwardHeadSHA != "" {
		if len(req.PRs) == 0 || req.PRs[len(req.PRs)-1].HeadSHA != req.FastForwardHeadSHA {
			return "", errors.New("fast-forward landing head must match the final pinned PR")
		}
		if err := r.verifyFastForward(req.ExpectedBaseSHA, req.TestedSHA, req.FastForwardHeadSHA); err != nil {
			return "", err
		}
		landed = req.FastForwardHeadSHA
	}
	if r.refs[req.Base] == landed || r.refs[req.Base] != req.ExpectedBaseSHA || !f.disableFenceLeaseForTesting && r.refs[req.Fence] != req.ExpectedFence {
		return "", forge.ErrLeaseRejected
	}
	if req.Base == req.Fence {
		return "", errors.New("base and fence must be distinct refs")
	}
	if _, ok := r.commits[req.TestedSHA]; !ok {
		return "", fmt.Errorf("unknown tested commit %q", req.TestedSHA)
	}
	for _, pinned := range req.PRs {
		pr, ok := r.prs[pinned.Number]
		if !ok || pr.HeadSHA != pinned.HeadSHA {
			return "", fmt.Errorf("PR %d head %q does not match actual head %q", pinned.Number, pinned.HeadSHA, pr.HeadSHA)
		}
	}
	next, err := r.fence(req.ExpectedFence, req.NextFenceID, req.TestedSHA)
	if err != nil {
		return "", err
	}
	r.refs[req.Base], r.refs[req.Fence] = landed, next
	for _, pinned := range req.PRs {
		if pr := r.prs[pinned.Number]; r.isAncestor(pinned.HeadSHA, landed) {
			pr.Closed = true
			r.prs[pr.Number] = pr
		}
	}
	return next, nil
}

func (r *repository) fence(expected, id, tested string) (string, error) {
	if id == "" || strings.ContainsAny(id, " \t\r\n\x00") {
		return "", errors.New("single-token fence ID is required")
	}
	if _, ok := r.commits[expected]; !ok {
		return "", fmt.Errorf("unknown fence commit %q", expected)
	}
	return r.mintFence([]string{expected}, id, tested), nil
}

func (r *repository) mintFence(parents []string, id, tested string) string {
	message := "openmerge fence " + id
	if tested != "" {
		message += " tested=" + tested
	}
	message += " nonce=" + r.nonce()
	return r.commit(parents, nil, message)
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
		return "", forge.ErrLeaseRejected
	}
	next, err := r.fence(expectedSHA, nextID, "")
	if err != nil {
		return "", err
	}
	r.refs[fenceRef] = next
	return next, nil
}

// ReadFence returns the current fence SHA and the ID recorded in its message.
func (f *Forge) ReadFence(ctx context.Context, repo forge.RepoRef, fenceRef string) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReadFence"); err != nil {
		return "", "", "", err
	}
	r := f.repo(repo)
	sha, ok := r.refs[fenceRef]
	if !ok {
		return "", "", "", fmt.Errorf("unknown ref %q", fenceRef)
	}
	id, tested := forge.ParseFenceMessage(r.commits[sha].Message)
	return sha, id, tested, nil
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
	pr.Labels = slices.Clone(pr.Labels)
	if pr.FilesKnown {
		pr.Files = []string{}
		r := f.repo(repo)
		before, after := r.files(r.prBases[number]), r.files(pr.HeadSHA)
		for path, content := range after {
			if old, exists := before[path]; !exists || old != content {
				pr.Files = append(pr.Files, path)
			}
		}
		for path := range before {
			if _, exists := after[path]; !exists {
				pr.Files = append(pr.Files, path)
			}
		}
		slices.Sort(pr.Files)
		if len(pr.Files) > forge.MaxChangedFiles {
			pr.Files, pr.FilesKnown = nil, false
		}
	}
	if pr.Closed {
		pr.Approvals = nil
	}
	for i := range pr.Approvals {
		a := &pr.Approvals[i]
		a.CanWrite = !pr.HeadInBaseRepo && a.SHA == pr.HeadSHA && f.repo(repo).permissions[a.User]
	}
	return pr, nil
}

// ListOpenPullRequests returns the current open heads for reconciliation.
func (f *Forge) ListOpenPullRequests(ctx context.Context, repo forge.RepoRef, base string) ([]forge.PullRequestSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ListOpenPullRequests"); err != nil {
		return nil, err
	}
	r := f.repo(repo)
	result := []forge.PullRequestSummary{}
	for _, pr := range r.prs {
		if !pr.Closed && (base == "" || pr.Base == base) {
			result = append(result, forge.PullRequestSummary{Number: pr.Number, HeadSHA: pr.HeadSHA, UpdatedAt: r.updated[pr.Number]})
		}
	}
	slices.SortFunc(result, func(a, b forge.PullRequestSummary) int { return a.Number - b.Number })
	return result, nil
}

// ListComments returns comments created at or after since, including other authors.
func (f *Forge) ListComments(ctx context.Context, repo forge.RepoRef, pr int, since time.Time) ([]forge.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ListComments"); err != nil {
		return nil, err
	}
	result := []forge.Comment{}
	for _, c := range f.repo(repo).comments[pr] {
		if since.IsZero() || !c.CreatedAt.Before(since) {
			result = append(result, forge.Comment{ID: c.ID, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
		}
	}
	return result, nil
}

// CanWrite reads current repository permissions without caching a decision.
func (f *Forge) CanWrite(ctx context.Context, repo forge.RepoRef, user string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CanWrite"); err != nil {
		return false, err
	}
	return f.repo(repo).permissions[user], nil
}

func (r *repository) addComment(pr int, c Comment) {
	r.nextComment++
	c.ID = r.nextComment
	c.CreatedAt = time.Now().UTC()
	r.comments[pr] = append(r.comments[pr], c)
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

// ReportStatus upserts the OpenMerge check on exactly one PR head.
func (f *Forge) ReportStatus(ctx context.Context, repo forge.RepoRef, pr int, headSHA string, status forge.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReportStatus"); err != nil {
		return err
	}
	r := f.repo(repo)
	if r.statuses[pr] == nil {
		r.statuses[pr] = make(map[string]forge.Status)
	}
	r.statuses[pr][headSHA] = status
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
	marker := "<!-- openmerge:" + key + " -->"
	for _, comment := range r.comments[pr] {
		if comment.Author == Identity && strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	r.addComment(pr, Comment{Author: Identity, Key: key, Body: body + "\n" + marker})
	return nil
}

// Identity is the authenticated bot identity used by this fake driver.
const Identity = "openmerge-bot"

// Comment includes its author so user-supplied markers cannot suppress writes.
type Comment struct {
	ID                int64
	Author, Key, Body string
	CreatedAt         time.Time
}

// MarkLanded closes the PR and records one commit link across retries.
func (f *Forge) MarkLanded(ctx context.Context, repo forge.RepoRef, number int, landedSHA string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "MarkLanded"); err != nil {
		return err
	}
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return fmt.Errorf("unknown PR %d", number)
	}
	if _, ok := r.commits[landedSHA]; !ok {
		return fmt.Errorf("unknown landed commit %q", landedSHA)
	}
	key := "landed/" + landedSHA
	for _, c := range r.comments[number] {
		if c.Author == Identity && c.Key == key {
			pr.Closed = true
			r.prs[number] = pr
			return nil
		}
	}
	r.addComment(number, Comment{Author: Identity, Key: key, Body: "Landed in [" + landedSHA + "](/" + repo.Path + "/commit/" + landedSHA + ")"})
	pr.Closed = true
	r.updated[number] = time.Now().UTC()
	r.prs[number] = pr
	return nil
}

// EnsureFence initializes a missing fence without replacing an existing token.
func (f *Forge) EnsureFence(ctx context.Context, repo forge.RepoRef, fenceRef string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "EnsureFence"); err != nil {
		return "", err
	}
	r := f.repo(repo)
	if r.refs[fenceRef] == "" {
		r.refs[fenceRef] = r.mintFence(nil, "initial", "")
	}
	return r.refs[fenceRef], nil
}

// IsAncestor traverses every parent edge, including merged PR histories.
func (f *Forge) IsAncestor(ctx context.Context, repo forge.RepoRef, ancestor, descendant string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "IsAncestor"); err != nil {
		return false, err
	}
	r := f.repo(repo)
	for _, sha := range []string{ancestor, descendant} {
		if _, ok := r.commits[sha]; !ok {
			return false, fmt.Errorf("unknown commit %q", sha)
		}
	}
	return r.isAncestor(ancestor, descendant), nil
}

func (r *repository) isAncestor(ancestor, descendant string) bool {
	pending := []string{descendant}
	seen := make(map[string]bool)
	for len(pending) > 0 {
		sha := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if sha == ancestor {
			return true
		}
		if !seen[sha] {
			seen[sha] = true
			pending = append(pending, r.commits[sha].Parents...)
		}
	}
	return false
}

// ReadFile reads a file from a branch, full ref, or commit snapshot.
func (f *Forge) ReadFile(ctx context.Context, repo forge.RepoRef, ref, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReadFile"); err != nil {
		return nil, err
	}
	r := f.repo(repo)
	sha := ref
	if value := r.refs[ref]; value != "" {
		sha = value
	} else if value := r.refs[strings.TrimPrefix(ref, "refs/heads/")]; value != "" {
		sha = value
	}
	content, ok := r.files(sha)[path]
	if !ok {
		return nil, forge.ErrFileNotFound
	}
	return []byte(content), nil
}
