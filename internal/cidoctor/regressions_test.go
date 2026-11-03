package cidoctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestLongPoleAttributesCriticalPathBottleneck(t *testing.T) {
	for _, graph := range []string{"complete", "multi-level", "missing", "wrong-sha", "missing-job", "cycle", "inconsistent-timing"} {
		t.Run(graph, func(t *testing.T) {
			j, a := fixture(t, 10, "long-pole")
			for i := range a.Runs {
				r := &a.Runs[i]
				if !strings.HasPrefix(r.HeadBranch, "openmerge/") {
					continue
				}
				end := r.Jobs[0].CompletedAt
				gate := Job{ID: r.ID*10 + 2, Name: "aggregate", Attempt: 1, CreatedAt: end, StartedAt: end, CompletedAt: end.Add(3 * time.Second), Conclusion: "success", Labels: []string{"ubuntu-latest"}, Steps: []Step{{Name: "Check results", Number: 1, StartedAt: end, CompletedAt: end.Add(3 * time.Second), Conclusion: "success"}}}
				r.Jobs = append(r.Jobs, gate)
				r.Graph.Needs = map[string][]string{"check": {}, "fast": {}, "aggregate": {"check", "fast"}}
				r.Graph.Required = []string{"aggregate"}
				switch graph {
				case "multi-level":
					prefix := r.Jobs[0]
					prefix.ID, prefix.Name = r.ID*10+3, "prefix"
					prefix.CompletedAt = prefix.StartedAt.Add(400 * time.Second)
					prefix.Steps = []Step{{Name: "Prepare", Number: 1, StartedAt: prefix.StartedAt, CompletedAt: prefix.CompletedAt, Conclusion: "success"}}
					independent := prefix
					independent.ID, independent.Name = r.ID*10+4, "independent"
					independent.CompletedAt = independent.StartedAt.Add(1200 * time.Second)
					independent.Steps = []Step{{Name: "Other tests", Number: 1, StartedAt: independent.StartedAt, CompletedAt: independent.CompletedAt, Conclusion: "success"}}
					for k := range r.Jobs {
						job := &r.Jobs[k]
						job.CreatedAt = job.CreatedAt.Add(400 * time.Second)
						job.StartedAt = job.StartedAt.Add(400 * time.Second)
						job.CompletedAt = job.CompletedAt.Add(400 * time.Second)
						job.Steps = slices.Clone(job.Steps)
						for l := range job.Steps {
							job.Steps[l].StartedAt = job.Steps[l].StartedAt.Add(400 * time.Second)
							job.Steps[l].CompletedAt = job.Steps[l].CompletedAt.Add(400 * time.Second)
						}
					}
					r.Jobs = append(r.Jobs, prefix, independent)
					r.Graph.Needs["prefix"], r.Graph.Needs["independent"] = []string{}, []string{}
					r.Graph.Needs["check"], r.Graph.Needs["fast"] = []string{"prefix"}, []string{"prefix"}
				case "inconsistent-timing":
					r.Graph.Needs["check"] = []string{"fast"}
				case "missing":
					r.Graph = nil
				case "wrong-sha":
					r.Graph.HeadSHA = "other"
				case "missing-job":
					delete(r.Graph.Needs, "fast")
				case "cycle":
					r.Graph.Needs["check"] = []string{"aggregate"}
				}
			}
			r := compute(t, j, a)
			var poles []Finding
			for _, f := range r.Findings {
				if f.Code == "long-pole" {
					poles = append(poles, f)
				}
			}
			if graph != "complete" && graph != "multi-level" {
				if len(poles) != 0 || !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.Code == "dependency-graph" }) {
					t.Fatalf("unestablished critical path produced advice or no gap: %+v", poles)
				}
				return
			}
			if len(poles) != 1 || !strings.Contains(poles[0].Subject, " :: check [") {
				t.Fatalf("want test bottleneck, not its 3s aggregate: %+v", poles)
			}
			if poles[0].Evidence.Metrics["median_sibling_idle_seconds"] != 810 || poles[0].MinutesAtStake != 135 {
				t.Fatalf("idle must compare parallel siblings only: %+v", poles[0])
			}
		})
	}
}

func TestMaskedSkipConclusionsRespectClosedWindow(t *testing.T) {
	for _, mode := range []string{"inside", "at-until", "after-until", "missing-success-completion", "before-since", "cancelled-after-until"} {
		t.Run(mode, func(t *testing.T) {
			j, a := fixture(t, 10, "masked-skip")
			for i := range a.Runs {
				r := &a.Runs[i]
				if !strings.HasPrefix(r.HeadBranch, "openmerge/") {
					continue
				}
				switch mode {
				case "at-until":
					r.Jobs[0].CompletedAt = window.Until
				case "after-until":
					r.Jobs[0].CompletedAt = window.Until.Add(time.Second)
				case "missing-success-completion":
					r.Jobs[0].CompletedAt = time.Time{}
				case "before-since":
					r.Jobs[0].CompletedAt = window.Since.Add(-time.Second)
				case "cancelled-after-until":
					r.Jobs[1].Conclusion = "cancelled"
					r.Jobs[1].CompletedAt = window.Until.Add(time.Second)
				}
			}
			r := compute(t, j, a)
			got := slices.Contains(codes(r), "masked-skip")
			if got != (mode == "inside") {
				t.Fatalf("masked-skip outside closed window: mode %s, finding %t", mode, got)
			}
			if mode != "inside" && !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.Code == "masked-skip-data" }) {
				t.Fatal("missing conclusion window gap")
			}
		})
	}
}

func TestMissingJournalWorkflowAndRetryEvidence(t *testing.T) {
	t.Run("another-workflow-on-the-same-push", func(t *testing.T) {
		j, a := fixture(t, 1, "time-split")
		j.Events = append(j.Events, store.JournalEvent{RecordedAt: at(8000), Event: queue.ChecksObserved{BatchID: "b1", TestedSHA: "sha-1", Runs: []queue.CheckRun{{WorkflowRunID: 1}, {WorkflowRunID: 999}}}})
		r := compute(t, j, a)
		if !slices.ContainsFunc(r.Gaps, func(g Gap) bool { return g.RunID == 999 && g.Code == "missing-actions" }) {
			t.Fatal("recording one workflow hid another missing workflow on the same push")
		}
	})
	for _, mode := range []string{"absent", "missing-job", "invalid-timing"} {
		t.Run("accepted-retries-"+mode, func(t *testing.T) {
			j, a := fixture(t, 10, "retry-habit")
			if mode == "absent" {
				a.Runs = slices.DeleteFunc(a.Runs, func(r Run) bool { return r.Attempt == 2 })
			} else {
				for i := range a.Runs {
					if a.Runs[i].Attempt == 2 {
						if mode == "missing-job" {
							a.Runs[i].Jobs[0].Name = "other"
						} else {
							a.Runs[i].Jobs[0].Steps = nil
						}
					}
				}
			}
			r := compute(t, j, a)
			if mode != "invalid-timing" {
				missing := 0
				for _, g := range r.Gaps {
					if g.Code == "missing-actions" && strings.Contains(g.Detail, "attempt 2") {
						missing++
					}
				}
				if missing != 10 {
					t.Fatalf("want one gap per missing accepted retry, got %d", missing)
				}
			}
			for _, s := range r.Assessments {
				if s.Code == "retry-habit" && strings.Contains(s.Subject, " :: check [") {
					t.Fatalf("incomplete retry samples entered the rate: %+v", s)
				}
			}
		})
	}
}

func TestTextCompactsNonTriggeringObservations(t *testing.T) {
	j, a := fixture(t, 10, "serial-step")
	r := compute(t, j, a)
	for i := range 40 {
		r.Assessments = append(r.Assessments, Assessment{Code: "serial-step", Subject: fmt.Sprintf("job :: cleanup %02d", i), QualifyingRuns: 10, Metrics: map[string]float64{"median_job_seconds": 930, "median_step_fraction": 0}})
	}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteText(&b, r); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(r)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("text formatting changed the full JSON evidence")
	}
	text := b.String()
	start, end := strings.Index(text, "\nObservations"), strings.Index(text, "\nJob time split")
	if start < 0 || end < start {
		t.Fatal("missing observations or classifications")
	}
	observations := text[start:end]
	if strings.Count(observations, "cleanup ") > 3 || strings.Count(observations, "\n") > 65 {
		t.Fatal("non-triggering step rows bury the findings")
	}
	if !strings.Contains(observations, "non-triggering") || !strings.Contains(text, "qualifying 10; triggered 10") || !strings.Contains(text, "unknown/run | 900.0") {
		t.Fatal("summary lost sample counts or triggered step classifications")
	}
	if strings.Index(text, "\nFindings:") > strings.Index(text, "\nThresholds:") {
		t.Fatal("findings should come before thresholds and observations")
	}
}
