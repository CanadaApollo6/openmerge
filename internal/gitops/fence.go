package gitops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func (s *session) fence(ctx context.Context, parent, id, tested string) (string, error) {
	if id == "" || strings.ContainsAny(id, " \t\r\n\x00") {
		return "", errors.New("single-token fence ID is required")
	}
	if parent != "" {
		if err := s.commits(ctx, parent); err != nil {
			return "", err
		}
	}
	tree, err := s.command(ctx, "", "mktree")
	if err != nil {
		return "", err
	}
	message := "openmerge fence " + id
	if tested != "" {
		message += " tested=" + tested
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	message += " nonce=" + hex.EncodeToString(nonce)
	if parent == "" {
		return s.commit(ctx, tree, message)
	}
	return s.commit(ctx, tree, message, parent)
}

func leaseRejected(stdout, stderr string) bool {
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 3 && fields[0] == "!" &&
			(strings.Contains(fields[2], "(stale info)") || strings.Contains(fields[2], "(non-fast-forward)")) {
			return true
		}
	}
	// A competitor can win after receive-pack advertised refs. That refusal is
	// remote-rejected, with the failed old-OID comparison in stderr, rather
	// than the client-side porcelain "stale info" rejection.
	return strings.Contains(stderr, "cannot lock ref '") &&
		(strings.Contains(stderr, " but expected ") || strings.Contains(stderr, "reference already exists"))
}

func (s *session) pushLease(ctx context.Context, args ...string) error {
	out, diagnostic, err := s.run(ctx, "", append([]string{"push", "--atomic", "--porcelain"}, args...)...)
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && leaseRejected(out, diagnostic) {
		return forge.ErrLeaseRejected
	}
	if strings.TrimSpace(diagnostic) == "" {
		diagnostic = out
	}
	return s.failure("push", diagnostic, err)
}

// Land verifies pinned PR heads and pushes the exact tested commit and a child
// fence token in one atomic push with explicit old-SHA leases on both refs.
func (r *Repo) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	base, err := s.branch(ctx, req.Base)
	if err != nil {
		return "", err
	}
	fence, err := s.branch(ctx, req.Fence)
	if err != nil {
		return "", err
	}
	if base == fence || !validSHA(req.ExpectedBaseSHA) || !validSHA(req.ExpectedFence) || !validSHA(req.TestedSHA) {
		return "", errors.New("distinct refs and full base, tested, and fence SHAs are required")
	}
	if err := s.heads(ctx, req.PRs); err != nil {
		return "", err
	}
	// Git skips lease checks for a destination already equal to the source.
	// Refuse that stale no-op before pushing the other ref. See README.md for
	// the remaining external-writer race inherent in the prescribed CLI push.
	values, err := s.read(ctx, base, fence)
	if err != nil {
		return "", err
	}
	if values[base] == req.TestedSHA || values[base] != req.ExpectedBaseSHA || values[fence] != req.ExpectedFence {
		return "", forge.ErrLeaseRejected
	}
	if err := s.commits(ctx, req.TestedSHA); err != nil {
		return "", err
	}
	next, err := s.fence(ctx, req.ExpectedFence, req.NextFenceID, req.TestedSHA)
	if err != nil {
		return "", err
	}
	if err := s.resolveRemote(ctx, repo); err != nil {
		return "", err
	}
	err = s.pushLease(ctx, "--force-with-lease="+base+":"+req.ExpectedBaseSHA,
		"--force-with-lease="+fence+":"+req.ExpectedFence, "--", s.remote,
		req.TestedSHA+":"+base, next+":"+fence)
	if err != nil {
		return "", err
	}
	return next, nil
}

// AdvanceFence mints a child token and leases the single remote fence ref.
func (r *Repo) AdvanceFence(ctx context.Context, repo forge.RepoRef, ref, expected, nextID string) (string, error) {
	if !validSHA(expected) {
		return "", errors.New("full expected fence SHA is required")
	}
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	full, err := s.branch(ctx, ref)
	if err != nil {
		return "", err
	}
	next, err := s.fence(ctx, expected, nextID, "")
	if err != nil {
		return "", err
	}
	if err := s.resolveRemote(ctx, repo); err != nil {
		return "", err
	}
	if err := s.pushLease(ctx, "--force-with-lease="+full+":"+expected, "--", s.remote, next+":"+full); err != nil {
		return "", err
	}
	return next, nil
}

// EnsureFence creates a root token with a create-only lease. A concurrent
// winner is read back; an existing ref is returned without modifying it.
func (r *Repo) EnsureFence(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
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
	if values[full] != "" {
		return values[full], nil
	}
	next, err := s.fence(ctx, "", "initial", "")
	if err != nil {
		return "", err
	}
	err = s.pushLease(ctx, "--force-with-lease="+full+":", "--", s.remote, next+":"+full)
	if err == nil {
		return next, nil
	}
	if !errors.Is(err, forge.ErrLeaseRejected) {
		return "", err
	}
	values, err = s.read(ctx, full)
	if err != nil {
		return "", err
	}
	if values[full] == "" {
		return "", errors.New("fence disappeared after concurrent bootstrap")
	}
	return values[full], nil
}

// ReadFence reads the current remote token and parses its commit message.
// Malformed and bootstrap messages return empty identity fields; missing refs
// are errors.
func (r *Repo) ReadFence(ctx context.Context, repo forge.RepoRef, ref string) (string, string, string, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", "", "", err
	}
	defer unlock()
	full, err := s.branch(ctx, ref)
	if err != nil {
		return "", "", "", err
	}
	values, err := s.read(ctx, full)
	if err != nil {
		return "", "", "", err
	}
	sha := values[full]
	if sha == "" {
		return "", "", "", errors.New("git fence ref does not exist")
	}
	if err := s.commits(ctx, sha); err != nil {
		return "", "", "", err
	}
	message, diagnostic, err := s.run(ctx, "", "show", "-s", "--format=format:%B", sha)
	if err != nil {
		return "", "", "", s.failure("show", diagnostic, err)
	}
	id, tested := forge.ParseFenceMessage(message)
	return sha, id, tested, nil
}
