package queuemetrics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// partnerWeek is a fixture installation's week: a carried-in batch, a clean
// batch of eight, nested bisection ending in a dequeue, a flaky rerun, an
// invalidated speculative batch, and a batch still open at --until.
func partnerWeek(t *testing.T) *journal {
	f := newJournal(t)
	// Pushed before --since (0) and landed inside the window: carried in.
	f.enqueue(-100, 10)
	b1 := f.build(-90, 10)
	f.pass(15, b1)
	// Eight PRs share one CI run.
	f.enqueue(30, 1, 2, 3, 4, 5, 6, 7, 8)
	b2 := f.build(40, 1, 2, 3, 4, 5, 6, 7, 8)
	f.pass(100, b2)
	// A batch of four fails; bisection isolates #24.
	f.enqueue(120, 21, 22, 23, 24)
	b3 := f.build(130, 21, 22, 23, 24)
	f.checks(190, b3, false)
	b4 := f.build(200, 21, 22)
	f.pass(260, b4)
	b5 := f.build(270, 23, 24)
	f.checks(330, b5, false)
	b6 := f.build(340, 23)
	f.pass(400, b6)
	b7 := f.build(410, 24)
	f.checks(470, b7, false)
	f.dequeue(475, 24, queue.ChecksFailed)
	// A flaky failure costs one accepted rerun.
	f.enqueue(500, 31)
	b8 := f.build(510, 31)
	f.rerun(570, b8)
	f.land(600, b8)
	// Speculation: #41 leaves, so #42's speculative run is invalidated and rebuilt.
	f.config(610, 4)
	f.enqueue(620, 41, 42)
	f.chain(630, 41)
	b10 := f.chain(630, 42)
	f.dequeue(650, 41, queue.Requested)
	f.emit(651, queue.BatchesInvalidated{BatchIDs: []string{b10}, Reason: "predecessor-invalidated"})
	b11 := f.chain(660, 42)
	f.pass(720, b11)
	// Mid-batch at --until (1000): pushed and passed inside, landed after.
	f.enqueue(900, 51)
	b12 := f.chain(950, 51)
	f.checks(990, b12, true)
	f.land(1100, b12)
	return f
}

func window(since, until int) SavingsWindow { return SavingsWindow{Since: at(since), Until: at(until)} }

func mustSavings(t *testing.T, f *journal, w SavingsWindow) Savings {
	t.Helper()
	s, err := ComputeSavings(f.j, w)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSavingsBaselineMathIsExact(t *testing.T) {
	s := mustSavings(t, partnerWeek(t), window(0, 1000))
	// Landed in [0, 1000): #10, #1-#8, #21, #22, #23, #31, #42.
	if s.PRsLanded != 14 {
		t.Fatalf("landed %d", s.PRsLanded)
	}
	// b1 (carried in) and b2-b12 were pushed; b8 had one accepted rerun.
	want := RunCounts{
		Executed: 13, TestRefsBuilt: 12, Reruns: 1, CarriedIn: 1,
		Outcomes: RunOutcomes{Landed: 6, Invalidated: 1, Discarded: 4, Open: 1},
		Baseline: 15, Saved: 2, SavedFraction: 2.0 / 15, RunsPerLanded: 13.0 / 14,
	}
	if s.Runs != want {
		t.Fatalf("runs\n got %+v\nwant %+v", s.Runs, want)
	}
	if s.Runs.Baseline != s.PRsLanded+s.Runs.Reruns || s.Runs.Executed != s.Runs.TestRefsBuilt+s.Runs.Reruns || s.Runs.Saved != s.Runs.Baseline-s.Runs.Executed {
		t.Fatal("baseline identities do not hold")
	}
	if s.Batches != (BatchSizes{Landed: 6, Mean: 14.0 / 6, P50: 1, P90: 8, Max: 8}) {
		t.Fatalf("batches %+v", s.Batches)
	}
	// Ten runs reached a terminal result; nine took 60s or less, b1 took 105s.
	m := s.Minutes
	if m.Source != MinutesObserved || m.ObservedRuns != 10 || m.PerRun != 1 || m.ObservedMedian != 1 || m.ObservedMax != 1.75 || m.ExecutedMinutes != 13 || m.BaselineMinutes != 15 || m.SavedMinutes != 2 {
		t.Fatalf("minutes %+v", m)
	}
	if s.EnqueueToLanded != (Latency{Count: 14, P50: 70, P90: 140}) {
		t.Fatalf("latency %+v", s.EnqueueToLanded)
	}
	iso := Isolation{Bisections: 2, BisectionRuns: 4, Dequeued: 2, ByReason: map[string]int{"checks-failed": 1, "requested": 1}}
	if !reflect.DeepEqual(s.Isolation, iso) {
		t.Fatalf("isolation %+v", s.Isolation)
	}
}

func TestSavingsFullSuiteMinutesOverridesObserved(t *testing.T) {
	w := window(0, 1000)
	w.FullSuiteMinutes = 12.5
	m := mustSavings(t, partnerWeek(t), w).Minutes
	if m.Source != MinutesFromFlag || m.PerRun != 12.5 || m.ExecutedMinutes != 13*12.5 || m.BaselineMinutes != 15*12.5 || m.SavedMinutes != 25 || m.ObservedMedian != 1 {
		t.Fatalf("minutes %+v", m)
	}
	w.FullSuiteMinutes = -1
	if _, err := ComputeSavings(partnerWeek(t).j, w); err == nil {
		t.Fatal("negative minutes accepted")
	}
}

// Invalidated speculative runs cost minutes, so an unlucky window reports a
// negative saving instead of clamping to zero.
func TestSavingsNeverClampsNegative(t *testing.T) {
	f := newJournal(t)
	f.config(0, 4)
	f.enqueue(10, 1, 2)
	f.chain(20, 1)
	b2 := f.chain(20, 2)
	f.dequeue(30, 1, queue.Requested)
	f.emit(31, queue.BatchesInvalidated{BatchIDs: []string{b2}, Reason: "predecessor-invalidated"})
	b3 := f.chain(40, 2)
	f.pass(100, b3)
	s := mustSavings(t, f, window(0, 200))
	if s.PRsLanded != 1 || s.Runs.Executed != 3 || s.Runs.Baseline != 1 || s.Runs.Saved != -2 || s.Runs.SavedFraction != -2 || s.Runs.Outcomes != (RunOutcomes{Landed: 1, Invalidated: 1, Discarded: 1}) {
		t.Fatalf("runs %+v", s.Runs)
	}
	if !strings.Contains(strings.Join(s.Warnings, "\n"), "more CI runs than the baseline") {
		t.Fatalf("warnings %v", s.Warnings)
	}
}

// A test ref that was never pushed ran no CI.
func TestSavingsSkipsUnpushedTestRefs(t *testing.T) {
	f := newJournal(t)
	f.enqueue(10, 1)
	f.emit(20, queue.BatchStarted{BatchID: "b1", BaseSHA: f.s.BaseSHA, PRs: f.batchPRs([]int{1}), At: at(20)})
	f.emit(21, queue.TestRefBuildFailed{BatchID: "b1", At: at(21)})
	f.next = 1
	b2 := f.build(30, 1)
	f.pass(90, b2)
	s := mustSavings(t, f, window(0, 200))
	if s.Runs.TestRefsBuilt != 1 || s.Runs.Executed != 1 || s.Runs.Saved != 0 {
		t.Fatalf("runs %+v", s.Runs)
	}
}

func TestSavingsEmptyWindow(t *testing.T) {
	s := mustSavings(t, partnerWeek(t), window(2000, 3000))
	if s.PRsLanded != 0 || s.Runs != (RunCounts{}) || s.Batches != (BatchSizes{}) || s.EnqueueToLanded != (Latency{}) || s.Isolation.Dequeued != 0 || len(s.Isolation.ByReason) != 0 {
		t.Fatalf("empty window %+v", s)
	}
	if s.Minutes.Source != MinutesUnavailable || s.Minutes.SavedMinutes != 0 || len(s.Warnings) != 0 {
		t.Fatalf("minutes %+v warnings %v", s.Minutes, s.Warnings)
	}
	empty := newJournal(t)
	if s := mustSavings(t, empty, window(0, 1)); s.PRsLanded != 0 || s.Runs.Executed != 0 {
		t.Fatalf("empty journal %+v", s)
	}
	for _, w := range []SavingsWindow{window(10, 10), window(10, 5), {Until: at(5)}} {
		if _, err := ComputeSavings(empty.j, w); err == nil {
			t.Fatalf("invalid window %+v accepted", w)
		}
	}
}

// Window boundaries cut through batches. Landings count by landing time; runs
// count when pushed in the window or when they tested a PR landed in it.
func TestSavingsWindowBoundaryMidBatch(t *testing.T) {
	f := partnerWeek(t)
	// --until between b12's push (950) and landing (1100): run counted, PR not.
	s := mustSavings(t, f, window(900, 1000))
	if s.PRsLanded != 0 || s.Runs.TestRefsBuilt != 1 || s.Runs.Outcomes.Open != 1 || s.Runs.Saved != -1 {
		t.Fatalf("until mid-batch %+v", s.Runs)
	}
	// --since between b12's push and landing: carried in with its landing.
	s = mustSavings(t, f, window(1000, 1200))
	if s.PRsLanded != 1 || s.Runs.TestRefsBuilt != 1 || s.Runs.CarriedIn != 1 || s.Runs.Outcomes.Landed != 1 || s.Runs.Saved != 0 {
		t.Fatalf("since mid-batch %+v", s.Runs)
	}
	// --since mid-bisection (after b3 failed at 190, before #21/#22 land):
	// the failed parent tested landed PRs, so it is carried in.
	s = mustSavings(t, f, window(195, 265))
	if s.PRsLanded != 2 || s.Runs.TestRefsBuilt != 2 || s.Runs.CarriedIn != 1 || s.Isolation.Bisections != 0 || s.Isolation.BisectionRuns != 1 {
		t.Fatalf("since mid-bisection runs=%+v isolation=%+v", s.Runs, s.Isolation)
	}
	// The prefix after --until never influences the report.
	trimmed := *f
	trimmed.j.Events = nil
	for _, rec := range f.j.Events {
		if rec.RecordedAt.Before(at(1000)) {
			trimmed.j.Events = append(trimmed.j.Events, rec)
		}
	}
	a, b := mustSavings(t, f, window(0, 1000)), mustSavings(t, &trimmed, window(0, 1000))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("events after --until changed the report")
	}
}

func TestSavingsGoldens(t *testing.T) {
	w := window(0, 1000)
	for name, minutes := range map[string]float64{"savings-observed": 0, "savings-flag": 12.5} {
		w.FullSuiteMinutes = minutes
		s := mustSavings(t, partnerWeek(t), w)
		var js, text bytes.Buffer
		enc := json.NewEncoder(&js)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s); err != nil {
			t.Fatal(err)
		}
		if err := WriteSavingsText(&text, s); err != nil {
			t.Fatal(err)
		}
		golden(t, name+".json", js.Bytes())
		golden(t, name+".txt", text.Bytes())
	}
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetIndent("", "  ")
	if err := enc.Encode(mustSavings(t, newJournal(t), window(0, 1))); err != nil {
		t.Fatal(err)
	}
	golden(t, "savings-empty.json", js.Bytes())
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}
