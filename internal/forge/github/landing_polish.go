package github

import (
	"context"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// CommitAuthors reads linked users only when composing a test commit, without
// adding commit-list requests to reconciliation. GitHub caps this at 250 commits;
// commits absent from the response retain git email-based attribution.
func (d *Driver) CommitAuthors(ctx context.Context, repo forge.RepoRef, number int) ([]forge.CommitAuthor, error) {

	if err := forge.ValidatePRNumber(number); err != nil {
		return nil, err
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	var result []forge.CommitAuthor
	for page := 1; page != 0; {
		commits, res, err := d.client.PullRequests.ListCommits(ctx, owner, name, number, &gh.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, safeError(err)
		}
		for _, commit := range commits {
			result = append(result, forge.CommitAuthor{SHA: commit.GetSHA(), Login: commit.GetAuthor().GetLogin(), ID: commit.GetAuthor().GetID()})
		}
		page = res.NextPage
	}
	return result, nil
}

// HeadBranchForDeletion returns a plain branch name, never the owner:branch
// label used in merge messages. An unavailable safety read prevents deletion.
func (d *Driver) HeadBranchForDeletion(ctx context.Context, repo forge.RepoRef, number int, headSHA, base string) (string, error) {

	if err := forge.ValidatePRNumber(number); err != nil {
		return "", err
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	pr, _, err := d.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return "", safeError(err)
	}
	head, target := pr.GetHead(), pr.GetBase()
	branch := head.GetRef()
	if pr.GetNumber() != number || target.GetRef() != base || head.GetSHA() != headSHA || head.GetRepo().GetID() <= 0 || head.GetRepo().GetID() != target.GetRepo().GetID() ||
		branch == "" || branch == base || branch == target.GetRef() || strings.HasPrefix(branch, "openmerge/") || strings.HasPrefix(branch, "refs/") {
		return "", nil
	}
	if pr.GetState() != "closed" {
		return "", errors.New("head cleanup is waiting for PR closure")
	}
	repository, _, err := d.client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return "", safeError(err)
	}
	if repository.GetID() != target.GetRepo().GetID() || repository.GetDefaultBranch() == "" || branch == repository.GetDefaultBranch() {
		return "", nil
	}
	b, res, err := d.client.Repositories.GetBranch(ctx, owner, name, branch, 0)
	if err != nil {
		if res != nil && res.StatusCode == 404 {
			return "", nil // already deleted
		}
		return "", safeError(err)
	}
	if b.Protected == nil || b.GetProtected() || b.GetName() != branch || b.GetCommit().GetSHA() != headSHA {
		return "", nil
	}
	prs, err := d.ListOpenPullRequests(ctx, repo, branch)
	if err != nil {
		return "", err
	}
	if len(prs) > 0 {
		return "", nil
	}
	// The head can also be shared by a PR targeting a different base. Scope
	// the head to this repository's owner so a fork's namesake is unrelated.
	users, _, err := d.client.PullRequests.List(ctx, owner, name, &gh.PullRequestListOptions{
		State: "open", Head: owner + ":" + branch, ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return "", safeError(err)
	}
	if len(users) > 0 {
		return "", nil
	}
	return branch, nil
}
