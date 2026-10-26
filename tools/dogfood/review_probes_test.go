package main

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/google/go-github/v93/github"
)

func TestReviewAuditRequiresActualMainMembership(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("tested but never landed", base)
	g.git("", "update-ref", "refs/heads/main", base)
	f := auditFixture(t, queue.Squash, base, tested, tested, tested)
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, tested)}}
	r := auditTest(t, f, g, a)
	if len(r.Violations) == 0 {
		t.Fatalf("FALSE PASS: main=%s, journal landing=%s, violations=%v", base, tested, r.Violations)
	}
}

func TestReviewAuditAcceptsForwardAdminPushBeforeSettlement(t *testing.T) {
	for _, chained := range []bool{false, true} {
		name := "legacy"
		if chained {
			name = "chained"
		}
		t.Run(name, func(t *testing.T) {
			g := newGitFixture(t)
			base := g.commit("base")
			head := g.commit("PR head", base)
			tested := g.commit("squashed queue landing", base)
			admin := g.commit("admin forward push after queue push", tested)
			g.git("", "update-ref", "refs/heads/main", admin)
			f := newFixture(t)
			f.s.BaseSHA, f.j.Seed.BaseSHA = base, base
			if chained {
				f.config(0, 4, "a")
			}
			f.enqueue(5, 1, head)
			id := f.start(10, 1, tested, queue.Squash, chained)
			f.begin(30, id)
			f.emit(35, queue.BaseMoved{SHA: admin})
			f.land(40, id, tested)
			r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, tested)}})
			if len(r.Violations) > 0 {
				t.Fatalf("FALSE ABORT: main advanced base -> tested -> admin; valid settlement emitted tested: %+v", r.Violations)
			}
		})
	}
}

func TestReviewAuditCIMustPrecedeObservedPush(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("queue commit", base)
	g.git("", "update-ref", "refs/heads/main", tested)
	f := newFixture(t)
	f.s.BaseSHA, f.j.Seed.BaseSHA = base, base
	f.enqueue(5, 1, tested)
	id := f.start(10, 1, tested, queue.Merge, false)
	f.begin(30, id)
	f.emit(35, queue.BaseMoved{SHA: tested})
	f.land(80, id, tested)
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 60, tested)}}
	r := auditTest(t, f, g, a)
	if !hasViolation(r, "ci_success") {
		t.Fatalf("FALSE PASS: push was observed at t=35, CI first succeeded at t=60, settlement t=80: %+v", r)
	}
}

func TestReviewAuditRejectsDifferentWorkflowWithSameName(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("queue commit", base)
	f := auditFixture(t, queue.Merge, base, tested, tested, tested)
	run := runFixture(1, 1, 10, 11, 20, tested)
	run.WorkflowID = new(int64(999))
	run.Path = new(".github/workflows/unrelated.yml")
	r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{run}})
	if !hasViolation(r, "ci_success") {
		t.Fatalf("FALSE PASS: only unrelated.yml (display name CI) passed: %+v", r)
	}
}

func TestReviewAuditCIRejectionControls(t *testing.T) {
	for _, which := range []string{"wrong path", "main branch", "other sha"} {
		t.Run(which, func(t *testing.T) {
			g := newGitFixture(t)
			base := g.commit("base")
			tested := g.commit("queue commit", base)
			f := auditFixture(t, queue.Merge, base, tested, tested, tested)
			run := runFixture(1, 1, 10, 11, 20, tested)
			switch which {
			case "wrong path":
				run.Path = new(".github/workflows/other.yml")
			case "main branch":
				run.HeadBranch = new("main")
			case "other sha":
				run.HeadSHA = new(strings.Repeat("0", 40))
			}
			r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{run}})
			if !hasViolation(r, "ci_success") {
				t.Fatal("wrong CI evidence accepted", r)
			}
		})
	}
}

func TestReviewSquashAndAdminMoveBeforeBatchAreValid(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	admin := g.commit("admin push before batch", base)
	head := g.commit("original PR head", base)
	tested := g.commit("squashed on admin base", admin)
	f := newFixture(t)
	f.s.BaseSHA, f.j.Seed.BaseSHA = base, base
	f.emit(1, queue.BaseMoved{SHA: admin})
	f.enqueue(5, 1, head)
	id := f.start(10, 1, tested, queue.Squash, false)
	f.begin(30, id)
	f.land(40, id, tested)
	r, err := audit(t.Context(), nil, f.j, epoch.Add(20*time.Second), epoch.Add(time.Hour), gitReader{dir: g.dir}, auditSource(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, tested)}}))
	if err != nil || len(r.Violations) > 0 {
		t.Fatalf("valid squash rejected: %+v %v", r, err)
	}
}
