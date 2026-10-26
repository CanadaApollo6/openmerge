package main

import (
	"bytes"
	"flag"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/google/go-github/v93/github"
)

func smartfireWorkflows() ciWorkflows {
	return ciWorkflows{
		".github/workflows/frontend.yml",
		".github/workflows/rust.yml",
		".github/workflows/repo.yml",
		".github/workflows/oauth-browser.yml",
	}
}

func TestCIWorkflowFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"default", nil, []string{defaultCIWorkflow}},
		{"explicit default", []string{"--ci-workflow", defaultCIWorkflow}, []string{defaultCIWorkflow}},
		{"replace default", []string{"--ci-workflow", "frontend.yml"}, []string{"frontend.yml"}},
		{"repeat", []string{"--ci-workflow", "frontend.yml", "--ci-workflow", "rust.yml"}, []string{"frontend.yml", "rust.yml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			var o options
			o.flags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(o.ciWorkflows.paths(), tc.want) {
				t.Fatalf("paths = %v, want %v", o.ciWorkflows.paths(), tc.want)
			}
		})
	}
	for _, command := range []string{"audit", "report"} {
		for _, args := range [][]string{
			{"--ci-workflow", ""},
			{"--ci-workflow", " \t"},
			{"--ci-workflow", "rust.yml", "--ci-workflow", "rust.yml"},
			{"--ci-workflow", defaultCIWorkflow, "--ci-workflow", defaultCIWorkflow},
		} {
			var out, errs bytes.Buffer
			if code := run(t.Context(), append([]string{command}, args...), &out, &errs); code != 1 || !strings.Contains(errs.String(), "CI workflow path") || out.Len() != 0 {
				t.Fatalf("%s %q: code=%d out=%q errors=%q", command, args, code, &out, &errs)
			}
		}
	}
}

func TestQueueCIWorkflowAndBranch(t *testing.T) {
	workflows := smartfireWorkflows()
	for _, path := range append(slices.Clone(workflows), defaultCIWorkflow, "") {
		for _, branch := range []string{"openmerge/queue/main/b1", "openmerge/queue/release/b2", "main", "openmerge/queue-other/b1", ""} {
			run := &github.WorkflowRun{Path: new(path), HeadBranch: new(branch)}
			want := slices.Contains(workflows, path) && strings.HasPrefix(branch, "openmerge/queue/")
			if queueCI(run, workflows) != want {
				t.Fatalf("queueCI(%q, %q) != %t", path, branch, want)
			}
		}
	}
}

func TestAuditEveryCIWorkflow(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := auditFixture(t, queue.Merge, base, head, head, head)
	workflows := smartfireWorkflows()
	for _, mode := range []string{"all passing", "missing", "two missing", "failed", "rerun succeeded", "too late", "at deadline", "not completed", "wrong branch"} {
		t.Run(mode, func(t *testing.T) {
			a := &fakeActions{historical: map[[2]int64]*github.WorkflowRun{}}
			var missing []string
			for i, path := range workflows {
				run := runFixture(int64(i+1), 1, 10, 11, 20, head)
				run.Path = new(path)
				if i == 3 {
					switch mode {
					case "missing", "two missing":
						missing = append(missing, path)
						continue
					case "failed":
						run.Conclusion = new("failure")
					case "rerun succeeded":
						failed := *run
						failed.Conclusion = new("failure")
						a.historical[[2]int64{run.GetID(), 1}] = &failed
						run.RunAttempt = new(2)
					case "too late":
						run.UpdatedAt = &github.Timestamp{Time: epoch.Add(35 * time.Second)}
					case "at deadline":
						run.UpdatedAt = &github.Timestamp{Time: epoch.Add(30 * time.Second)}
					case "not completed":
						run.Status = new("in_progress")
					case "wrong branch":
						run.HeadBranch = new("main")
					}
					if mode != "all passing" && mode != "rerun succeeded" {
						missing = append(missing, path)
					}
				}
				if i == 0 && mode == "two missing" {
					missing = append(missing, path)
					continue
				}
				a.runs = append(a.runs, run)
			}
			// A successful default CI run cannot substitute for a missing workflow.
			a.runs = append(a.runs, runFixture(99, 1, 10, 11, 20, head))
			result := auditWorkflowsTest(t, workflows, f, g, a)
			if len(missing) == 0 {
				if len(result.Violations) != 0 {
					t.Fatal(result.Violations)
				}
			} else {
				want := "CI has no successful queue-ref attempt for workflows " + strings.Join(missing, ", ") + " on " + head + " completed before LandingStarted (the conservative pre-push deadline)"
				if len(result.Violations) != 1 || result.Violations[0].Check != "ci_success" || result.Violations[0].Message != want {
					t.Fatalf("violations = %+v, want %q", result.Violations, want)
				}
			}
			if slices.Contains(a.attemptCalls, [2]int64{99, 1}) {
				t.Fatal("expanded non-configured workflow")
			}
		})
	}
}

func auditWorkflowsTest(t *testing.T, workflows ciWorkflows, f *fixture, g *gitFixture, a *fakeActions) AuditResult {
	t.Helper()
	result, err := audit(t.Context(), workflows, f.j, epoch, epoch.Add(time.Hour), gitReader{dir: g.dir}, auditSource(t, f, g, a))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMultiWorkflowAttemptCounting(t *testing.T) {
	f := newFixture(t)
	f.enqueue(5, 1, "head")
	id := f.start(10, 1, "head", queue.Merge, false)
	f.begin(30, id)
	f.land(40, id, "head")
	workflows := smartfireWorkflows()
	a := &fakeActions{historical: map[[2]int64]*github.WorkflowRun{}}
	for i, path := range workflows {
		run := runFixture(int64(i+1), 1, 10, 11, 20, "head")
		run.Path = new(path)
		if i == 3 {
			failed := *run
			failed.Conclusion = new("failure")
			a.historical[[2]int64{run.GetID(), 1}] = &failed
			run.RunAttempt = new(2)
			// Reports charge even a rerun after landing to its original batch.
			run.RunStartedAt = &github.Timestamp{Time: epoch.Add(70 * time.Second)}
			run.UpdatedAt = &github.Timestamp{Time: epoch.Add(80 * time.Second)}
		}
		a.runs = append(a.runs, run, run)
	}
	a.runs = append(a.runs, runFixture(99, 1, 10, 11, 20, "head"))
	r, err := reportJournalAt(f.j, testWindow(0, 60), epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r.Runs, err = countRuns(t.Context(), workflows, a, "main", r.Batches)
	if err != nil {
		t.Fatal(err)
	}
	finishDrain(&r, f.j)
	if r.RunAttempts != 5 || r.RunsPerMerge != 5 || !r.DrainEnd.Equal(epoch.Add(80*time.Second)) {
		t.Fatalf("attempts=%d runs/merge=%v drain=%v", r.RunAttempts, r.RunsPerMerge, r.DrainEnd)
	}
	want := [][2]int64{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {4, 2}}
	if !reflect.DeepEqual(a.attemptCalls, want) {
		t.Fatalf("expanded %v, want %v", a.attemptCalls, want)
	}
}

func TestAttemptCannotChangeWorkflow(t *testing.T) {
	workflows := smartfireWorkflows()
	listed := runFixture(1, 1, 10, 11, 20, "head")
	listed.Path = new(workflows[0])
	attempt := *listed
	attempt.Path = new(workflows[1])
	a := &fakeActions{runs: []*github.WorkflowRun{listed}, historical: map[[2]int64]*github.WorkflowRun{{1, 1}: &attempt}}
	if _, err := attempts(t.Context(), workflows, a, a.runs); err == nil || !strings.Contains(err.Error(), "inconsistent Actions metadata") {
		t.Fatalf("changed workflow accepted: %v", err)
	}
}
