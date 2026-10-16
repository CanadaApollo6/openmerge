package executor_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

var (
	repo   = forge.RepoRef{Owner: "owner", Name: "repo"}
	epoch  = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	errIO  = errors.New("infrastructure unavailable")
	errLog = errors.New("event log unavailable")
)

const fenceRef = "openmerge/fence/main"

type harness struct {
	f           *fake.Forge
	x           *executor.Executor
	base, fence string
	prs         []forge.BuildPR
}

func setup(t *testing.T) *harness {
	t.Helper()
	f := fake.New()
	base, fence := f.Init(repo, "main", fenceRef)
	h := &harness{f: f, base: base, fence: fence}
	for _, n := range []int{1, 2, 3} {
		pr, err := f.OpenPR(repo, n, base, true)
		must(t, err)
		h.prs = append(h.prs, forge.BuildPR{Number: n, HeadSHA: pr.HeadSHA})
	}
	h.x = &executor.Executor{
		Driver: f, Git: f, Repo: repo, Base: "main", FenceRef: fenceRef,
		RequiredChecks: []string{"ci", "lint"}, Now: func() time.Time { return epoch },
	}
	return h
}

func (h *harness) buildAction() queue.BuildTestRef {
	return queue.BuildTestRef{BatchID: "batch", BaseSHA: h.base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}, Ref: "openmerge/queue/main/batch", Method: queue.Merge}
}

func (h *harness) tested(t *testing.T) string {
	t.Helper()
	var sha string
	must(t, h.x.Execute(context.Background(), h.buildAction(), func(e queue.Event) error { sha = e.(queue.TestRefBuilt).TestedSHA; return nil }))
	return sha
}

func (h *harness) read(t *testing.T, ref string) string {
	t.Helper()
	sha, err := h.f.ReadRef(context.Background(), repo, ref)
	must(t, err)
	return sha
}

func record(events *[]queue.Event) func(queue.Event) error {
	return func(e queue.Event) error { *events = append(*events, e); return nil }
}

func TestBuildTestRef(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   queue.MergeMethod
		buildErr error
		conflict bool
	}{
		{name: "merge", method: queue.Merge},
		{name: "squash", method: queue.Squash},
		{name: "rebase", method: queue.Rebase},
		{name: "wrapped conflict", method: queue.Merge, buildErr: fmt.Errorf("cannot merge: %w", forge.ErrConflict), conflict: true},
		{name: "infrastructure", method: queue.Merge, buildErr: errIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			a := h.buildAction()
			a.Method = tc.method
			if tc.buildErr != nil {
				h.f.FailNext("BuildTestRef", tc.buildErr)
			}
			var events []queue.Event
			err := h.x.Execute(context.Background(), a, record(&events))
			if tc.buildErr == nil {
				must(t, err)
				sha := h.read(t, a.Ref)
				want := []queue.Event{queue.TestRefBuilt{BatchID: a.BatchID, TestedSHA: sha, Method: tc.method, At: epoch}}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("events = %#v, want %#v", events, want)
				}
				return
			}
			if tc.conflict {
				must(t, err)
			} else if !errors.Is(err, tc.buildErr) {
				t.Fatalf("error = %v, want infrastructure error", err)
			}
			want := []queue.Event{queue.TestRefBuildFailed{BatchID: a.BatchID, Conflict: tc.conflict, At: epoch}}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
			if _, err := h.f.ReadRef(context.Background(), repo, a.Ref); err == nil {
				t.Fatal("failed build pushed a ref")
			}
		})
	}
}

func TestBuildResultEmitFailure(t *testing.T) {
	for _, buildErr := range []error{nil, errIO, forge.ErrConflict} {
		t.Run(fmt.Sprint(buildErr), func(t *testing.T) {
			h := setup(t)
			if buildErr != nil {
				h.f.FailNext("BuildTestRef", buildErr)
			}
			err := h.x.Execute(context.Background(), h.buildAction(), func(queue.Event) error { return errLog })
			if !errors.Is(err, errLog) {
				t.Fatalf("error = %v, want emit error", err)
			}
		})
	}
}

func TestRefreshChecks(t *testing.T) {
	ci := func(state forge.CheckState) forge.CheckRun { return forge.CheckRun{Name: "ci", State: state} }
	lint := func(state forge.CheckState) forge.CheckRun { return forge.CheckRun{Name: "lint", State: state} }
	for _, tc := range []struct {
		name                             string
		checks                           []forge.CheckRun
		emptyRequired, terminal, success bool
	}{
		{name: "all success", checks: []forge.CheckRun{ci(forge.Success), lint(forge.Success)}, terminal: true, success: true},
		{name: "one failure", checks: []forge.CheckRun{ci(forge.Failure), lint(forge.Success)}, terminal: true},
		{name: "all failed", checks: []forge.CheckRun{ci(forge.Failure), lint(forge.Failure)}, terminal: true},
		{name: "pending", checks: []forge.CheckRun{ci(forge.Pending), lint(forge.Success)}},
		{name: "missing", checks: []forge.CheckRun{ci(forge.Success)}},
		{name: "no checks"},
		{name: "failure waits for other required checks", checks: []forge.CheckRun{ci(forge.Failure), lint(forge.Pending)}},
		{name: "unrelated failure ignored", checks: []forge.CheckRun{ci(forge.Success), lint(forge.Success), {Name: "other", State: forge.Failure}}, terminal: true, success: true},
		{name: "duplicate failure wins", checks: []forge.CheckRun{ci(forge.Success), ci(forge.Failure), lint(forge.Success)}, terminal: true},
		{name: "duplicate pending waits", checks: []forge.CheckRun{ci(forge.Success), ci(forge.Pending), lint(forge.Success)}},
		{name: "unknown state waits", checks: []forge.CheckRun{ci("unknown"), lint(forge.Success)}},
		{name: "no required checks", emptyRequired: true, terminal: true, success: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			sha := h.tested(t)
			if tc.emptyRequired {
				h.x.RequiredChecks = nil
			}
			h.f.SetCheckRuns(repo, sha, tc.checks)
			var events []queue.Event
			must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, record(&events)))
			var want []queue.Event
			if tc.terminal {
				want = append(want, queue.ChecksCompleted{TestedSHA: sha, Success: tc.success})
			}
			want = append(want, queue.ChecksRefreshed{BatchID: "batch", TestedSHA: sha, At: epoch})
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}

func TestRefreshErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failRead   bool
		failEmitAt int
	}{
		{name: "read failure", failRead: true},
		{name: "completed emit failure", failEmitAt: 1},
		{name: "refreshed emit failure", failEmitAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			sha := h.tested(t)
			h.f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			if tc.failRead {
				h.f.FailNext("CheckRuns", errIO)
			}
			calls := 0
			err := h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, func(queue.Event) error {
				calls++
				if calls == tc.failEmitAt {
					return errLog
				}
				return nil
			})
			want, count := errLog, tc.failEmitAt
			if tc.failRead {
				want, count = errIO, 0
			}
			if !errors.Is(err, want) || calls != count {
				t.Fatalf("error %v, emit calls %d; want %v, %d", err, calls, want, count)
			}
		})
	}
}

func TestEffectsAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		action       func(*harness) queue.Action
		want         func(queue.Action) queue.Event
		check        func(*testing.T, *harness)
	}{
		{name: "delete", method: "DeleteRef", action: func(h *harness) queue.Action { return queue.DeleteRef{Ref: h.buildAction().Ref} },
			want: func(a queue.Action) queue.Event { return queue.ActionCompleted{Key: a.Key()} },
			check: func(t *testing.T, h *harness) {
				t.Helper()
				if _, err := h.f.ReadRef(context.Background(), repo, h.buildAction().Ref); err == nil {
					t.Fatal("ref was not deleted")
				}
			}},
		{name: "report", method: "ReportStatus", action: func(*harness) queue.Action { return queue.ReportStatus{PR: 1, Status: "merged", Detail: "PR landed"} },
			want: func(a queue.Action) queue.Event { return queue.ActionCompleted{Key: a.Key()} },
			check: func(t *testing.T, h *harness) {
				t.Helper()
				want := forge.Status{Context: "openmerge", State: "merged", Detail: "PR landed"}
				if got := h.f.Statuses(repo, 1); len(got) != 1 || got["openmerge"] != want {
					t.Fatalf("statuses = %v, want %v", got, want)
				}
			}},
		{name: "dequeue", method: "ReportStatus", action: func(*harness) queue.Action {
			return queue.Dequeue{PR: 1, AdmissionID: 42, Reason: queue.ChecksTimeout, BatchID: "batch"}
		},
			want: func(queue.Action) queue.Event {
				return queue.Dequeued{PR: 1, AdmissionID: 42, Reason: queue.ChecksTimeout, BatchID: "batch"}
			},
			check: func(t *testing.T, h *harness) {
				t.Helper()
				want := forge.Status{Context: "openmerge", State: "dequeued", Detail: "checks-timeout"}
				if got := h.f.Statuses(repo, 1); len(got) != 1 || got["openmerge"] != want {
					t.Fatalf("statuses = %v, want %v", got, want)
				}
			}},
	} {
		for _, failure := range []string{"none", "forge", "emit"} {
			t.Run(tc.name+"/"+failure, func(t *testing.T) {
				h := setup(t)
				h.tested(t)
				a := tc.action(h)
				if failure == "forge" {
					h.f.FailNext(tc.method, errIO)
				}
				var events []queue.Event
				emit := record(&events)
				if failure == "emit" {
					emit = func(queue.Event) error { return errLog }
				}
				err := h.x.Execute(context.Background(), a, emit)
				switch failure {
				case "none":
					must(t, err)
				case "forge":
					if !errors.Is(err, errIO) || len(events) != 0 {
						t.Fatalf("failed effect emitted events or lost error: %v %v", err, events)
					}
					if tc.name == "delete" {
						h.read(t, h.buildAction().Ref)
					} else if len(h.f.Statuses(repo, 1)) != 0 {
						t.Fatal("failed status write changed forge")
					}
				case "emit":
					if !errors.Is(err, errLog) {
						t.Fatalf("error = %v, want emit failure", err)
					}
					tc.check(t, h)
				}
				// Retry the same keyed action, including after an unrecorded effect.
				events = nil
				must(t, h.x.Execute(context.Background(), a, record(&events)))
				if !reflect.DeepEqual(events, []queue.Event{tc.want(a)}) {
					t.Fatalf("retry events = %#v", events)
				}
				tc.check(t, h)
			})
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
