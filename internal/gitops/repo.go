// Package gitops implements forge.Git using the git executable and bare caches.
// Cache locks coordinate goroutines and Repo instances in this process only.
// A work directory must not be shared by multiple OpenMerge processes.
package gitops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Remote resolves an opaque forge repository path and fresh credentials.
// Initial credentials must last through preflight reads and fetches. BuildTestRef, Land and
// AdvanceFence resolve again immediately before pushing; the fresh credentials
// must remain valid for that push.
type Remote func(context.Context, forge.RepoRef) (url string, auth Auth, err error)

// Identity names an author or committer of a git commit.
type Identity struct{ Name, Email string }

// Config supplies the cache, transport, PR ref mapping, and commit metadata.
// HeadRef is forge specific; GitHub uses refs/pull/<number>/head.
// Nil Now uses time.Now. Zero identities use OpenMerge <openmerge@localhost>.
type Config struct {
	WorkDir           string
	Remote            Remote
	HeadRef           func(pr int) string
	Author, Committer Identity
	// PullRequest supplies current metadata at build time. Production callers
	// must set it; nil uses Author and generic messages for standalone git use.
	PullRequest   func(context.Context, forge.RepoRef, int) (forge.PullRequest, error)
	CommitAuthors func(context.Context, forge.RepoRef, int) ([]forge.CommitAuthor, error)
	Now           func() time.Time
	// GCInterval and GCThresholdBytes trigger best-effort cache collection after
	// an operation. Zero selects one hour / 512 MiB; negative disables a trigger.
	GCInterval       time.Duration
	GCThresholdBytes int64
}

// Repo is a CLI-backed Git implementation. Construct it with New.
type Repo struct {
	cfg          Config
	git          string
	askpass      string
	explicitBase bool
}

var (
	_          forge.Git = (*Repo)(nil)
	cacheLocks sync.Map  // absolute bare repository path -> *sync.Mutex
)

// New checks Git >= 2.38 once and prepares a credential-free askpass helper.
func New(ctx context.Context, cfg Config) (repo *Repo, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("gitops work directory %q: %w", cfg.WorkDir, err)
		}
	}()
	if cfg.WorkDir == "" || cfg.Remote == nil || cfg.HeadRef == nil {
		return nil, errors.New("gitops requires WorkDir, Remote, and HeadRef")
	}
	for _, id := range []*Identity{&cfg.Author, &cfg.Committer} {
		if *id == (Identity{}) {
			*id = Identity{Name: "OpenMerge", Email: "openmerge@localhost"}
		}
		if !validIdentity(*id) {
			return nil, errors.New("invalid git commit identity")
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.GCInterval == 0 {
		cfg.GCInterval = time.Hour
	}
	if cfg.GCThresholdBytes == 0 {
		cfg.GCThresholdBytes = 512 << 20
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("gitops requires git >= 2.38 on PATH")
	}
	r := &Repo{cfg: cfg, git: git}
	s := &session{r: r}
	version, err := s.command(ctx, "", "--version")
	if err != nil {
		return nil, err
	}
	var major, minor int
	if n, _ := fmt.Sscanf(version, "git version %d.%d", &major, &minor); n != 2 || major < 2 || major == 2 && minor < 38 {
		return nil, fmt.Errorf("gitops requires git >= 2.38; found %q", version)
	}
	// --merge-base was added after 2.38. Older versions use synthetic parents
	// to give merge-tree the same explicit base when replaying a commit.
	out, diagnostic, _ := s.run(ctx, "", "merge-tree", "-h")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.explicitBase = strings.Contains(out+diagnostic, "--merge-base")
	r.cfg.WorkDir, err = filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.cfg.WorkDir, 0o700); err != nil {
		return nil, err
	}
	// Atomic replacement keeps constructors from truncating an executing helper.
	helper, err := os.CreateTemp(r.cfg.WorkDir, ".askpass-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(helper.Name()) }()
	if err := helper.Chmod(0o700); err != nil {
		_ = helper.Close()
		return nil, err
	}
	_, writeErr := helper.WriteString(askpassScript)
	closeErr := helper.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, err
	}
	r.askpass = filepath.Join(r.cfg.WorkDir, "askpass")
	if err := os.Rename(helper.Name(), r.askpass); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repo) identityEnv() []string {
	date := r.cfg.Now().Format("2006-01-02T15:04:05-07:00")
	return []string{"GIT_AUTHOR_NAME=" + r.cfg.Author.Name, "GIT_AUTHOR_EMAIL=" + r.cfg.Author.Email,
		"GIT_COMMITTER_NAME=" + r.cfg.Committer.Name, "GIT_COMMITTER_EMAIL=" + r.cfg.Committer.Email,
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
}

func (r *Repo) open(ctx context.Context, repo forge.RepoRef) (*session, func(), error) {
	return r.openSession(ctx, repo, false)
}

func (r *Repo) openSession(ctx context.Context, repo forge.RepoRef, diffRead bool) (*session, func(), error) {
	key := fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path)))
	dir := filepath.Join(r.cfg.WorkDir, key)
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	if err := lockCache(ctx, mu); err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*session, func(), error) { mu.Unlock(); return nil, nil, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s := &session{r: r, diffRead: diffRead}
	if err := s.resolveRemote(ctx, repo); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if _, err := s.command(ctx, "", "init", "--bare", "--template=", "--", dir); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	}
	s.dir = dir
	if err := s.startAcquisitions(ctx); err != nil {
		return fail(err)
	}
	return s, func() {
		defer mu.Unlock()
		// All commands, including push, have completed before maintenance runs.
		maintenanceCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.maintainCache(maintenanceCtx)
	}, nil
}

func (s *session) resolveRemote(ctx context.Context, repo forge.RepoRef) error {
	remote, auth, err := s.r.cfg.Remote(ctx, repo)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		fresh := &session{auth: auth}
		diagnostic := s.scrub(fresh.scrub(err.Error()))
		if errors.Is(err, ErrAuth) || authFailure(err.Error()) {
			return fmt.Errorf("resolve git remote: %s: %w", diagnostic, ErrAuth)
		}
		return fmt.Errorf("resolve git remote: %s", diagnostic)
	}
	s.remote, s.auth, err = cleanRemote(remote, auth)
	return err
}

func validSHA(sha string) bool { return forge.ValidDiffObjectID(sha) }

func (s *session) branch(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return forge.BranchRef(ref)
}

func (s *session) fetch(ctx context.Context, specs ...string) error {
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", s.remote}
	_, err := s.command(ctx, "", append(args, specs...)...)
	return err
}

func (s *session) commits(ctx context.Context, shas ...string) error {
	for _, sha := range shas {
		if err := forge.ValidateCommitIDs(sha); err != nil {
			return err
		}
		if _, _, err := s.run(ctx, "", "cat-file", "-e", sha+"^{commit}"); err == nil {
			continue
		}
		if err := s.fetch(ctx, sha); err != nil {
			return err
		}
		if _, err := s.command(ctx, "", "cat-file", "-e", sha+"^{commit}"); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) read(ctx context.Context, refs ...string) (map[string]string, error) {
	args := append([]string{"ls-remote", "--refs", "--", s.remote}, refs...)
	out, err := s.command(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if ok && validSHA(sha) {
			values[ref] = sha
		}
	}
	return values, nil
}

// ReadRef queries the remote rather than trusting the local cache.
func (r *Repo) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	full, err := s.branch(ctx, ref)
	if err != nil {
		return "", err
	}
	values, err := s.read(ctx, full)
	if err != nil {
		return "", err
	}
	if values[full] == "" {
		return "", errors.New("git ref does not exist")
	}
	return values[full], nil
}

// HasMergeCommits inspects the complete newly introduced commit graph.
func (r *Repo) HasMergeCommits(ctx context.Context, repo forge.RepoRef, base, head string) (bool, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := s.commits(ctx, base, head); err != nil {
		return false, err
	}
	merges, err := s.command(ctx, "", "rev-list", "--merges", "--max-count=1", head, "^"+base)
	return merges != "", err
}

// IsAncestor checks graph reachability after obtaining any missing commits.
func (r *Repo) IsAncestor(ctx context.Context, repo forge.RepoRef, ancestor, descendant string) (bool, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := s.commits(ctx, ancestor, descendant); err != nil {
		return false, err
	}
	_, diagnostic, err := s.run(ctx, "", "merge-base", "--is-ancestor", ancestor, descendant)
	if exitCode(err, 1) {
		return false, nil
	}
	if err != nil {
		return false, s.failure("merge-base", diagnostic, err)
	}
	return true, nil
}
