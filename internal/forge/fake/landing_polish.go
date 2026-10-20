package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CommitAuthors returns linked users, defaulting to unlinked commit identities.
func (f *Forge) CommitAuthors(ctx context.Context, repo forge.RepoRef, number int) ([]forge.CommitAuthor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CommitAuthors"); err != nil {
		return nil, err
	}

	if err := forge.ValidatePRNumber(number); err != nil {
		return nil, err
	}
	r := f.repo(repo)
	if _, ok := r.prs[number]; !ok {
		return nil, fmt.Errorf("unknown PR %d", number)
	}
	var authors []forge.CommitAuthor
	for _, sha := range r.prCommits[number] {
		author := r.authors[sha]
		author.SHA = sha
		authors = append(authors, author)
	}
	return authors, nil
}

// SetCommitAuthor associates a commit with a forge user for attribution tests.
func (f *Forge) SetCommitAuthor(repo forge.RepoRef, author forge.CommitAuthor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(repo).authors[author.SHA] = author
}

// SetBranchProtected supplies the branch-protection read in cleanup tests.
func (f *Forge) SetBranchProtected(repo forge.RepoRef, branch string, protected bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(repo).protected[branch] = protected
}

// HeadBranchForDeletion preserves forks, protected heads and dependency branches.
func (f *Forge) HeadBranchForDeletion(ctx context.Context, repo forge.RepoRef, number int, headSHA, base string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "HeadBranchForDeletion"); err != nil {
		return "", err
	}

	if err := forge.ValidatePRNumber(number); err != nil {
		return "", err
	}
	r := f.repo(repo)
	pr, ok := r.prs[number]
	if !ok {
		return "", fmt.Errorf("unknown PR %d", number)
	}
	branch := pr.HeadBranch
	if !pr.HeadInBaseRepo || pr.Base != base || pr.HeadSHA != headSHA || branch == "" || branch == base || branch == pr.Base || strings.HasPrefix(branch, "openmerge/") || strings.HasPrefix(branch, "refs/") || r.protected[branch] {
		return "", nil
	}
	if !pr.Closed {
		return "", fmt.Errorf("head cleanup is waiting for PR closure")
	}
	if r.defaultBranch == "" || branch == r.defaultBranch || r.refs[branch] != headSHA {
		return "", nil
	}
	for _, other := range r.prs {
		if !other.Closed && (other.Base == branch || other.HeadInBaseRepo && other.HeadBranch == branch) {
			return "", nil
		}
	}
	return branch, nil
}

// DeleteBranch applies the same old-SHA lease as the real git transport.
func (f *Forge) DeleteBranch(ctx context.Context, repo forge.RepoRef, branch, expectedSHA string, opts forge.PushOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "DeleteBranch"); err != nil {
		return err
	}

	if err := forge.ValidateDeleteBranch(branch, expectedSHA); err != nil {
		return err
	}
	r := f.repo(repo)
	if r.refs[branch] == expectedSHA {
		ref := "refs/heads/" + branch
		if err := opts.CheckDispatch(ctx, repo, forge.PushDeleteBranch, []forge.PushRefIntent{{Ref: ref, OldSHA: expectedSHA, Delete: true}}); err != nil {
			return err
		}
		if r.protected[branch] {
			return errors.New("protected branch cannot be deleted")
		}
		if err := r.validatePush(ctx, repo, opts, forge.PushDeleteBranch, forge.PushRef{Ref: ref, OldSHA: expectedSHA}); err != nil {
			return err
		}
		delete(r.refs, branch)
	}
	return nil
}
