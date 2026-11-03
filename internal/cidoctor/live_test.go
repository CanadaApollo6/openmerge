package cidoctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func liveFixture(t *testing.T, n int, rule string) (store.Journal, Actions, *fake.Forge) {
	t.Helper()
	j, a := fixture(t, n, rule)
	f := fake.New()
	repo := forge.RepoRef{Path: j.Key.Repository}
	base, _ := f.Init(repo, "main", "openmerge/fence/main")
	workflow := "jobs:\n  check:\n    runs-on: ubuntu-latest\n  fast:\n    runs-on: ubuntu-latest\n"
	if rule == "masked-skip" {
		workflow = "jobs:\n  check:\n    needs: fast\n    runs-on: ubuntu-latest\n  fast:\n    runs-on: ubuntu-latest\n"
	}
	pr, err := f.OpenPRWithFiles(repo, 99, base, true, map[string]string{
		".github/workflows/ci.yml": workflow,
		".openmerge.yml":           "version: 1\nqueues:\n  - name: default\n    merge_when:\n      - check-success = check\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Runs = slices.DeleteFunc(a.Runs, func(r Run) bool { return r.Event == "pull_request" })
	for _, r := range a.Runs {
		if err := f.PushRef(repo, r.HeadSHA, pr.HeadSHA); err != nil {
			t.Fatal(err)
		}
	}
	var events []store.JournalEvent
	for _, e := range j.Events {
		events = append(events, e)
		if push, ok := e.Event.(queue.TestRefBuilt); ok {
			for _, r := range a.Runs {
				if r.HeadSHA == push.TestedSHA && r.HeadBranch != "main" {
					events = append(events, store.JournalEvent{RecordedAt: e.RecordedAt, Event: queue.ChecksObserved{BatchID: push.BatchID, TestedSHA: push.TestedSHA, Runs: []queue.CheckRun{{WorkflowRunID: r.ID, Name: "check", State: "pending"}}}})
				}
			}
		}
	}
	j.Events = events
	f.SetActions(repo, a.Runs)
	return j, a, f
}

func TestLiveActionsReportParity(t *testing.T) {
	for _, rule := range []string{"long-pole", "serial-step", "cold-setup", "checkout-cost", "masked-skip"} {
		t.Run(rule, func(t *testing.T) {
			j, recorded, f := liveFixture(t, 10, rule)
			live, err := ReadActions(t.Context(), f, j, window, DefaultActionsAPIBudget)
			if err != nil {
				t.Fatal(err)
			}
			// Model the file path by decoding its recorded JSON contract.
			data, err := json.Marshal(recorded)
			if err != nil {
				t.Fatal(err)
			}
			var file Actions
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatal(err)
			}
			want, got := compute(t, j, file), compute(t, j, live)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("live report differs\ngot %+v\nwant %+v", got, want)
			}
			a, b := new(bytes.Buffer), new(bytes.Buffer)
			if err := WriteText(a, got); err != nil {
				t.Fatal(err)
			}
			if err := WriteText(b, want); err != nil {
				t.Fatal(err)
			}
			if a.String() != b.String() {
				t.Fatal("text output differs")
			}
			if !slices.Contains(codes(got), rule) {
				t.Fatal("parity must exercise real findings")
			}
		})
	}
}

func TestLiveActionsBudgetAndPermissionGaps(t *testing.T) {
	for _, tc := range []struct {
		name    string
		budget  int
		failure string
		want    string
		calls   int
	}{
		{"budget", 2, "", "actions-budget", 1},
		{"run permission", 100, "ActionsRun", "actions-permission", 4},
		{"jobs permission", 100, "ActionsJobs", "actions-permission", 7},
		{"baseline permission", 100, "DefaultBranchActions", "actions-permission", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, _, f := liveFixture(t, 2, "long-pole")
			if tc.failure != "" {
				f.FailNext(tc.failure, forge.ErrActionsUnavailable)
			}
			a, err := ReadActions(t.Context(), f, j, window, tc.budget)
			if err != nil {
				t.Fatal(err)
			}
			r := compute(t, j, a)
			if !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.Code == tc.want }) {
				t.Fatalf("gaps %+v", r.Gaps)
			}
			if tc.name == "budget" {
				if r.QueueRuns != 0 || len(r.Jobs) != 0 || f.ActionsReadCalls(forge.RepoRef{Path: j.Key.Repository}) != tc.calls {
					t.Fatalf("budget admitted incomplete evidence %+v", r)
				}
				for _, id := range []uint64{1, 2} {
					if !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.RunID == id && g.Code == "actions-budget" }) {
						t.Fatalf("remaining run %d not labelled", id)
					}
				}
			}
		})
	}
}

func TestLiveActionsAttemptsAndPagination(t *testing.T) {
	j, a, f := liveFixture(t, 1, "long-pole")
	repo := forge.RepoRef{Path: j.Key.Repository}
	first := a.Runs[0]
	first.Jobs = nil
	for i := 0; i < 101; i++ {
		job := a.Runs[0].Jobs[0]
		job.ID = uint64(100 + i)
		job.Name = fmt.Sprintf("job-%d", i)
		first.Jobs = append(first.Jobs, job)
	}
	retry := first
	retry.Attempt = 2
	retry.Jobs = slices.Clone(first.Jobs[:1])
	retry.Jobs[0].Attempt = 2
	runs := []Run{first, retry}
	for i := 0; i < 101; i++ {
		warm := a.Runs[1]
		warm.ID = uint64(1000 + i)
		runs = append(runs, warm)
	}
	f.SetActions(repo, runs)
	live, err := ReadActions(t.Context(), f, j, window, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Runs) != 103 || live.Runs[0].Attempt != 1 || len(live.Runs[0].Jobs) != 101 || !live.Runs[0].JobsComplete || live.Runs[1].Attempt != 2 {
		t.Fatalf("attempts or pagination lost: %d runs", len(live.Runs))
	}
	// DefaultBranch (1), run (1), first jobs (2), workflow/policy files (5),
	// second jobs (1), baseline pages (2), baseline jobs (101).
	limited, err := ReadActions(t.Context(), f, j, window, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Runs) != 0 || !slices.ContainsFunc(limited.Gaps, func(g Gap) bool { return g.RunID == first.ID && g.Code == "actions-budget" }) {
		t.Fatalf("partial page leaked %+v", limited)
	}
}

func TestLiveActionsFailureAndRetries(t *testing.T) {
	j, _, f := liveFixture(t, 1, "long-pole")
	for i := 0; i < 3; i++ {
		f.FailNext("ActionsJobs", errors.New("network unavailable"))
	}
	if _, err := ReadActions(t.Context(), f, j, window, 100); err == nil {
		t.Fatal("network failure became a gap")
	}
	f.FailNext("ActionsJobs", errors.New("transient network failure"))
	if _, err := ReadActions(t.Context(), f, j, window, 100); err != nil {
		t.Fatal(err)
	}
	f.FailNext("ActionsJobs", errors.New("transient network failure"))
	a, err := ReadActions(t.Context(), f, j, window, 3)
	if err != nil || len(a.Runs) != 0 || len(a.Gaps) == 0 {
		t.Fatalf("retry exceeded budget %+v %v", a, err)
	}
}

func TestLiveActionsDoesNotDiscoverUnidentifiedRuns(t *testing.T) {
	j, _, f := liveFixture(t, 1, "long-pole")
	j.Events = slices.DeleteFunc(j.Events, func(e store.JournalEvent) bool { _, ok := e.Event.(queue.ChecksObserved); return ok })
	a, err := ReadActions(t.Context(), f, j, window, 100)
	if err != nil {
		t.Fatal(err)
	}
	if compute(t, j, a).QueueRuns != 0 || f.ActionsReadCalls(forge.RepoRef{Path: j.Key.Repository}) != 2 {
		t.Fatal("unidentified queue run was discovered")
	}
}

type incompleteJobsReader struct{ forge.ActionsReader }

func (r incompleteJobsReader) ActionsJobs(ctx context.Context, repo forge.RepoRef, id, attempt uint64, page int) (forge.ActionsJobsPage, error) {
	jobs, err := r.ActionsReader.ActionsJobs(ctx, repo, id, attempt, page)
	jobs.Total++
	return jobs, err
}

func TestLiveActionsIncompleteJobsAreGaps(t *testing.T) {
	j, _, f := liveFixture(t, 1, "long-pole")
	a, err := ReadActions(t.Context(), incompleteJobsReader{f}, j, window, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := compute(t, j, a)
	if r.QueueRuns != 0 || len(r.Jobs) != 0 || !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.RunID == 1 && g.Code == "actions-data" }) {
		t.Fatalf("incomplete evidence used %+v", r)
	}
}

func TestLiveActionsRejectsMismatchedJournalIdentity(t *testing.T) {
	j, a, f := liveFixture(t, 1, "long-pole")
	a.Runs[0].HeadSHA = "unrelated"
	f.SetActions(forge.RepoRef{Path: j.Key.Repository}, a.Runs)
	live, err := ReadActions(t.Context(), f, j, window, 100)
	if err != nil {
		t.Fatal(err)
	}
	r := compute(t, j, live)
	if r.QueueRuns != 0 || len(r.Jobs) != 0 || !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.RunID == 1 && g.Code == "run-identity" }) {
		t.Fatalf("mismatched evidence used %+v", r)
	}
}

type statusError int

func (e statusError) Error() string       { return fmt.Sprintf("HTTP %d", int(e)) }
func (e statusError) HTTPStatusCode() int { return int(e) }

func TestLiveActionsRepositoryAccessIsGap(t *testing.T) {
	for _, status := range []statusError{403, 404} {
		j, _, f := liveFixture(t, 1, "long-pole")
		f.FailNext("DefaultBranch", status)
		a, err := ReadActions(t.Context(), f, j, window, 100)
		if err != nil {
			t.Fatalf("HTTP %d: %v", status, err)
		}
		if len(a.Runs) != 0 || !slices.ContainsFunc(a.Gaps, func(g Gap) bool { return g.Code == "actions-access" }) {
			t.Fatalf("HTTP %d: %+v", status, a)
		}
	}
}

type duplicateBaselineReader struct{ forge.ActionsReader }

// The last entry of page 1 repeats on page 2 in place of an omitted run, so the
// entry count still matches Total.
func (r duplicateBaselineReader) DefaultBranchActions(ctx context.Context, repo forge.RepoRef, branch string, since, until time.Time, page int) (forge.ActionsRunsPage, error) {
	runs, err := r.ActionsReader.DefaultBranchActions(ctx, repo, branch, since, until, page)
	if err == nil && page == 2 && len(runs.Runs) > 0 {
		prev, _ := r.ActionsReader.DefaultBranchActions(ctx, repo, branch, since, until, 1)
		runs.Runs[0] = prev.Runs[len(prev.Runs)-1]
	}
	return runs, err
}

func TestLiveActionsDuplicateBaselineIsGap(t *testing.T) {
	j, a, f := liveFixture(t, 1, "long-pole")
	runs := []Run{a.Runs[0]}
	for i := 0; i < 101; i++ {
		warm := a.Runs[1]
		warm.ID = uint64(1000 + i)
		runs = append(runs, warm)
	}
	f.SetActions(forge.RepoRef{Path: j.Key.Repository}, runs)
	live, err := ReadActions(t.Context(), duplicateBaselineReader{f}, j, window, 500)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(live.Runs, func(r Run) bool { return r.HeadBranch == "main" }) || !slices.ContainsFunc(live.Gaps, func(g Gap) bool { return g.Code == "baseline-data" }) {
		t.Fatalf("duplicate baseline inventory used: %d runs, gaps %+v", len(live.Runs), live.Gaps)
	}
}

func TestLiveActionsReadsOnlyGraphEvidence(t *testing.T) {
	for path, want := range map[string]bool{
		".github/workflows/ci.yml":         true,
		".github/workflows/ci.yaml":        true,
		".openmerge.yml":                   true,
		".github/openmerge.yaml":           true,
		".github/workflows/nested/ci.yml":  false,
		".github/workflows/../secrets.yml": false,
		".github/workflows/.yml.txt":       false,
		".env":                             false,
		"dynamic/pages/pages-build":        false,
	} {
		if reportReadable(path) != want {
			t.Errorf("reportReadable(%q) = %v", path, !want)
		}
	}
	j, a, f := liveFixture(t, 1, "long-pole")
	a.Runs[0].Path = "config/secrets.yml"
	f.SetActions(forge.RepoRef{Path: j.Key.Repository}, a.Runs)
	reader := &pathRecorder{ActionsReader: f}
	live, err := ReadActions(t.Context(), reader, j, window, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Runs) == 0 || live.Runs[0].Graph != nil {
		t.Fatalf("out-of-scope workflow path produced a graph: %+v", live.Runs)
	}
	for _, path := range reader.paths {
		if !reportReadable(path) {
			t.Fatalf("read %q outside graph evidence", path)
		}
	}
}

type pathRecorder struct {
	forge.ActionsReader
	paths []string
}

func (r *pathRecorder) ReadFile(ctx context.Context, repo forge.RepoRef, ref, path string) ([]byte, error) {
	r.paths = append(r.paths, path)
	return r.ActionsReader.ReadFile(ctx, repo, ref, path)
}
