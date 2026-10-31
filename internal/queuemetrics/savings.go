package queuemetrics

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

// SavingsVersion identifies the JSON contract of Savings.
const SavingsVersion = 1

// BaselineDefinition is printed in every report so a reader never has to guess
// what OpenMerge is compared against.
const BaselineDefinition = "one full CI run per PR landed in the window, plus every CI rerun the queue dispatched for the runs counted as executed. " +
	"It models testing each PR alone before it merges, with the same flaky reruns. " +
	"It ignores the runs a one-at-a-time queue would also spend on PRs that failed or were dequeued, and on retesting after the base moves, " +
	"so it is a lower bound on the cost without batching."

// ExecutedDefinition states what counts as a CI run OpenMerge executed.
const ExecutedDefinition = "one full CI run per queue test ref OpenMerge pushed, whatever happened to it afterwards " +
	"(landed, failed, bisected, invalidated by speculation, or still running at --until), plus each rerun the forge accepted. " +
	"Pushes and rerun acceptances recorded after their batch left the queue still count; repeated confirmations count once. " +
	"A run belongs to the window when its test ref was pushed in [since, until) or it tested a PR that landed in the window. " +
	"Batches whose test ref was never pushed (build failure or conflict) ran no CI and are not counted."

// Minute sources label how per-run CI minutes were obtained.
const (
	MinutesFromFlag     = "full-suite-minutes"
	MinutesObserved     = "observed"
	MinutesUnavailable  = "unavailable"
	observedMinutesNote = "median journal wall-clock from test-ref push to the first terminal CI result; includes runner wait and polling delay, and is not billed job minutes"
)

// ObservedMinutesWarning is printed under observed minutes. Wall-clock can err
// either way: runner queue time inflates it, and parallel jobs bill more
// minutes than the wall-clock they span.
const ObservedMinutesWarning = "minutes are observed wall-clock, not billed. Runner wait inflates them; parallel jobs bill more than their wall-clock. " +
	"Pass --full-suite-minutes to quote cost."

// SavingsWindow selects [Since, Until) by journal recording time.
type SavingsWindow struct {
	Since, Until time.Time
	// FullSuiteMinutes, when positive, is the operator's minutes for one full
	// CI run. It takes precedence over durations observed in the journal.
	FullSuiteMinutes float64
}

// Savings is the per-queue CI savings report. All counts are derived from the
// journal alone; no forge data is read.
type Savings struct {
	Version      int       `json:"version"`
	Installation string    `json:"installation"`
	Repository   string    `json:"repository"`
	BaseBranch   string    `json:"base_branch"`
	Lane         string    `json:"lane"`
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`

	Baseline string `json:"baseline_definition"`
	Executed string `json:"executed_definition"`

	PRsLanded       int            `json:"prs_landed"`
	Batches         BatchSizes     `json:"batches"`
	Runs            RunCounts      `json:"ci_runs"`
	Minutes         MinutesSummary `json:"ci_minutes"`
	EnqueueToLanded Latency        `json:"enqueue_to_landed"`
	Isolation       Isolation      `json:"failure_isolation"`
	// Shadow is present only when the queue has an ADR 0015 shadow ledger.
	// It is potential additional savings, never applied, and changes no
	// count above.
	Shadow   *ShadowSavings `json:"potential_additional_savings_shadow,omitempty"`
	Warnings []string       `json:"warnings"`
}

// ShadowDefinition is printed with every shadow section.
const ShadowDefinition = "exact tree dedupe in shadow mode: test builds in the window whose key (tree OID, workflow blob OIDs, merge_when digest, runner image, queue) " +
	"equalled an earlier completed queue build that passed, or that failed and was confirmed by a retry or as a singleton. " +
	"Unconfirmed failures and unknown keys are never counted. Nothing was reused; every build still ran CI."

// ShadowSavings reports what exact-tree dedupe would have avoided.
type ShadowSavings struct {
	Definition string `json:"definition"`
	treededupe.Summary
	// AvoidableFullSuiteMinutes prices the hits at --full-suite-minutes.
	AvoidableFullSuiteMinutes float64 `json:"avoidable_full_suite_minutes,omitempty"`
}

// BatchSizes describes the batches that landed in the window.
type BatchSizes struct {
	Landed int     `json:"landed"`
	Mean   float64 `json:"mean_size"`
	P50    int     `json:"p50_size"`
	P90    int     `json:"p90_size"`
	Max    int     `json:"max_size"`
}

// RunCounts compares the CI runs OpenMerge executed with the baseline.
type RunCounts struct {
	Executed      int `json:"executed"`
	TestRefsBuilt int `json:"test_refs_built"`
	Reruns        int `json:"reruns"`
	// CarriedIn counts test refs pushed before --since that tested a PR landed
	// in the window. They are included in Executed.
	CarriedIn int `json:"carried_in"`
	// RerunsUnconfirmed were requested but the forge never confirmed dispatch.
	// They are reported, not counted.
	RerunsUnconfirmed int         `json:"reruns_unconfirmed"`
	Outcomes          RunOutcomes `json:"test_ref_outcomes"`
	Baseline          int         `json:"baseline"`
	Saved             int         `json:"saved"`
	// SavedFraction is Saved / Baseline, zero when the baseline is zero. It is
	// negative when OpenMerge executed more runs than the baseline.
	SavedFraction float64 `json:"saved_fraction"`
	RunsPerLanded float64 `json:"runs_per_landed_pr"`
}

// RunOutcomes partitions TestRefsBuilt by what happened to each test ref by --until.
type RunOutcomes struct {
	Landed      int `json:"landed"`
	Invalidated int `json:"invalidated"`
	// Discarded covers failed, bisected and rebuilt batches.
	Discarded int `json:"discarded"`
	Open      int `json:"open"`
}

// MinutesSummary estimates CI minutes. Per-run minutes are applied to every
// executed and baseline run, including partial runs that were cancelled.
type MinutesSummary struct {
	Source          string  `json:"source"`
	Note            string  `json:"note"`
	PerRun          float64 `json:"per_run_minutes"`
	ObservedRuns    int     `json:"observed_runs"`
	ObservedMedian  float64 `json:"observed_median_minutes"`
	ObservedMax     float64 `json:"observed_max_minutes"`
	ExecutedMinutes float64 `json:"executed_minutes"`
	BaselineMinutes float64 `json:"baseline_minutes"`
	SavedMinutes    float64 `json:"saved_minutes"`
}

// Latency is nearest-rank enqueue-to-landed time for PRs landed in the window.
type Latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_seconds"`
	P90   float64 `json:"p90_seconds"`
}

// Isolation summarizes how failures were contained.
type Isolation struct {
	Bisections    int            `json:"bisections"`
	BisectionRuns int            `json:"bisection_runs"`
	Dequeued      int            `json:"dequeued"`
	ByReason      map[string]int `json:"dequeues_by_reason"`
}

// savingsBatch is what the report remembers about a batch after the fold drops
// it, so a test ref pushed late can still be attributed and labelled.
type savingsBatch struct {
	admissions []uint64
	bisection  bool
	outcome    string
}

func (b *savingsBatch) observe(s queue.State, batch queue.Batch) {
	b.bisection = b.bisection || batch.FromBisection
	for _, pr := range batch.PRs {
		if en := Entry(s, pr.PR); en != nil && !slices.Contains(b.admissions, en.AdmissionID) {
			b.admissions = append(b.admissions, en.AdmissionID)
		}
	}
}

// savingsRun is one pushed test ref, identified by batch and tested SHA.
type savingsRun struct {
	batch      *savingsBatch
	builtAt    time.Time
	terminalAt time.Time
	reruns     int
	unknown    int
	outcome    string
}

// rerunAttempt collects every journal record of one rerun request. A forge
// acceptance anywhere counts it once; it is unconfirmed only if nothing accepted it.
type rerunAttempt struct {
	run      *savingsRun
	accepted bool
	unknown  bool
}

const (
	outcomeOpen        = ""
	outcomeLanded      = "landed"
	outcomeInvalidated = "invalidated"
	outcomeDiscarded   = "discarded"
)

// ComputeSavings replays the journal prefix recorded before w.Until through
// the production fold and summarizes the window. It is a pure function of the
// journal and window.
func ComputeSavings(j store.Journal, w SavingsWindow) (Savings, error) {
	r := Savings{
		Version: SavingsVersion, Installation: j.Key.Installation, Repository: j.Key.Repository, BaseBranch: j.Key.BaseBranch, Lane: j.Key.Lane,
		Since: w.Since.UTC(), Until: w.Until.UTC(), Baseline: BaselineDefinition, Executed: ExecutedDefinition,
		Isolation: Isolation{ByReason: map[string]int{}}, Warnings: []string{},
	}
	if w.Since.IsZero() || !w.Until.After(w.Since) {
		return r, fmt.Errorf("window must have a positive duration")
	}
	if w.FullSuiteMinutes < 0 {
		return r, fmt.Errorf("full-suite minutes must not be negative")
	}
	s := j.Seed
	batches := map[string]*savingsBatch{}
	batchOf := func(id string) *savingsBatch {
		if batches[id] == nil {
			batches[id] = &savingsBatch{}
		}
		return batches[id]
	}
	// Runs are discovered from the push records themselves, not from batches
	// that survive the fold: a ref pushed after its batch was dequeued or
	// invalidated still ran CI. Repeated confirmations of the same ref count once.
	runs := map[string]*savingsRun{}
	bySHA := map[string]*savingsRun{}
	var order []*savingsRun
	pushed := func(batchID, sha string, at time.Time, live queue.State) *savingsRun {
		key := batchID + "\x00" + sha
		if run := runs[key]; run != nil {
			return run
		}
		b := batchOf(batchID)
		run := &savingsRun{batch: b, builtAt: at}
		if live.Batch(batchID) == nil {
			run.outcome = cmp.Or(b.outcome, outcomeDiscarded)
		}
		runs[key], bySHA[sha] = run, run
		order = append(order, run)
		return run
	}
	reruns := map[string]*rerunAttempt{}
	var rerunOrder []*rerunAttempt
	rerun := func(batchID, sha string, generation uint64, at time.Time, live queue.State) *rerunAttempt {
		key := fmt.Sprintf("%s\x00%s\x00%d", batchID, sha, generation)
		if a := reruns[key]; a != nil {
			return a
		}
		// A rerun implies its ref was pushed; fail toward counting that run.
		a := &rerunAttempt{run: pushed(batchID, sha, at, live)}
		reruns[key] = a
		rerunOrder = append(rerunOrder, a)
		return a
	}
	// Refs already under test in the seed were pushed before the journal began.
	for _, b := range s.Batches() {
		batchOf(b.ID).observe(s, *b)
		if b.TestedSHA != "" {
			pushed(b.ID, b.TestedSHA, b.StartedTestingAt, s)
		}
	}
	landedAdmissions := map[uint64]bool{}
	var sizes, latencies []float64
	rejectedLandings := 0
	for _, rec := range j.Events {
		if rec.RecordedAt.IsZero() {
			return r, fmt.Errorf("event %d has no recorded timestamp", rec.ID)
		}
		// The report covers the journal prefix before --until, in log order.
		if !rec.RecordedAt.Before(w.Until) {
			break
		}
		inside := !rec.RecordedAt.Before(w.Since)
		before := s
		s = queue.Apply(s, rec.Event)
		for _, b := range s.Batches() {
			batchOf(b.ID).observe(s, *b)
		}

		landedBatch, accepted, isLanded := LandedBatch(before, s, rec.Event)
		invalidated := map[string]bool{}
		if e, ok := rec.Event.(queue.BatchesInvalidated); ok {
			for _, id := range e.BatchIDs {
				invalidated[id] = true
			}
		}
		for _, old := range before.Batches() {
			if s.Batch(old.ID) != nil {
				continue
			}
			outcome := outcomeDiscarded
			switch {
			case accepted && landedBatch.ID == old.ID:
				outcome = outcomeLanded
			case invalidated[old.ID]:
				outcome = outcomeInvalidated
			}
			b := batchOf(old.ID)
			b.observe(before, *old)
			if b.outcome == outcomeOpen {
				b.outcome = outcome
			}
			for _, run := range order {
				if run.batch == b && run.outcome == outcomeOpen {
					run.outcome = outcome
				}
			}
			// A failed multi-PR batch splits into smaller bisection halves. A
			// dequeue or rebuild keeps the composition and is not a bisection.
			_, dequeue := rec.Event.(queue.Dequeued)
			if inside && outcome == outcomeDiscarded && !dequeue && len(old.PRs) > 1 && s.PendingBisection() && s.PendingBatchSize() > 0 && s.PendingBatchSize() < len(old.PRs) {
				r.Isolation.Bisections++
			}
		}

		switch e := rec.Event.(type) {
		case queue.TestRefBuilt:
			if e.BatchID != "" && e.TestedSHA != "" {
				pushed(e.BatchID, e.TestedSHA, rec.RecordedAt, s)
			}
		case queue.ChainedTestRefBuilt:
			if e.BatchID != "" && e.TestedSHA != "" {
				pushed(e.BatchID, e.TestedSHA, rec.RecordedAt, s)
			}
		case queue.FastForwardRefBuilt:
			if e.BatchID != "" && e.TestedSHA != "" {
				pushed(e.BatchID, e.TestedSHA, rec.RecordedAt, s)
			}
		case queue.CheckRerunDispatched:
			if e.BatchID != "" && e.TestedSHA != "" {
				a := rerun(e.BatchID, e.TestedSHA, e.Generation, rec.RecordedAt, s)
				a.accepted = a.accepted || !e.OutcomeUnknown && !e.Unsupported
				a.unknown = a.unknown || e.OutcomeUnknown
			}
		case queue.CheckRerunDispatchProgress:
			if e.BatchID != "" && e.TestedSHA != "" && e.Target.Accepted {
				rerun(e.BatchID, e.TestedSHA, e.Generation, rec.RecordedAt, s).accepted = true
			}
		case queue.ChecksCompleted:
			if run := bySHA[e.TestedSHA]; run != nil && run.terminalAt.IsZero() && rec.RecordedAt.After(run.builtAt) {
				run.terminalAt = rec.RecordedAt
			}
		case queue.ChecksObserved:
			if run := bySHA[e.TestedSHA]; run != nil && e.Terminal && run.terminalAt.IsZero() && rec.RecordedAt.After(run.builtAt) {
				run.terminalAt = rec.RecordedAt
			}
		case queue.Dequeued:
			en := Entry(before, e.PR)
			if inside && en != nil && en.AdmissionID == e.AdmissionID {
				if after := Entry(s, e.PR); after == nil || after.AdmissionID != e.AdmissionID {
					r.Isolation.Dequeued++
					r.Isolation.ByReason[string(e.Reason)]++
				}
			}
		}

		if isLanded && !accepted && inside {
			rejectedLandings++
		}
		if accepted && inside {
			sizes = append(sizes, float64(len(landedBatch.PRs)))
			for _, pr := range landedBatch.PRs {
				en := Entry(before, pr.PR)
				if en == nil {
					return r, fmt.Errorf("landing %d has no admission for #%d", rec.ID, pr.PR)
				}
				seconds := rec.RecordedAt.Sub(en.EnqueuedAt).Seconds()
				if seconds < 0 {
					return r, fmt.Errorf("landing %d precedes enqueue timestamp", rec.ID)
				}
				landedAdmissions[en.AdmissionID] = true
				latencies = append(latencies, seconds)
			}
		}
	}
	for _, a := range rerunOrder {
		switch {
		case a.accepted:
			a.run.reruns++
		case a.unknown:
			a.run.unknown++
		}
	}

	var observed []float64
	for _, run := range order {
		attributed := !run.builtAt.Before(w.Since)
		if !attributed {
			for _, id := range run.batch.admissions {
				attributed = attributed || landedAdmissions[id]
			}
			if !attributed {
				continue
			}
			r.Runs.CarriedIn++
		}
		r.Runs.TestRefsBuilt++
		r.Runs.Reruns += run.reruns
		r.Runs.RerunsUnconfirmed += run.unknown
		if run.batch.bisection {
			r.Isolation.BisectionRuns++
		}
		switch run.outcome {
		case outcomeLanded:
			r.Runs.Outcomes.Landed++
		case outcomeInvalidated:
			r.Runs.Outcomes.Invalidated++
		case outcomeDiscarded:
			r.Runs.Outcomes.Discarded++
		default:
			r.Runs.Outcomes.Open++
		}
		if !run.terminalAt.IsZero() {
			observed = append(observed, run.terminalAt.Sub(run.builtAt).Minutes())
		}
	}

	r.PRsLanded = len(latencies)
	r.Batches.Landed = len(sizes)
	if len(sizes) > 0 {
		r.Batches.Mean = float64(r.PRsLanded) / float64(len(sizes))
		r.Batches.P50 = int(Percentile(sizes, .5))
		r.Batches.P90 = int(Percentile(sizes, .9))
		r.Batches.Max = int(slices.Max(sizes))
	}
	r.EnqueueToLanded = Latency{Count: len(latencies), P50: Percentile(latencies, .5), P90: Percentile(latencies, .9)}

	r.Runs.Executed = r.Runs.TestRefsBuilt + r.Runs.Reruns
	r.Runs.Baseline = r.PRsLanded + r.Runs.Reruns
	r.Runs.Saved = r.Runs.Baseline - r.Runs.Executed
	if r.Runs.Baseline > 0 {
		r.Runs.SavedFraction = float64(r.Runs.Saved) / float64(r.Runs.Baseline)
	}
	if r.PRsLanded > 0 {
		r.Runs.RunsPerLanded = float64(r.Runs.Executed) / float64(r.PRsLanded)
	}

	m := &r.Minutes
	m.ObservedRuns = len(observed)
	if len(observed) > 0 {
		m.ObservedMedian = Percentile(observed, .5)
		m.ObservedMax = slices.Max(observed)
	}
	switch {
	case w.FullSuiteMinutes > 0:
		m.Source, m.PerRun = MinutesFromFlag, w.FullSuiteMinutes
		m.Note = "operator-supplied minutes for one full CI run"
	case len(observed) > 0:
		m.Source, m.PerRun, m.Note = MinutesObserved, m.ObservedMedian, observedMinutesNote
	default:
		m.Source = MinutesUnavailable
		m.Note = "no completed CI run in the window and no --full-suite-minutes; minutes are not estimated"
	}
	m.ExecutedMinutes = float64(r.Runs.Executed) * m.PerRun
	m.BaselineMinutes = float64(r.Runs.Baseline) * m.PerRun
	m.SavedMinutes = float64(r.Runs.Saved) * m.PerRun

	if r.Runs.CarriedIn > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d test ref(s) pushed before --since tested PRs that landed in the window; they are counted as executed", r.Runs.CarriedIn))
	}
	if r.Runs.Outcomes.Open > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d test ref(s) were still open at --until; they are counted as executed, and their PRs count only if they landed in the window", r.Runs.Outcomes.Open))
	}
	if r.Runs.RerunsUnconfirmed > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d rerun request(s) have no confirmed forge dispatch and are not counted", r.Runs.RerunsUnconfirmed))
	}
	if rejectedLandings > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d landed event(s) in the window were not accepted by replay and were ignored; run the landing audit", rejectedLandings))
	}
	if r.Runs.Saved < 0 {
		r.Warnings = append(r.Warnings, "OpenMerge executed more CI runs than the baseline in this window")
	}
	if j.TreeDedupeShadow != nil {
		r.Shadow, r.Warnings = shadowSavings(j.TreeDedupeShadow, w, r.Warnings)
	}
	return r, nil
}

// shadowSavings summarizes the shadow ledger for the window. An unreadable
// ledger is a warning, never a report failure.
func shadowSavings(raw []byte, w SavingsWindow, warnings []string) (*ShadowSavings, []string) {
	ledger, err := treededupe.Decode(raw)
	if err != nil {
		return nil, append(warnings, "the tree dedupe shadow ledger is unreadable by this binary and is not reported")
	}
	s := &ShadowSavings{Definition: ShadowDefinition, Summary: ledger.Summarize(w.Since, w.Until)}
	if w.FullSuiteMinutes > 0 {
		s.AvoidableFullSuiteMinutes = float64(s.WouldReuseSuccess+s.WouldReuseFailure) * w.FullSuiteMinutes
	}
	if s.Dropped > 0 && s.Since.After(w.Since) {
		warnings = append(warnings, fmt.Sprintf("the tree dedupe shadow ledger retains builds only since %s; earlier shadow hits in the window are not counted", s.Since.UTC().Format(time.RFC3339)))
	}
	return s, warnings
}
