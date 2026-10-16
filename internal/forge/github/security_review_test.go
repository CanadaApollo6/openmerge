package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	gh "github.com/google/go-github/v93/github"
)

func reviewTrust(pr forge.PullRequest) bool {
	if pr.HeadInBaseRepo {
		return true
	}
	for _, a := range pr.Approvals {
		if a.CanWrite && a.SHA == pr.HeadSHA {
			return true
		}
	}
	return false
}

func TestReviewRevokedWriterCannotApproveNewHead(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	before, err := f.driver.PullRequest(ctx, testRepo, 1)
	must(t, err)
	if !reviewTrust(before) {
		t.Fatal("fixture was not initially trusted")
	}
	f.mu.Lock()
	f.permissions["writer"] = "read"
	f.clock = f.clock.Add(30 * time.Second)
	next := strings.Repeat("b", 40)
	f.prs[1].Head.SHA = &next
	f.reviews[1] = append(f.reviews[1], &gh.PullRequestReview{ID: new(int64(10)), State: new("APPROVED"), CommitID: &next, User: &gh.User{Login: new("writer")}})
	f.mu.Unlock()
	after, err := f.driver.PullRequest(ctx, testRepo, 1)
	must(t, err)
	if reviewTrust(after) {
		t.Errorf("REPRO: revoked writer approved NEW fork head: %+v; permission API calls=%d", after, f.permissionCalls["writer"])
	}
}

func reviewRefresh(f *apiFixture) ([]queue.Event, error) {
	x := executor.Executor{Driver: f.driver, Repo: testRepo, Base: "main", RequiredChecks: []string{"CI"}}
	var events []queue.Event
	err := x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: "tested"}, func(e queue.Event) error { events = append(events, e); return nil })
	return events, err
}

func reviewCompletedSuccess(events []queue.Event) bool {
	for _, e := range events {
		if c, ok := e.(queue.ChecksCompleted); ok && c.Success {
			return true
		}
	}
	return false
}

func TestReviewOtherAppCannotReplaceFailingCheck(t *testing.T) {
	f := newAPI(t)
	f.runs["tested"] = []*gh.CheckRun{
		{ID: new(int64(100)), Name: new("CI"), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: new(int64(11))}},
		{ID: new(int64(101)), Name: new("CI"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(int64(22))}},
	}
	events, err := reviewRefresh(f)
	var ambiguity *executor.CheckSourceAmbiguityError
	if !errors.As(err, &ambiguity) || ambiguity.Name != "CI" || reviewCompletedSuccess(events) {
		t.Fatalf("other App masked failing CI: events=%+v err=%v", events, err)
	}
}

func TestReviewRerequestedSuiteCannotReuseSuccess(t *testing.T) {
	f := newAPI(t)
	f.runs["tested"] = []*gh.CheckRun{{ID: new(int64(100)), Name: new("CI"), Status: new("completed"), Conclusion: new("success"), CheckSuite: &gh.CheckSuite{ID: new(int64(9))}, App: &gh.App{ID: new(int64(11))}}}
	f.suites["tested"] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("queued"), HeadSHA: new("tested"), App: &gh.App{ID: new(int64(11))}}}
	payload := []byte(`{"action":"rerequested","repository":{"full_name":"acme/project"},"installation":{"id":7},"check_suite":{"id":9,"head_sha":"tested","status":"queued","conclusion":null}}`)
	event, err := VerifyAndParse(signedRequest("check_suite", payload), webhookSecret)
	must(t, err)
	if p, ok := event.Payload.(ChecksChanged); !ok || p.SHA != "tested" {
		t.Fatalf("rerun ignored: %T", event.Payload)
	}
	events, err := reviewRefresh(f)
	must(t, err)
	if reviewCompletedSuccess(events) {
		t.Errorf("REPRO: queued rerun emitted success; suite reads=%d; webhook=%T; events=%+v", f.calls["GET repos/acme/project/commits/tested/check-suites"], event.Payload, events)
	}
}

func TestReviewCrossAPICollisionFailsClosedInExecutor(t *testing.T) {
	f := newAPI(t)
	f.runs["tested"] = []*gh.CheckRun{{ID: new(int64(100)), Name: new("CI"), Status: new("completed"), Conclusion: new("success")}}
	f.statuses["tested"] = []*gh.RepoStatus{{ID: new(int64(101)), Context: new("CI"), State: new("pending")}}
	events, err := reviewRefresh(f)
	var collision *executor.CheckSourceAmbiguityError
	if !errors.As(err, &collision) || len(events) != 2 || reviewCompletedSuccess(events) {
		t.Fatalf("not fail closed: events=%+v err=%v", events, err)
	}
}

func TestReviewRepositoryIdentityControls(t *testing.T) {
	for _, mode := range []string{"same-name-fork", "deleted-fork", "renamed-base", "transferred-base"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			f.reviews[1] = nil
			f.prs[1].Head.Repo.Name = new("project")
			f.prs[1].Base.Repo.Name = new("project")
			switch mode {
			case "deleted-fork":
				f.prs[1].Head.Repo = nil
			case "renamed-base":
				f.prs[1].Base.Repo.Name = new("renamed")
			case "transferred-base":
				f.prs[1].Base.Repo.FullName = new("other-owner/project")
			}
			pr, err := f.driver.PullRequest(context.Background(), testRepo, 1)
			must(t, err)
			if reviewTrust(pr) {
				t.Fatalf("unapproved fork trusted: %+v", pr)
			}
		})
	}
}

func TestReviewWebhookHeaderObservations(t *testing.T) {
	body := webhookFixture(t, "pull_request")
	for _, mode := range []string{"duplicate-signature", "duplicate-delivery", "missing-event", "changed-delivery"} {
		t.Run(mode, func(t *testing.T) {
			r := signedRequest("pull_request", body)
			switch mode {
			case "duplicate-signature":
				r.Header.Add("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
			case "duplicate-delivery":
				r.Header.Add("X-GitHub-Delivery", "other")
			case "missing-event":
				r.Header.Del("X-GitHub-Event")
			case "changed-delivery":
				r.Header.Set("X-GitHub-Delivery", "replay-new-id")
			}
			e, err := VerifyAndParse(r, webhookSecret)
			t.Logf("mode=%s delivery=%q payload=%T error=%v", mode, e.DeliveryID, e.Payload, err)
		})
	}
}
