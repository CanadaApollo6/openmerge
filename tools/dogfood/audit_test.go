package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/google/go-github/v93/github"
)

type gitFixture struct {
	t         *testing.T
	dir, tree string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	g := &gitFixture{t: t, dir: t.TempDir()}
	g.git("", "init", "--bare")
	g.tree = g.git("", "mktree")
	return g
}
func (g *gitFixture) git(input string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", g.dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com"}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func (g *gitFixture) commit(message string, parents ...string) string {
	g.t.Helper()
	args := []string{"commit-tree", g.tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	return g.git(message+"\n", args...)
}
func auditFixture(t *testing.T, method queue.MergeMethod, base, head, tested, landed string) *fixture {
	t.Helper()
	f := newFixture(t)
	f.s.BaseSHA = base
	f.j.Seed.BaseSHA = base
	f.enqueue(5, 1, head)
	id := f.start(10, 1, tested, method, false)
	f.begin(30, id)
	f.land(40, id, landed)
	return f
}
func auditTest(t *testing.T, f *fixture, g *gitFixture, a *fakeActions) AuditResult {
	t.Helper()
	r, err := audit(t.Context(), f.j, epoch, epoch.Add(time.Hour), gitReader{dir: g.dir}, a)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func hasViolation(r AuditResult, check string) bool {
	for _, v := range r.Violations {
		if v.Check == check {
			return true
		}
	}
	return false
}
func TestAuditChecksWithTemporaryGitRepository(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	tested := g.commit("tested", head)
	unrelated := g.commit("unrelated")
	cases := []struct {
		name                  string
		method                queue.MergeMethod
		tested, landed, check string
		ci                    bool
	}{
		{"exact tested commit", queue.Merge, tested, tested, "", true},
		{"wrong landed commit", queue.Merge, tested, head, "tested_commit", true},
		{"main rewound", queue.Merge, unrelated, unrelated, "main_forward", true},
		{"no successful CI", queue.Merge, tested, tested, "ci_success", false},
		{"verified fast-forward", queue.FastForward, tested, head, "", true},
		{"wrong marker parent", queue.FastForward, g.commit("bad marker", base), head, "tested_commit", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := auditFixture(t, tc.method, base, head, tc.tested, tc.landed)
			run := runFixture(1, 1, 10, 11, 20, tc.tested)
			if !tc.ci {
				run.Conclusion = new("failure")
			}
			r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{run}})
			if len(r.CheckedEventIDs) != 1 {
				t.Fatal(r)
			}
			if tc.check == "" {
				if len(r.Violations) != 0 {
					t.Fatal(r.Violations)
				}
			} else if !hasViolation(r, tc.check) {
				t.Fatalf("missing %s: %+v", tc.check, r.Violations)
			}
		})
	}
}
func TestAuditRejectsChangedMarkerTree(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	blob := g.git("different\n", "hash-object", "-w", "--stdin")
	g.tree = g.git("100644 blob "+blob+"\tfile\n", "mktree")
	tested := g.commit("marker", head)
	f := auditFixture(t, queue.FastForward, base, head, tested, head)
	r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, tested)}})
	if !hasViolation(r, "tested_commit") {
		t.Fatal(r)
	}
}
func TestAuditRequiresSuccessBeforeLandingAndReadsHistoricalAttempts(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := auditFixture(t, queue.Merge, base, head, head, head)
	late := runFixture(1, 2, 10, 70, 80, head)
	early := runFixture(1, 1, 10, 11, 20, head)
	a := &fakeActions{runs: []*github.WorkflowRun{late}, historical: map[[2]int64]*github.WorkflowRun{{1, 1}: early, {1, 2}: late}}
	if r := auditTest(t, f, g, a); len(r.Violations) != 0 {
		t.Fatal(r)
	}
	early.Conclusion = new("failure")
	if r := auditTest(t, f, g, a); !hasViolation(r, "ci_success") {
		t.Fatal("post-landing success accepted")
	}
}
func TestAuditChecksPreviousQueueHeadAcrossExternalBaseMove(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	one := g.commit("one", base)
	other := g.commit("other")
	two := g.commit("two", other)
	f := auditFixture(t, queue.Merge, base, one, one, one)
	f.emit(50, queue.BaseMoved{SHA: other})
	f.enqueue(60, 2, two)
	id := f.start(70, 2, two, queue.Merge, false)
	f.begin(80, id)
	f.land(90, id, two)
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, one), runFixture(2, 1, 70, 71, 75, two)}}
	r, err := audit(t.Context(), f.j, epoch.Add(55*time.Second), epoch.Add(time.Hour), gitReader{dir: g.dir}, a)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(r, "main_forward") {
		t.Fatal("external rewind hid non-descendant queue heads")
	}
}
func TestAuditDoesNotTrustRejectedJournalLandings(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := newFixture(t)
	f.j.Seed.BaseSHA = base
	f.s.BaseSHA = base
	f.emit(10, queue.Landed{BatchID: "unknown", NewBaseSHA: head, NewFence: "fence"})
	r := auditTest(t, f, g, &fakeActions{})
	if !hasViolation(r, "journal") || !hasViolation(r, "tested_commit") || !hasViolation(r, "ci_success") {
		t.Fatal(r)
	}
}
func TestAuditSinceAndCoverage(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := auditFixture(t, queue.Merge, base, head, head, head)
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, head)}}
	audited := auditTest(t, f, g, a)
	r := mustReport(t, f, testWindow(0, 60))
	if err := attachAudit(&r, f.j, audited); err != nil {
		t.Fatal(err)
	}
	missing := audited
	missing.CheckedEventIDs = nil
	if err := attachAudit(&r, f.j, missing); err == nil {
		t.Fatal("incomplete audit accepted")
	}
	wrong := audited
	wrong.Queue.Installation = "another"
	if err := attachAudit(&r, f.j, wrong); err == nil {
		t.Fatal("wrong queue audit accepted")
	}
	after, err := audit(t.Context(), f.j, epoch.Add(41*time.Second), epoch.Add(time.Hour), gitReader{dir: g.dir}, a)
	if err != nil || len(after.CheckedEventIDs) != 0 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	a.err = fmt.Errorf("API unavailable")
	if _, err := audit(t.Context(), f.j, epoch, epoch.Add(time.Hour), gitReader{dir: g.dir}, a); err == nil {
		t.Fatal("API error accepted")
	}
}
func TestAuditMissingObjectsFailClosed(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	missing := strings.Repeat("a", 40)
	f := auditFixture(t, queue.Merge, base, head, missing, missing)
	r := auditTest(t, f, g, &fakeActions{})
	if !hasViolation(r, "tested_commit") || !hasViolation(r, "main_forward") {
		t.Fatal(r)
	}
}
func TestAuditCoverageIncludesOutOfCohortLandings(t *testing.T) {
	f := newFixture(t)
	f.enqueue(-10, 1, "h")
	id := f.start(10, 1, "tested", queue.Merge, false)
	f.begin(20, id)
	landed := f.land(30, id, "")
	r := mustReport(t, f, testWindow(0, 60))
	if r.Landed != 0 {
		t.Fatal("unexpected cohort")
	}
	a := AuditResult{Version: 1, Queue: f.j.Key, Since: epoch, Until: epoch.Add(time.Hour)}
	if err := attachAudit(&r, f.j, a); err == nil {
		t.Fatal("uncounted landing escaped independent audit coverage")
	}
	a.CheckedEventIDs = []int64{landed.ID}
	a.Violations = []AuditViolation{{EventID: landed.ID, At: landed.RecordedAt, Check: "tested_commit", Message: "bad"}}
	if err := attachAudit(&r, f.j, a); err != nil || len(r.Audit.Violations) != 1 {
		t.Fatal("violation lost", err)
	}
}

func TestAuditOwnPushObservationBeforeLegacySettlement(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := newFixture(t)
	f.s.BaseSHA = base
	f.j.Seed.BaseSHA = base
	f.enqueue(5, 1, head)
	id := f.start(10, 1, head, queue.Merge, false)
	f.begin(30, id)
	f.emit(35, queue.BaseMoved{SHA: head})
	f.land(40, id, head)
	r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, head)}})
	if len(r.Violations) != 0 {
		t.Fatal(r)
	}
}
