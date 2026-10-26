package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
)

func TestReviewEngagementUsesEventBoundary(t *testing.T) {
	a, _ := comparableReports()
	for i := range a.Changes {
		a.Changes[i].LandedAt = a.Window.To.Time
	}
	two, three := engagementBy(a, a.Window.To.Time.Add(time.Hour))
	if two != 10 || three != 3 {
		t.Fatalf("same-time events before close lost: %d/%d", two, three)
	}
	for i := range a.Changes {
		a.Changes[i].LandedEventID = a.Window.To.EventID
	}
	two, three = engagementBy(a, a.Window.To.Time.Add(time.Hour))
	if two != 0 || three != 0 {
		t.Fatalf("close event included: %d/%d", two, three)
	}
}
func TestReviewFinalRegenerationRequired(t *testing.T) {
	a, b := comparableReports()
	a.ReportAt = a.DrainEnd.Add(time.Hour)
	if c := compare(a, b); c.Status != "inconclusive" || !strings.Contains(strings.Join(c.Reasons, " "), "regenerate both reports") {
		t.Fatal("stale A report accepted before B drain", c)
	}
}
func TestReviewCompareRejectsInconsistentEvidence(t *testing.T) {
	for _, field := range []string{"latency", "first latency", "runs", "run count", "arrivals", "daily arrivals", "landed at close", "engagement", "deep engagement", "engagement at close", "deep at close", "audit coverage"} {
		t.Run(field, func(t *testing.T) {
			a, b := comparableReports()
			a.ReportAt, b.ReportAt = b.DrainEnd.Add(time.Hour), b.DrainEnd.Add(time.Hour)
			switch field {
			case "latency":
				a.TimeToMerge.Median--
			case "first latency":
				a.FirstEnqueue.Median--
			case "runs":
				a.RunsPerMerge--
			case "run count":
				a.RunAttempts++
			case "arrivals":
				a.ArrivalsPerDay = 5
			case "daily arrivals":
				a.ArrivalsByDay = map[string]int{"fabricated": 100}
			case "landed at close":
				a.LandedAtClose++
			case "engagement":
				a.Engaged++
			case "deep engagement":
				a.DeepEngaged++
			case "engagement at close":
				a.EngagedAtClose++
			case "deep at close":
				a.DeepEngagedAtClose++
			case "audit coverage":
				a.Audit.CheckedEventIDs = nil
			}
			reason := "summary fields"
			if field == "audit coverage" {
				reason = "audit is missing cohort landed event"
			}
			if c := compare(a, b); c.Status != "inconclusive" || !strings.Contains(strings.Join(c.Reasons, " "), reason) {
				t.Fatal("inconsistent "+field+" accepted", c)
			}
		})
	}
}
func TestReviewCountRunsSkipsUnrelatedAttemptExpansion(t *testing.T) {
	own := runFixture(1, 1, 10, 11, 20, "own")
	other := runFixture(2, 2, 10, 11, 20, "other")
	other.HeadBranch = new("openmerge/queue/main/another-period")
	a := &fakeActions{runs: []*github.WorkflowRun{own, other}}
	runs, err := countRuns(t.Context(), nil, a, "main", []string{"b1"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("unrelated inaccessible history broke count: %+v %v", runs, err)
	}
	if len(a.attemptCalls) != 1 {
		t.Fatal(a.attemptCalls)
	}
}
func TestReviewHumanReportLabels(t *testing.T) {
	a, _ := comparableReports()
	a.DrainComplete = false
	a.DrainEnd = time.Time{}
	var out bytes.Buffer
	if err := printReport(&out, a); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first-eligibility lower bound", "end: incomplete", "At-close engagement >=2: 10; >=3: 3"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
}
