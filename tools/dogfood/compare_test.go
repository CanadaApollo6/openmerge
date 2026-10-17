package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

func comparableReports() (Report, Report) {
	key := store.QueueKey{Installation: "1", Repository: "CanadaApollo6/openmerge", BaseBranch: "main"}
	middle := Boundary{Time: epoch.Add(14 * 24 * time.Hour), EventID: 100, Commit: "policy-b"}
	a := Report{Version: 2, Queue: key, Window: Window{From: Boundary{Time: epoch}, To: middle}, Landed: 50, LandedAtClose: 50, Engaged: 10, DeepEngaged: 3, CIConfig: "ci-unchanged", LogReviewed: true, ArrivalsPerDay: 4, RunsPerMerge: 1.5, TimeToMerge: Percentiles{Median: 30, P95: 90}}
	b := a
	b.Window = Window{From: middle, To: Boundary{Time: epoch.Add(28 * 24 * time.Hour)}}
	b.Engaged = 0
	a.NextConfig = &middle
	for i, r := range []*Report{&a, &b} {
		r.Changes = make([]Change, 50)
		for n := range r.Changes {
			r.Changes[n] = Change{PR: n + 1, AdmissionID: uint64(n + 1), LandedEventID: int64(n + 1), LandedAt: r.Window.From.Time.Add(time.Hour), Tested: true, Position: 1}
			if i == 0 && n < 10 {
				r.Changes[n].Position = 2
			}
			if i == 0 && n < 3 {
				r.Changes[n].Position = 3
			}
		}
		r.DrainComplete = true
		r.DrainEnd = r.Window.To.Time
		r.ReportAt = r.DrainEnd.Add(time.Hour)
		r.Audit = &AuditResult{Version: 1, Queue: key, Since: r.Window.From.Time, Until: r.Window.To.Time.Add(time.Second)}
		r.WindowHistory = []WindowPoint{{At: r.Window.From.Time, MaxDepth: []int{4, 1}[i], W: 1}}
	}
	return a, b
}
func TestDecisionRule(t *testing.T) {
	cases := []struct {
		name, want, reason string
		change             func(*Report, *Report)
	}{
		{"pass", "pass", "all pre-registered", func(*Report, *Report) {}},
		{"better on all metrics", "pass", "", func(a, _ *Report) { a.TimeToMerge = Percentiles{Median: 20, P95: 60}; a.RunsPerMerge = 1 }},
		{"median worse", "fail", "median", func(a, _ *Report) { a.TimeToMerge.Median++ }},
		{"p95 worse", "fail", "p95", func(a, _ *Report) { a.TimeToMerge.P95++ }},
		{"runs worse", "fail", "runs per", func(a, _ *Report) { a.RunsPerMerge++ }},
		{"incorrect landing aborts even small cohort", "aborted", "abort", func(a, _ *Report) { a.Landed = 1; a.Audit.Violations = []AuditViolation{{Check: "tested_commit"}} }},
		{"B incorrect landing", "aborted", "B has 1 audit", func(_, b *Report) { b.Audit.Violations = []AuditViolation{{Check: "ci_success"}} }},
		{"insufficient changes", "inconclusive", "needs 50", func(a, _ *Report) { a.Landed = 49; a.LandedAtClose = 49; a.Changes = a.Changes[:49] }},
		{"B fewer than 50 landed", "inconclusive", "B has 49", func(_, b *Report) { b.LandedAtClose = 49; b.Landed = 49; b.Changes = b.Changes[:49] }},
		{"B insufficient at close despite drain landings", "inconclusive", "B has 49", func(_, b *Report) { b.LandedAtClose = 49 }},
		{"position three floor", "inconclusive", "position >=3", func(a, _ *Report) {
			for i := range a.Changes {
				if a.Changes[i].Position == 3 {
					a.Changes[i].Position = 2
				}
			}
		}},
		{"untested landing cannot engage", "inconclusive", "position >=2", func(a, _ *Report) { a.Changes[9].Tested = false }},
		{"incomplete drain", "inconclusive", "drain incomplete", func(_, b *Report) { b.DrainComplete = false }},
		{"operator abort", "aborted", "wedge", func(a, _ *Report) { a.AbortedReason = "hour-long wedge" }},
		{"short A", "inconclusive", "14 days", func(a, _ *Report) { a.Window.From.Time = a.Window.From.Time.Add(time.Hour) }},
		{"insufficient engagement", "inconclusive", "position >=2", func(a, _ *Report) { a.Engaged = 9; a.Changes[9].Position = 1 }},
		{"six weeks without engagement", "inconclusive", "six weeks", func(a, b *Report) {
			a.Engaged = 9
			a.Changes[9].Position = 1
			a.Window.To.Time = epoch.Add(42 * 24 * time.Hour)
			b.Window.From = a.Window.To
			a.NextConfig = &b.Window.From
			b.Window.To.Time = epoch.Add(84 * 24 * time.Hour)
		}},
		{"six weeks with position two floor but not position three", "inconclusive", "six weeks", func(a, b *Report) {
			a.Window.To.Time = epoch.Add(42 * 24 * time.Hour)
			a.DrainEnd = a.Window.To.Time
			a.ReportAt = a.DrainEnd.Add(time.Hour)
			a.Audit.Until = a.ReportAt
			b.Window.From = a.Window.To
			a.NextConfig = &b.Window.From
			b.Window.To.Time = epoch.Add(84 * 24 * time.Hour)
			b.DrainEnd = b.Window.To.Time
			b.ReportAt = b.DrainEnd.Add(time.Hour)
			b.Audit.Since = b.Window.From.Time
			b.Audit.Until = b.ReportAt
			for i := range a.Changes {
				if a.Changes[i].Position == 3 {
					a.Changes[i].Position = 2
				}
			}
		}},
		{"arrivals exactly factor two", "pass", "", func(a, _ *Report) { a.ArrivalsPerDay = 8 }},
		{"A arrivals exceed factor two", "inconclusive", "not comparable", func(a, _ *Report) { a.ArrivalsPerDay = 8.01 }},
		{"B arrivals exceed factor two", "inconclusive", "not comparable", func(_, b *Report) { b.ArrivalsPerDay = 8.01 }},
		{"no arrivals", "inconclusive", "not comparable", func(_, b *Report) { b.ArrivalsPerDay = 0 }},
		{"missing audit", "inconclusive", "audit is missing", func(a, _ *Report) { a.Audit = nil }},
		{"CI differs", "inconclusive", "CI configuration", func(_, b *Report) { b.CIConfig = "changed" }},
		{"CI unknown", "inconclusive", "CI configuration", func(a, b *Report) { a.CIConfig = ""; b.CIConfig = "" }},
		{"unreviewed operator log", "inconclusive", "material", func(_, b *Report) { b.LogReviewed = false }},
		{"different queues", "inconclusive", "different queues", func(_, b *Report) { b.Queue.Installation = "other" }},
		{"nonadjacent windows", "inconclusive", "not adjacent", func(_, b *Report) { b.Window.From.Time = b.Window.From.Time.Add(time.Hour) }},
		{"unequal length", "inconclusive", "equal duration", func(_, b *Report) { b.Window.To.Time = b.Window.To.Time.Add(time.Hour) }},
		{"wrong depth", "inconclusive", "max_depth", func(a, _ *Report) { a.WindowHistory[0].MaxDepth = 1 }},
		{"missing replay", "inconclusive", "max_depth", func(a, _ *Report) { a.WindowHistory = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := comparableReports()
			tc.change(&a, &b)
			c := compare(a, b)
			if c.Status != tc.want || !strings.Contains(strings.Join(c.Reasons, " "), tc.reason) {
				t.Fatalf("got %+v want %s / %s", c, tc.want, tc.reason)
			}
		})
	}
}
func TestDepthAtBoundaryIgnoresZeroDurationOldPolicy(t *testing.T) {
	a, b := comparableReports()
	a.WindowHistory = append([]WindowPoint{{At: a.Window.From.Time, MaxDepth: 1}}, a.WindowHistory...)
	if c := compare(a, b); c.Status != "pass" {
		t.Fatal(c)
	}
}
func TestCompareCLIExitStatusesAndJSON(t *testing.T) {
	for _, status := range []struct {
		name string
		code int
	}{{"pass", 0}, {"fail", 1}, {"inconclusive", 2}, {"aborted", 3}} {
		t.Run(status.name, func(t *testing.T) {
			a, b := comparableReports()
			switch status.name {
			case "fail":
				a.RunsPerMerge++
			case "aborted":
				a.AbortedReason = "hour-long wedge"
			case "inconclusive":
				a.Engaged = 0
				for i := range a.Changes {
					a.Changes[i].Position = 1
				}
			}
			dir := t.TempDir()
			for name, r := range map[string]Report{"a.json": a, "b.json": b} {
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errs bytes.Buffer
			code := run(t.Context(), []string{"compare", "--json", filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")}, &out, &errs)
			if code != status.code || errs.Len() != 0 {
				t.Fatalf("code=%d stderr=%s", code, &errs)
			}
			var c Comparison
			if err := json.Unmarshal(out.Bytes(), &c); err != nil || c.Status != status.name {
				t.Fatalf("output=%s err=%v", &out, err)
			}
		})
	}
}
func TestHumanReportIncludesPerChangeAndReplayEvidence(t *testing.T) {
	f := newFixture(t)
	f.enqueue(5, 1, "head")
	id := f.start(10, 1, "tested", "merge", false)
	f.begin(20, id)
	f.land(30, id, "")
	r := mustReport(t, f, testWindow(0, 60))
	var out bytes.Buffer
	if err := printReport(&out, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nearest rank", "median 25.0s", "position 1", "queue length 1", "W=1", "UNKNOWN"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
}

func TestLateEngagementCannotRescueSixWeekDeadline(t *testing.T) {
	a, b := comparableReports()
	a.Window.To.Time = epoch.Add(43 * 24 * time.Hour)
	b.Window.From = a.Window.To
	a.NextConfig = &b.Window.From
	b.Window.To.Time = epoch.Add(86 * 24 * time.Hour)
	a.DrainEnd = a.Window.To.Time
	a.ReportAt = a.DrainEnd.Add(time.Hour)
	a.Audit.Until = a.DrainEnd.Add(time.Second)
	b.DrainEnd = b.Window.To.Time
	b.ReportAt = b.DrainEnd.Add(time.Hour)
	b.Audit.Since, b.Audit.Until = b.Window.From.Time, b.DrainEnd.Add(time.Second)
	for i := range a.Changes {
		a.Changes[i].LandedAt = epoch.Add(42*24*time.Hour + time.Second)
		a.Changes[i].Position = 3
	}
	if c := compare(a, b); c.Status != "inconclusive" || !strings.Contains(strings.Join(c.Reasons, " "), "later landings") {
		t.Fatal(c)
	}
	for i := range a.Changes {
		a.Changes[i].LandedAt = epoch.Add(10 * 24 * time.Hour)
	}
	if c := compare(a, b); c.Status != "pass" {
		t.Fatal(c)
	}
}

func TestBExtensionNeverRescuesTraffic(t *testing.T) {
	a, b := comparableReports()
	b.Window.To.Time = b.Window.To.Time.Add(14 * 24 * time.Hour)
	if c := compare(a, b); c.Status != "inconclusive" || !strings.Contains(strings.Join(c.Reasons, " "), "equal duration") {
		t.Fatal(c)
	}
}
