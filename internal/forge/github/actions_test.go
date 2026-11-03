package github

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestActionsReaderUsesInstallationCacheAndPages(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/actions/") {
			return false
		}
		if r.Method != http.MethodGet {
			t.Error("Actions reader attempted a write")
		}
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/attempts/2/jobs"):
			if q.Get("per_page") != "100" || q.Get("page") != "1" {
				t.Errorf("jobs query %s", r.URL.RawQuery)
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/acme/project/actions/runs/123/attempts/2/jobs?per_page=100&page=2>; rel="next"`, f.server.URL))
			_, _ = fmt.Fprint(w, `{"total_count":101,"jobs":[{"id":12,"name":"test","run_attempt":2,"created_at":"2026-10-16T00:00:00Z","steps":[{"name":"Run tests","number":1}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/runs/123"):
			_, _ = fmt.Fprint(w, `{"id":123,"workflow_id":90,"path":".github/workflows/ci.yml","run_attempt":2,"event":"push","head_branch":"openmerge/queue/main/b1","head_sha":"abc","created_at":"2026-10-16T00:00:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			if q.Get("branch") != "main" || q.Get("event") != "push" || q.Get("created") != "2026-10-16T00:00:00Z..2026-10-16T01:00:00Z" || q.Get("page") != "2" {
				t.Errorf("baseline query %s", r.URL.RawQuery)
			}
			_, _ = fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":124,"run_attempt":1}]}`)
		default:
			t.Errorf("unexpected read %s", r.URL.Path)
		}
		return true
	}
	ctx := forge.WithRequestClass(t.Context(), forge.ReportingRequest)
	run, err := f.driver.ActionsRun(ctx, testRepo, 123)
	must(t, err)
	if run.ID != 123 || run.Attempt != 2 || run.WorkflowID != 90 || run.HeadSHA != "abc" {
		t.Fatalf("metadata %+v", run)
	}
	for i := 0; i < 2; i++ {
		jobs, err := f.driver.ActionsJobs(ctx, testRepo, 123, 2, 1)
		must(t, err)
		if jobs.Total != 101 || jobs.NextPage != 2 || len(jobs.Jobs) != 1 || jobs.Jobs[0].Attempt != 2 || len(jobs.Jobs[0].Steps) != 1 {
			t.Fatalf("jobs %+v", jobs)
		}
	}
	runs, err := f.driver.DefaultBranchActions(ctx, testRepo, "main", epoch, epoch.Add(time.Hour), 2)
	must(t, err)
	if len(runs.Runs) != 1 || runs.Total != 1 || runs.NextPage != 0 {
		t.Fatalf("runs %+v", runs)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notModified != 1 || f.tokens != 1 {
		t.Fatalf("cache/auth not reused: 304=%d tokens=%d", f.notModified, f.tokens)
	}
}

func TestActionsReaderPermissionGaps(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/actions/") {
					f.apiError(w, status, "untrusted response body secret")
					return true
				}
				return false
			}
			for _, read := range []func() error{
				func() error { _, err := f.driver.ActionsRun(t.Context(), testRepo, 1); return err },
				func() error { _, err := f.driver.ActionsJobs(t.Context(), testRepo, 1, 1, 1); return err },
				func() error {
					_, err := f.driver.DefaultBranchActions(t.Context(), testRepo, "main", epoch, epoch.Add(time.Hour), 1)
					return err
				},
			} {
				err := read()
				if err == nil || strings.Contains(err.Error(), "secret") || errors.Is(err, forge.ErrActionsUnavailable) != (status != 500) {
					t.Fatalf("incorrect error %v", err)
				}
			}
		})
	}
	f := newAPI(t)
	_, err := f.app.token(t.Context(), 7)
	must(t, err)
	entry := f.app.tokens[7]
	entry.mu.Lock()
	delete(entry.permissions, "actions")
	entry.mu.Unlock()
	_, err = f.driver.ActionsJobs(t.Context(), testRepo, 1, 1, 1)
	if !errors.Is(err, forge.ErrActionsUnavailable) {
		t.Fatalf("missing grant %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls["GET repos/acme/project/actions/runs/1/attempts/1/jobs"] != 0 {
		t.Fatal("read attempted despite missing grant")
	}
}

func TestActionsReaderYieldsToReportingQuotaFloor(t *testing.T) {
	f := newAPI(t)
	_, err := f.app.token(t.Context(), 7)
	must(t, err)
	transport := f.driver.client.Client().Transport.(*transport)
	transport.mu.Lock()
	transport.budgetKnown = true
	transport.limit, transport.remaining = 1000, 250
	transport.reset = f.clock.Add(time.Hour)
	transport.mu.Unlock()
	_, err = f.driver.ActionsRun(t.Context(), testRepo, 1)
	var rate *RateLimitError
	if !errors.As(err, &rate) {
		t.Fatalf("reporting quota not honored: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls["GET repos/acme/project/actions/runs/1"] != 0 {
		t.Fatal("report consumed protected queue quota")
	}
}
