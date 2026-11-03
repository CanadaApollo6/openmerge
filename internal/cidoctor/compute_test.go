package cidoctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }

var window = Window{Since: at(0), Until: at(1000000)}

// Synthetic rule fixtures exercise thresholds independently of the real
// OpenMerge recording. Journal records pass through the production codecs.
func fixture(t *testing.T, n int, rule string) (store.Journal, Actions) {
	t.Helper()
	j := store.Journal{Key: store.QueueKey{Installation: "1", Repository: "fixture/app", BaseBranch: "main"}, Seed: queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}}
	actions := Actions{Version: 1, Repository: "fixture/app", DefaultBranch: "main", Runs: []Run{}}
	state := j.Seed
	emit := func(sec int, e queue.Event) {
		t.Helper()
		_, payload, err := queue.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := queue.DecodeEvent(payload)
		if err != nil {
			t.Fatal(err)
		}
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), RecordedAt: at(sec), Event: decoded, Payload: payload})
		state = queue.Apply(state, decoded)
	}
	for i := 1; i <= n; i++ {
		sec := i * 5000
		id := fmt.Sprintf("b%d", i)
		sha := fmt.Sprintf("sha-%d", i)
		emit(sec, queue.PRObserved{PR: i, HeadSHA: sha, Eligible: true, Trusted: true})
		emit(sec, queue.EnqueueRequested{PR: i, At: at(sec)})
		emit(sec, queue.BatchStarted{BatchID: id, BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: i, HeadSHA: sha}}, At: at(sec)})
		emit(sec, queue.TestRefBuilt{BatchID: id, TestedSHA: sha, At: at(sec), Method: queue.Merge})
		mkJob := func(id uint64, name string, setup, run int) Job {
			start := sec + 5
			return Job{ID: id, Name: name, Attempt: 1, CreatedAt: at(sec + 2), StartedAt: at(start), CompletedAt: at(start + setup + run), Conclusion: "success", Labels: []string{"ubuntu-latest"}, Steps: []Step{
				{Name: "Run actions/checkout@pinned", Number: 1, StartedAt: at(start), CompletedAt: at(start + setup), Conclusion: "success"},
				{Name: "Run tests", Number: 2, StartedAt: at(start + setup), CompletedAt: at(start + setup + run), Conclusion: "success"},
			}}
		}
		r := Run{ID: uint64(i), WorkflowID: 99, Path: ".github/workflows/ci.yml", Attempt: 1, Event: "push", HeadSHA: sha, HeadBranch: "openmerge/queue/main/" + id, CreatedAt: at(sec + 1), JobsComplete: true, Jobs: []Job{mkJob(uint64(i*10), "check", 30, 90), mkJob(uint64(i*10+1), "fast", 30, 90)}}
		r.Graph = &Graph{HeadSHA: sha, Source: r.Path + "@" + sha, Needs: map[string][]string{"check": {}, "fast": {}}, Required: []string{"check"}}
		switch rule {
		case "cold-setup":
			r.Jobs[0] = mkJob(uint64(i*10), "check", 300, 90)
		case "long-pole", "serial-step":
			r.Jobs[0] = mkJob(uint64(i*10), "check", 30, 900)
		case "checkout-cost":
			r.Jobs[0] = mkJob(uint64(i*10), "check", 120, 90)
		case "masked-skip":
			r.Graph.Needs["check"] = []string{"fast"}
			r.Jobs[1].Conclusion = "skipped"
			r.Jobs[1].Steps = nil
			r.Jobs[1].StartedAt = time.Time{}
			r.Jobs[1].CompletedAt = time.Time{}
		case "retry-habit":
			failed := []queue.CheckRun{{ID: uint64(i * 10), WorkflowRunID: r.ID, Attempt: 1, Name: "check", Source: "app:1", State: "failure"}}
			deadline := at(sec).Add(time.Hour)
			emit(sec+130, queue.ChecksObserved{BatchID: id, TestedSHA: sha, Runs: failed, Terminal: true, OriginalDeadline: deadline, Timeout: time.Hour})
			emit(sec+131, queue.CheckRerunRequested{BatchID: id, TestedSHA: sha, Runs: failed, OriginalDeadline: deadline, At: at(sec + 131), Deadline: at(sec + 131).Add(time.Hour), Timeout: time.Hour})
			emit(sec+132, queue.CheckRerunDispatched{BatchID: id, TestedSHA: sha, Detail: "accepted"})
			retry := r
			retry.Attempt = 2
			retry.Jobs = slices.Clone(r.Jobs[:1])
			retry.Jobs[0].Attempt = 2
			retry.Jobs[0].CreatedAt = retry.Jobs[0].CreatedAt.Add(200 * time.Second)
			retry.Jobs[0].StartedAt = retry.Jobs[0].StartedAt.Add(200 * time.Second)
			retry.Jobs[0].CompletedAt = retry.Jobs[0].CompletedAt.Add(200 * time.Second)
			retry.Jobs[0].Steps = slices.Clone(retry.Jobs[0].Steps)
			for k := range retry.Jobs[0].Steps {
				retry.Jobs[0].Steps[k].StartedAt = retry.Jobs[0].Steps[k].StartedAt.Add(200 * time.Second)
				retry.Jobs[0].Steps[k].CompletedAt = retry.Jobs[0].Steps[k].CompletedAt.Add(200 * time.Second)
			}
			actions.Runs = append(actions.Runs, retry)
		}
		actions.Runs = append(actions.Runs, r)
		// Warm baseline has identical workflow, job names, and runner labels.
		warm := r
		warm.ID = uint64(1000 + i)
		warm.HeadBranch = "main"
		warm.Graph = nil
		warm.Jobs = []Job{mkJob(uint64(10000+i*10), "check", 30, 90), mkJob(uint64(10001+i*10), "fast", 30, 90)}
		actions.Runs = append(actions.Runs, warm)
		pr := r
		pr.ID = uint64(2000 + i)
		pr.HeadBranch = "feature"
		pr.Event = "pull_request"
		pr.Graph = nil
		pr.Jobs = []Job{mkJob(uint64(20000+i*10), "check", 30, 90)}
		if rule != "duplicate-pr-run" {
			pr.Jobs[0].CreatedAt = at(sec - 200)
			pr.Jobs[0].StartedAt = at(sec - 190)
			pr.Jobs[0].CompletedAt = at(sec - 70)
			pr.CreatedAt = at(sec - 201)
			pr.Jobs[0].Steps = []Step{{Name: "Run tests", Number: 1, StartedAt: at(sec - 190), CompletedAt: at(sec - 70), Conclusion: "success"}}
		}
		actions.Runs = append(actions.Runs, pr)
		emit(sec+2000, queue.Dequeued{PR: i, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested})
	}
	return j, actions
}

func compute(t *testing.T, j store.Journal, a Actions) Report {
	t.Helper()
	r, err := Compute(j, a, window)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func codes(r Report) []string {
	out := []string{}
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
func golden(t *testing.T, name string, data []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, data) {
		t.Fatalf("%s changed; regenerate with OPENMERGE_UPDATE_REPORTING_GOLDENS=1", p)
	}
}
func reportGoldens(t *testing.T, name string, r Report) {
	t.Helper()
	js, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, name+".json", append(js, '\n'))
	var b bytes.Buffer
	if err := WriteText(&b, r); err != nil {
		t.Fatal(err)
	}
	golden(t, name+".txt", b.Bytes())
}

func TestFindingGoldens(t *testing.T) {
	for _, rule := range []string{"time-split", "cold-setup", "long-pole", "serial-step", "checkout-cost", "masked-skip", "retry-habit", "duplicate-pr-run"} {
		t.Run(rule, func(t *testing.T) {
			j, a := fixture(t, 10, rule)
			r := compute(t, j, a)
			if !slices.Contains(codes(r), rule) {
				t.Fatalf("missing %s: %v gaps %+v", rule, codes(r), r.Gaps)
			}
			for _, f := range r.Findings {
				if len(f.Evidence.RunIDs) < MinimumRuns || f.MinutesAtStake < 0 {
					t.Fatalf("bad evidence %+v", f)
				}
			}
			reportGoldens(t, rule, r)
		})
	}
}

func TestMinimumSampleAndGapsGoldens(t *testing.T) {
	j, a := fixture(t, 9, "serial-step")
	r := compute(t, j, a)
	if len(r.Findings) != 0 || len(r.Jobs) == 0 || len(r.Assessments) == 0 {
		t.Fatal("minimum must retain data without findings")
	}
	reportGoldens(t, "minimum", r)
	j, a = fixture(t, 10, "serial-step")
	a.Runs[0].JobsComplete = false
	a.Runs[3].Jobs[0].Steps[1].StartedAt = time.Time{}
	a.Runs[6].Jobs[0].Name = "renamed"
	a.Runs[9].Graph = nil
	a.Runs[12].Jobs[0].Steps[1].Name = "custom opaque command"
	a.Runs = a.Runs[:len(a.Runs)-3]
	r = compute(t, j, a)
	reportGoldens(t, "gaps", r)
	for _, f := range r.Findings {
		if f.Code == "serial-step" {
			t.Fatal("gaps inflated the sample")
		}
	}
}

func TestBoundariesAndIsolation(t *testing.T) {
	j, a := fixture(t, 10, "serial-step")
	want := compute(t, j, a)
	// An unrelated branch cannot supply queue evidence or change results.
	other := a.Runs[0]
	other.ID = 77777
	other.HeadBranch = "other"
	other.HeadSHA = "unrelated"
	a.Runs = append(a.Runs, other)
	if got := compute(t, j, a); !reflect.DeepEqual(got, want) {
		t.Fatal("unrelated run changed report")
	}
	slices.Reverse(a.Runs)
	if got := compute(t, j, a); !reflect.DeepEqual(got, want) {
		t.Fatal("recording order changed report")
	}
	j.Events = append(j.Events, store.JournalEvent{RecordedAt: window.Until, Event: queue.TestRefBuilt{BatchID: "late", TestedSHA: "unseen", At: window.Until}})
	if got := compute(t, j, a); !reflect.DeepEqual(got, want) {
		t.Fatal("event at until changed report")
	}
	a.Repository = "other/repo"
	if _, err := Compute(j, a, window); err == nil {
		t.Fatal("repository mismatch accepted")
	}
	a.Repository = j.Key.Repository
	a.Version = 2
	if _, err := Compute(j, a, window); err == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestDuplicateAndAmbiguousAttemptsFailSilent(t *testing.T) {
	j, a := fixture(t, 10, "serial-step")
	a.Runs = append(a.Runs, a.Runs[0])
	r := compute(t, j, a)
	if slices.Contains(codes(r), "serial-step") {
		t.Fatal("duplicate inflated sample")
	}
	j, a = fixture(t, 1, "retry-habit")
	r = compute(t, j, a)
	for _, s := range r.Assessments {
		if s.QualifyingRuns > 1 {
			t.Fatal("attempts inflated sample")
		}
	}
}

func TestClassifier(t *testing.T) {
	for name, want := range map[string]string{"Run actions/checkout@sha": "checkout", "Run actions/setup-go@sha": "setup", "Run actions/cache/restore@sha": "setup", "Run actions/cache@sha": "setup", "Post Run actions/cache@sha": "cleanup", "Install dependencies": "setup", "Run npm ci": "setup", "Initialize containers": "setup", "Run make test": "unknown", "Post Run actions/setup-go@sha": "cleanup", "Complete job": "cleanup", "Restore cache": "setup"} {
		if got := classify(name); got != want {
			t.Fatalf("%s got %s want %s", name, got, want)
		}
	}
}

func TestThresholdsAndMeasuredMinutes(t *testing.T) {
	j, a := fixture(t, 10, "cold-setup")
	r := compute(t, j, a)
	for _, f := range r.Findings {
		if f.Code == "cold-setup" && f.MinutesAtStake != 45 {
			t.Fatalf("expected 10 * (300-30)/60, got %g", f.MinutesAtStake)
		}
	}
	for i := range a.Runs {
		if strings.HasPrefix(a.Runs[i].HeadBranch, "openmerge/") {
			job := &a.Runs[i].Jobs[0]
			job.Steps[0].CompletedAt = job.StartedAt.Add(60 * time.Second)
			job.Steps[1].StartedAt = job.Steps[0].CompletedAt
		}
	}
	if slices.Contains(codes(compute(t, j, a)), "cold-setup") {
		t.Fatal("exactly 2x triggered strict threshold")
	}
}

func TestRealOpenMergeRecordings(t *testing.T) {
	for _, period := range []string{"before", "after"} {
		t.Run(period, func(t *testing.T) {
			data, err := os.ReadFile("testdata/openmerge-" + period + ".actions.json")
			if err != nil {
				t.Fatal(err)
			}
			var actions Actions
			if err = json.Unmarshal(data, &actions); err != nil {
				t.Fatal(err)
			}
			// This is an identity-only journal projection, not a captured production
			// journal. Branch and tested SHA are recorded facts. Push time is unknown.
			j := store.Journal{Key: store.QueueKey{Installation: "evaluation", Repository: actions.Repository, BaseBranch: "main"}, Seed: queue.State{BaseBranch: "main"}}
			for _, r := range actions.Runs {
				if strings.HasPrefix(r.HeadBranch, "openmerge/queue/main/") {
					e := queue.TestRefBuilt{BatchID: strings.TrimPrefix(r.HeadBranch, "openmerge/queue/main/"), TestedSHA: r.HeadSHA, Method: queue.Merge}
					j.Events = append(j.Events, store.JournalEvent{RecordedAt: r.CreatedAt, Event: e})
				}
			}
			slices.SortFunc(j.Events, func(a, b store.JournalEvent) int { return a.RecordedAt.Compare(b.RecordedAt) })
			r, err := Compute(j, actions, Window{Since: at(0), Until: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)})
			if err != nil {
				t.Fatal(err)
			}
			if period == "before" {
				if r.QueueRuns != 20 || !slices.Contains(codes(r), "long-pole") || !slices.Contains(codes(r), "serial-step") {
					t.Fatalf("unexpected before report: %d %v", r.QueueRuns, codes(r))
				}
				if slices.Contains(codes(r), "cold-setup") {
					t.Fatal("actual setup medians do not support cold-setup")
				}
			} else {
				if r.QueueRuns != 3 || len(r.Findings) != 0 {
					t.Fatalf("post sample must remain below minimum: %d %v", r.QueueRuns, codes(r))
				}
			}
			reportGoldens(t, "openmerge-"+period, r)
		})
	}
}

func TestEveryFindingRequiresTenDistinctRuns(t *testing.T) {
	for _, rule := range []string{"time-split", "cold-setup", "long-pole", "serial-step", "checkout-cost", "masked-skip", "retry-habit", "duplicate-pr-run"} {
		j, a := fixture(t, 9, rule)
		if r := compute(t, j, a); len(r.Findings) != 0 {
			t.Fatalf("%s produced findings below minimum: %v", rule, codes(r))
		}
	}
}

func TestIncompleteRunCannotProduceTimingAdvice(t *testing.T) {
	j, a := fixture(t, 10, "serial-step")
	a.Runs[0].Jobs[1].Steps = nil
	r := compute(t, j, a)
	if slices.Contains(codes(r), "serial-step") || slices.Contains(codes(r), "long-pole") {
		t.Fatal("incomplete sibling left the run eligible for timing advice")
	}
	if r.GapCount == 0 {
		t.Fatal("missing timing was not counted")
	}
}

func TestRetryNeedsJournalAcceptance(t *testing.T) {
	for _, mode := range []string{"unknown", "unsupported", "wrong-sha", "stale-generation"} {
		j, a := fixture(t, 10, "retry-habit")
		for i, rec := range j.Events {
			if e, ok := rec.Event.(queue.CheckRerunDispatched); ok {
				switch mode {
				case "unknown":
					e.OutcomeUnknown = true
				case "unsupported":
					e.Unsupported = true
				case "wrong-sha":
					e.TestedSHA = "other"
				case "stale-generation":
					e.Generation++
				}
				j.Events[i].Event = e
			}
		}
		if slices.Contains(codes(compute(t, j, a)), "retry-habit") {
			t.Fatalf("%s dispatch became retry advice", mode)
		}
	}
}

func TestMissingOrWrongRevisionGraphCannotDiagnoseMaskedSkip(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-sha", "missing-job", "cycle"} {
		j, a := fixture(t, 10, "masked-skip")
		for i := range a.Runs {
			r := &a.Runs[i]
			if !strings.HasPrefix(r.HeadBranch, "openmerge/") {
				continue
			}
			switch mode {
			case "missing":
				r.Graph = nil
			case "wrong-sha":
				r.Graph.HeadSHA = "other"
			case "missing-job":
				r.Graph.Needs["check"] = []string{"absent"}
			case "cycle":
				r.Graph.Needs["fast"] = []string{"check"}
			}
		}
		r := compute(t, j, a)
		if slices.Contains(codes(r), "masked-skip") || r.GapCount == 0 {
			t.Fatalf("%s graph was trusted", mode)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("closed") }
func TestTextPropagatesWriteFailure(t *testing.T) {
	if err := WriteText(failingWriter{}, Report{}); err == nil {
		t.Fatal("write failure ignored")
	}
}

func TestRetryThresholdIsStrict(t *testing.T) {
	for _, n := range []int{19, 20} {
		j, a := fixture(t, n, "retry-habit")
		for i, rec := range j.Events {
			if e, ok := rec.Event.(queue.CheckRerunDispatched); ok && e.BatchID != "b1" {
				e.Unsupported = true
				j.Events[i].Event = e
			}
		}
		got := slices.Contains(codes(compute(t, j, a)), "retry-habit")
		if got != (n == 19) {
			t.Fatalf("one retry over %d runs, finding %t", n, got)
		}
	}
}

func TestWaitOverlapIsMeasuredPRTime(t *testing.T) {
	j, a := fixture(t, 10, "duplicate-pr-run")
	for i := range a.Runs {
		r := &a.Runs[i]
		if !strings.HasPrefix(r.HeadBranch, "openmerge/") {
			continue
		}
		// Queue jobs wait until after the PR completes. PR execution still
		// overlaps the recorded queue workflow's existence.
		for k := range r.Jobs {
			job := &r.Jobs[k]
			job.StartedAt = job.StartedAt.Add(200 * time.Second)
			job.CompletedAt = job.CompletedAt.Add(200 * time.Second)
			for l := range job.Steps {
				job.Steps[l].StartedAt = job.Steps[l].StartedAt.Add(200 * time.Second)
				job.Steps[l].CompletedAt = job.Steps[l].CompletedAt.Add(200 * time.Second)
			}
		}
	}
	r := compute(t, j, a)
	if !slices.Contains(codes(r), "duplicate-pr-run") {
		t.Fatal("missed duplicate work during queue wait")
	}
}
