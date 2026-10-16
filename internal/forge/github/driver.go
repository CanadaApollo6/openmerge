package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	gh "github.com/google/go-github/v93/github"
)

// Driver implements forge.Driver for repositories belonging to one installation.
// A server must serialize durable actions across processes; GitHub exposes no
// atomic create-if-absent operation for check runs or issue comments.
type Driver struct {
	app          *App
	installation int64
	client       *gh.Client
	writes       sync.Mutex
}

var _ forge.Driver = (*Driver)(nil)

// GitHub repository paths have exactly two components. RepoRef stays opaque to
// the engine; this forge-specific boundary is responsible for interpreting it.
func splitRepo(repo forge.RepoRef) (string, string, error) {
	parts := strings.Split(repo.Path, "/")
	if len(parts) != 2 {
		return "", "", errors.New("github repository must be owner/name")
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", "", errors.New("invalid github repository")
		}
		for _, c := range p {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.ContainsRune("._-", c):
			default:
				return "", "", errors.New("invalid github repository")
			}
		}
	}
	return parts[0], parts[1], nil
}

// PullRequest attributes approvals to GitHub's review commit_id, including stale
// SHAs. Only each user's newest decisive review counts; comments do not revoke it.
func (d *Driver) PullRequest(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	return d.pullRequest(ctx, repo, number, true)
}

// PullRequestMetadata omits changed files when the caller only needs PR metadata.
func (d *Driver) PullRequestMetadata(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	return d.pullRequest(ctx, repo, number, false)
}

func (d *Driver) pullRequest(ctx context.Context, repo forge.RepoRef, number int, files bool) (forge.PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.PullRequest{}, err
	}
	pr, _, err := d.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return forge.PullRequest{}, safeError(err)
	}
	result := pullRequestMetadata(pr)
	if result.Closed {
		return result, nil
	}
	if files && pr.GetChangedFiles() <= forge.MaxChangedFiles {
		result.Files, result.FilesKnown, err = d.changedFiles(ctx, owner, name, number, pr.ChangedFiles)
		if err != nil {
			return forge.PullRequest{}, err
		}
		// The files endpoint is not SHA-pinned. Reject a mixed observation if
		// its diff or condition/trust metadata moved while pages were read.
		after, _, err := d.client.PullRequests.Get(ctx, owner, name, number)
		if err != nil {
			return forge.PullRequest{}, safeError(err)
		}
		if !reflect.DeepEqual(pullRequestMetadata(after), pullRequestMetadata(pr)) || after.GetBase().GetSHA() != pr.GetBase().GetSHA() || !reflect.DeepEqual(after.ChangedFiles, pr.ChangedFiles) {
			return forge.PullRequest{}, errors.New("pull request changed during file observation; retry")
		}
	}
	reviews := make(map[string]*gh.PullRequestReview)
	for page := 1; page != 0; {
		list, res, e := d.client.PullRequests.ListReviews(ctx, owner, name, number, &gh.ListOptions{Page: page, PerPage: 100})
		if e != nil {
			return forge.PullRequest{}, safeError(e)
		}
		for _, review := range list {
			user := review.GetUser().GetLogin()
			switch review.GetState() {
			case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
				if user != "" && (reviews[user] == nil || review.GetID() > reviews[user].GetID()) {
					reviews[user] = review
				}
			}
		}
		page = res.NextPage
	}
	users := make([]string, 0, len(reviews))
	for user := range reviews {
		users = append(users, user)
	}
	slices.Sort(users)
	for _, user := range users {
		review := reviews[user]
		if review.GetState() != "APPROVED" || review.GetCommitID() == "" {
			continue
		}
		write := false
		if !result.HeadInBaseRepo && review.GetCommitID() == result.HeadSHA {
			write, err = d.CanWrite(ctx, repo, user)
			if err != nil {
				return forge.PullRequest{}, err
			}
		}
		result.Approvals = append(result.Approvals, forge.Approval{User: user, SHA: review.GetCommitID(), CanWrite: write})
	}
	return result, nil
}

func pullRequestMetadata(pr *gh.PullRequest) forge.PullRequest {
	result := forge.PullRequest{Number: pr.GetNumber(), HeadSHA: pr.GetHead().GetSHA(), Base: pr.GetBase().GetRef(), Draft: pr.GetDraft(), Author: pr.GetUser().GetLogin(), Closed: pr.GetState() == "closed"}
	result.Title, result.Body, result.HeadBranch = pr.GetTitle(), pr.GetBody(), pr.GetHead().GetLabel()
	result.LabelsKnown = pr.Labels != nil
	for _, label := range pr.Labels {
		if label == nil || label.GetName() == "" {
			result.LabelsKnown = false
			continue
		}
		result.Labels = append(result.Labels, label.GetName())
	}
	if pr.Mergeable != nil {
		result.ConflictKnown, result.Conflict = true, !pr.GetMergeable()
	}
	if result.HeadBranch == "" {
		result.HeadBranch = pr.GetHead().GetRef()
	}
	result.AuthorID = pr.GetUser().GetID()
	if result.AuthorID > 0 && result.Author != "" {
		result.AuthorEmail = fmt.Sprintf("%d+%s@users.noreply.github.com", result.AuthorID, result.Author)
	}
	head, base := pr.GetHead().GetRepo(), pr.GetBase().GetRepo()
	result.HeadInBaseRepo = head.GetID() > 0 && head.GetID() == base.GetID()
	return result
}

func (d *Driver) changedFiles(ctx context.Context, owner, repo string, number int, count *int) ([]string, bool, error) {
	files := []string{}
	seen := make(map[string]bool)
	filenames := make(map[string]bool)
	for page := 1; page != 0; {
		list, response, err := d.client.PullRequests.ListFiles(ctx, owner, repo, number, &gh.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, false, safeError(err)
		}
		for _, file := range list {
			if file.GetFilename() == "" || filenames[file.GetFilename()] || file.GetStatus() == "renamed" && file.GetPreviousFilename() == "" {
				return nil, false, errors.New("incomplete or repeated PR file observation")
			}
			filenames[file.GetFilename()] = true
			for _, path := range []string{file.GetFilename(), file.GetPreviousFilename()} {
				if path != "" && !seen[path] {
					seen[path] = true
					files = append(files, path)
				}
			}
			if len(files) > forge.MaxChangedFiles {
				return nil, false, nil
			}
		}
		if response.NextPage != 0 && (response.NextPage <= page || len(list) == 0) {
			return nil, false, errors.New("PR file pagination did not advance")
		}
		if len(filenames) == forge.MaxChangedFiles && response.NextPage != 0 {
			return nil, false, nil
		}
		page = response.NextPage
	}
	if count != nil && *count != len(filenames) {
		return nil, false, errors.New("PR changed-file count does not match complete file observation; retry")
	}
	return files, true, nil
}

// CanWrite revalidates collaborator permission on every call. The transport may
// reuse a body only after GitHub validates its ETag, never from a cached grant.
func (d *Driver) CanWrite(ctx context.Context, repo forge.RepoRef, user string) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	if user == "" || strings.ContainsAny(user, "/\\?#%\r\n") {
		return false, errors.New("invalid github login")
	}
	level, res, err := d.client.Repositories.GetPermissionLevel(ctx, owner, name, user)
	write := false
	if err != nil {
		if res == nil || res.StatusCode != 404 {
			return false, safeError(err)
		}
	} else {
		switch level.GetPermission() {
		case "admin", "write", "maintain":
			write = true
		}
	}
	return write, nil
}

// ListOpenPullRequests follows every page without fetching reviews or permissions.
func (d *Driver) ListOpenPullRequests(ctx context.Context, repo forge.RepoRef, base string) ([]forge.PullRequestSummary, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	result := []forge.PullRequestSummary{}
	for page := 1; page != 0; {
		prs, res, err := d.client.PullRequests.List(ctx, owner, name, &gh.PullRequestListOptions{State: "open", Base: base, ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, safeError(err)
		}
		for _, pr := range prs {
			result = append(result, forge.PullRequestSummary{Number: pr.GetNumber(), HeadSHA: pr.GetHead().GetSHA(), UpdatedAt: pr.GetUpdatedAt().Time})
		}
		page = res.NextPage
	}
	return result, nil
}

// ListComments includes human and bot comments, retaining IDs for durable dedupe.
func (d *Driver) ListComments(ctx context.Context, repo forge.RepoRef, pr int, since time.Time) ([]forge.Comment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	result := []forge.Comment{}
	for page := 1; page != 0; {
		opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}}
		if !since.IsZero() {
			// GitHub uses an exclusive, second-granular updated_at filter.
			overlap := since.Truncate(time.Second).Add(-time.Second)
			opts.Since = &overlap
		}
		comments, res, err := d.client.Issues.ListComments(ctx, owner, name, pr, opts)
		if err != nil {
			return nil, safeError(err)
		}
		for _, comment := range comments {
			// GitHub filters by updated_at, which also includes edited comments.
			// Commands are discovered by creation time, matching the forge contract.
			if since.IsZero() || !comment.GetCreatedAt().Before(since) {
				result = append(result, forge.Comment{ID: comment.GetID(), Author: comment.GetUser().GetLogin(), Body: comment.GetBody(), CreatedAt: comment.GetCreatedAt().Time})
			}
		}
		page = res.NextPage
	}
	return result, nil
}

func (d *Driver) checkRuns(ctx context.Context, owner, name, sha string) ([]*gh.CheckRun, error) {
	var all []*gh.CheckRun
	for page := 1; page != 0; {
		list, res, err := d.client.Checks.ListCheckRunsForRef(ctx, owner, name, sha, &gh.ListCheckRunsOptions{Filter: new("all"), ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, safeError(err)
		}
		all = append(all, list.CheckRuns...)
		page = res.NextPage
	}
	return all, nil
}
func (d *Driver) own(run *gh.CheckRun) bool {
	return run.GetName() == "OpenMerge" && run.GetApp().GetID() == d.app.id
}

// CheckRuns includes every rerun and legacy status. GitHub assigns increasing
// native IDs within each source's history. Suite state overrides stale run results.
func (d *Driver) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	runs, err := d.checkRuns(ctx, owner, name, sha)
	if err != nil {
		return nil, err
	}
	suites := make(map[int64]*gh.CheckSuite)
	for page := 1; page != 0; {
		list, res, err := d.client.Checks.ListCheckSuitesForRef(ctx, owner, name, sha, &gh.ListCheckSuiteOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, safeError(err)
		}
		for _, suite := range list.CheckSuites {
			suites[suite.GetID()] = suite
		}
		page = res.NextPage
	}
	result := []forge.CheckRun{}
	for _, run := range runs {
		if !d.own(run) {
			state := checkState(run.GetStatus(), run.GetConclusion())
			suite := suites[run.GetCheckSuite().GetID()]
			if run.GetCheckSuite().GetID() <= 0 || suite.GetStatus() != "completed" || run.GetApp().GetID() <= 0 {
				state = forge.Pending
			}
			result = append(result, forge.CheckRun{ID: uint64(max(run.GetID(), 0)), Name: run.GetName(), Source: "app:" + strconv.FormatInt(run.GetApp().GetID(), 10), State: state, HeadBranch: suite.GetHeadBranch()})
		}
	}
	for page := 1; page != 0; {
		statuses, res, e := d.client.Repositories.ListStatuses(ctx, owner, name, sha, &gh.ListOptions{Page: page, PerPage: 100})
		if e != nil {
			return nil, safeError(e)
		}
		for _, status := range statuses {
			state := forge.Pending
			switch status.GetState() {
			case "success":
				state = forge.Success
			case "failure", "error":
				state = forge.Failure
			}
			if status.GetCreator().GetLogin() == "" {
				state = forge.Pending
			}
			result = append(result, forge.CheckRun{ID: uint64(max(status.GetID(), 0)), Name: status.GetContext(), Source: "status:" + strings.ToLower(status.GetCreator().GetLogin()), State: state})
		}
		page = res.NextPage
	}
	return result, nil
}
func checkState(status, conclusion string) forge.CheckState {
	if status != "completed" {
		return forge.Pending
	}
	switch conclusion {
	case "success":
		return forge.Success
	case "failure", "timed_out", "action_required", "startup_failure", "stale":
		return forge.Failure
	case "cancelled":
		return forge.Cancelled
	case "skipped":
		return forge.Skipped
	case "neutral":
		return forge.Neutral
	default:
		return forge.Pending
	}
}

// ReportStatus rediscovers the App-owned external ID on every retry, so a
// successful create followed by a lost response does not require a local cache.
func (d *Driver) ReportStatus(ctx context.Context, repo forge.RepoRef, pr int, headSHA string, s forge.Status) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	status, conclusion := "completed", ""
	switch s.State {
	case forge.Queued, forge.Testing, forge.Landing:
		status = "in_progress"
	case forge.Merged:
		conclusion = "success"
	case forge.Dequeued:
		conclusion = "cancelled"
	case forge.Blocked:
		conclusion = "failure"
	case forge.Paused:
		conclusion = "action_required"
	default:
		return errors.New("unknown OpenMerge status state")
	}
	// Reasons and summaries can quote check names, branches and diagnostics from
	// outside OpenMerge; redact them before they reach a public PR.
	title := string(s.State)
	if s.Reason != "" {
		title += ": " + d.app.sanitizer.Sanitize(s.Reason)
	}
	summary := s.Summary
	if summary == "" {
		summary = title
	} else {
		summary = d.app.sanitizer.SanitizeText(summary)
	}
	output := &gh.CheckRunOutput{Title: &title, Summary: &summary}
	external := "openmerge/pr/" + strconv.Itoa(pr)
	d.writes.Lock()
	defer d.writes.Unlock()
	runs, err := d.checkRuns(ctx, owner, name, headSHA)
	if err != nil {
		return err
	}
	slices.SortFunc(runs, func(a, b *gh.CheckRun) int { return cmp.Compare(b.GetID(), a.GetID()) })
	var completed *gh.Timestamp
	var end *string
	if conclusion != "" {
		end = &conclusion
		completed = &gh.Timestamp{Time: d.app.now()}
	}
	for _, run := range runs {
		if d.own(run) && run.GetExternalID() == external {
			_, _, err = d.client.Checks.UpdateCheckRun(ctx, owner, name, run.GetID(), gh.UpdateCheckRunOptions{Name: "OpenMerge", Status: &status, Conclusion: end, CompletedAt: completed, Output: output})
			return safeError(err)
		}
	}
	_, _, err = d.client.Checks.CreateCheckRun(ctx, owner, name, gh.CreateCheckRunOptions{Name: "OpenMerge", HeadSHA: headSHA, ExternalID: &external, Status: &status, Conclusion: end, CompletedAt: completed, Output: output})
	return safeError(err)
}

// Comment trusts hidden markers only in comments by this App's bot identity.
func (d *Driver) Comment(ctx context.Context, repo forge.RepoRef, pr int, key, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if key == "" || strings.ContainsAny(key, "<>\r\n") || strings.Contains(key, "--") {
		return errors.New("invalid OpenMerge comment key")
	}
	marker := "<!-- openmerge:key=" + key + " -->"
	d.writes.Lock()
	defer d.writes.Unlock()
	for page := 1; page != 0; {
		comments, res, e := d.client.Issues.ListComments(ctx, owner, name, pr, &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if e != nil {
			return safeError(e)
		}
		for _, comment := range comments {
			if comment.GetUser().GetLogin() == d.app.slug+"[bot]" && comment.GetUser().GetType() == "Bot" && strings.Contains(comment.GetBody(), marker) {
				return nil
			}
		}
		page = res.NextPage
	}
	// Comment bodies are redacted like statuses; the marker is OpenMerge's own.
	_, _, err = d.client.Issues.CreateComment(ctx, owner, name, pr, gh.IssueCommentRequest{Body: d.app.sanitizer.SanitizeText(body) + "\n\n" + marker})
	return safeError(err)
}

// MarkLanded links the already-landed commit before closing. A crash between the
// two writes is repaired by the next call; GitHub cannot mark a squash/rebase as
// natively merged after an external push, and no merge endpoint is used.
func (d *Driver) MarkLanded(ctx context.Context, repo forge.RepoRef, pr int, sha string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if err = d.Comment(ctx, repo, pr, "landed/"+sha, fmt.Sprintf("Landed in [%s](%s%s/commit/%s).", sha, d.app.git.String(), repo.Path, sha)); err != nil {
		return err
	}
	current, _, err := d.client.PullRequests.Get(ctx, owner, name, pr)
	if err != nil {
		return safeError(err)
	}
	if current.GetState() == "closed" {
		return nil
	}
	_, _, err = d.client.PullRequests.Edit(ctx, owner, name, pr, &gh.PullRequest{State: new("closed")})
	return safeError(err)
}

// DeleteRef accepts branch names or full refs and treats missing refs as success.
func (d *Driver) DeleteRef(ctx context.Context, repo forge.RepoRef, ref string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	ref = strings.TrimPrefix(ref, "refs/")
	if !strings.HasPrefix(ref, "heads/") && !strings.HasPrefix(ref, "tags/") {
		ref = "heads/" + ref
	}
	res, err := d.client.Git.DeleteRef(ctx, owner, name, ref)
	if err != nil && res != nil {
		if res.StatusCode == 404 {
			return nil
		}
		var api *gh.ErrorResponse
		if res.StatusCode == 422 && errors.As(err, &api) && strings.EqualFold(strings.TrimSuffix(api.Message, "."), "Reference does not exist") {
			return nil
		}
	}
	return safeError(err)
}

// ReadFile reads file contents at an explicit branch or commit, never a download
// URL from the API response. Missing files return an APIError with status 404.
func (d *Driver) ReadFile(ctx context.Context, repo forge.RepoRef, ref, path string) ([]byte, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if ref == "" || path == "" {
		return nil, errors.New("github ReadFile requires ref and path")
	}
	file, _, _, err := d.client.Repositories.GetContents(ctx, owner, name, path, &gh.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		normalized := safeError(err)
		var api *APIError
		if errors.As(normalized, &api) && api.StatusCode == 404 {
			return nil, fmt.Errorf("%w: %w", forge.ErrFileNotFound, normalized)
		}
		return nil, normalized
	}
	if file == nil || file.GetType() != "file" {
		return nil, errors.New("github content is not a file")
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, safeError(err)
	}
	return []byte(content), nil
}

// Remote supplies gitops credentials separately from the credential-free URL.
func (d *Driver) Remote(ctx context.Context, repo forge.RepoRef) (string, gitops.Auth, error) {
	if _, _, err := splitRepo(repo); err != nil {
		return "", gitops.Auth{}, err
	}
	token, err := d.app.token(ctx, d.installation)
	if err != nil {
		return "", gitops.Auth{}, err
	}
	return d.app.git.String() + repo.Path + ".git", gitops.Auth{Username: "x-access-token", Password: token}, nil
}

// HeadRef maps a GitHub pull request to its fetchable head ref.
func HeadRef(pr int) string { return fmt.Sprintf("refs/pull/%d/head", pr) }

// BotLogin identifies comments authored by this App.
func (d *Driver) BotLogin() string { return d.app.slug + "[bot]" }

// Committer resolves the App's bot user, whose ID differs from the App and
// installation IDs. Callers can fall back or supply an explicit identity.
func (d *Driver) Committer(ctx context.Context) (gitops.Identity, error) {
	login := d.BotLogin()
	user, _, err := d.client.Users.Get(ctx, login)
	if err != nil {
		return gitops.Identity{}, safeError(err)
	}
	if user.GetID() <= 0 || user.GetLogin() != login {
		return gitops.Identity{}, errors.New("github App bot user identity does not match configuration")
	}
	return gitops.Identity{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", user.GetID(), login)}, nil
}
