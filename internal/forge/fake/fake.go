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
	refs      map[string]string
	commits   map[string]Commit
	prs       map[int]forge.PullRequest
	checks    map[string][]forge.CheckRun
	statuses  map[int]map[string]forge.Status
	comments  map[int][]Comment
	prCommits map[int][]string
	prBases   map[int]string
	nonce     func() string
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
	_ forge.Driver = (*Forge)(nil)
	_ forge.Git    = (*Forge)(nil)
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
			nonce: f.nonce,
			refs:  make(map[string]string), commits: make(map[string]Commit),
			prs: make(map[int]forge.PullRequest), checks: make(map[string][]forge.CheckRun),
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
	if len(parents) > 0 {
		content = maps.Clone(r.commits[parents[0]].Files)
	}
	if len(files) > 0 {
		content = maps.Clone(files[0])
	}
	prs = slices.Clone(prs)
	slices.Sort(prs)
	prs = slices.Compact(prs)
	c := Commit{Parents: slices.Clone(parents), PRs: prs, Message: message, Files: content}
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

// OnTestRef installs a callback for newly pushed test refs. prs contains only
// the request's PRs, in request order; the commit contains inherited content too.
func (f *Forge) OnTestRef(fn func(sha string, prs []int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onTestRef = fn
}

// BuildTestRef composes merge commits, one squash per PR, or rebased PR commits. Each
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
	files := maps.Clone(base.Files)
	prs := make([]int, 0, len(req.PRs))
	top := req.BaseSHA
	for i, pr := range req.PRs {
		actual, ok := r.prs[pr.Number]
		if !ok || actual.HeadSHA != pr.HeadSHA {
			return "", nil, false, fmt.Errorf("PR %d head %q does not match actual head %q", pr.Number, pr.HeadSHA, actual.HeadSHA)
		}
		content = append(content, pr.Number)
		prs = append(prs, pr.Number)
		for j, original := range r.prCommits[pr.Number] {
			c := r.commits[original]
			parent := r.commits[c.Parents[0]]
			// Replay each file delta against the evolving test tree. A different edit
			// to the same file is a conflict, just as with these add-file fixtures in git.
			for path, value := range c.Files {
				before, existed := parent.Files[path]
				if existed && before == value {
					continue
				}
				current, present := files[path]
				if (present != existed || current != before) && (!present || current != value) {
					return "", nil, false, fmt.Errorf("PR %d file %s: %w", pr.Number, path, forge.ErrConflict)
				}
				files[path] = value
			}
			if req.Method == forge.Rebase {
				message := fmt.Sprintf("rebase PR %d commit %s", pr.Number, original)
				if i == len(req.PRs)-1 && j == len(r.prCommits[pr.Number])-1 {
					message += "\nOpenMerge-Batch: " + req.BatchID
				}
				top = r.commit([]string{top}, content, message, files)
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
			top = r.commit(parents, content, message, files)
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
	if r.refs[req.Base] == req.TestedSHA || r.refs[req.Base] != req.ExpectedBaseSHA || !f.disableFenceLeaseForTesting && r.refs[req.Fence] != req.ExpectedFence {
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
	r.refs[req.Base], r.refs[req.Fence] = req.TestedSHA, next
	for _, pinned := range req.PRs {
		if pr := r.prs[pinned.Number]; r.isAncestor(pinned.HeadSHA, req.TestedSHA) {
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
	r.comments[pr] = append(r.comments[pr], Comment{Author: Identity, Key: key, Body: body + "\n" + marker})
	return nil
}

// Identity is the authenticated bot identity used by this fake driver.
const Identity = "openmerge-bot"

// Comment includes its author so user-supplied markers cannot suppress writes.
type Comment struct{ Author, Key, Body string }

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
	r.comments[number] = append(r.comments[number], Comment{Author: Identity, Key: key, Body: "Landed in [" + landedSHA + "](/" + repo.Path + "/commit/" + landedSHA + ")"})
	pr.Closed = true
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
