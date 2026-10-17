package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Boundary identifies an instant, or the exact ConfigApplied event at that instant.
// Event IDs disambiguate admissions recorded at the same timestamp as a policy change.
type Boundary struct {
	Time    time.Time `json:"time"`
	EventID int64     `json:"event_id,omitempty"`
	Commit  string    `json:"commit,omitempty"`
}

func boundary(j store.Journal, value string) (Boundary, error) {
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return Boundary{Time: at.UTC()}, nil
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(value, "event:"), 10, 64)
	commit := strings.TrimPrefix(value, "commit:")
	var found []Boundary
	for _, r := range j.Events {
		if e, ok := r.Event.(queue.ConfigApplied); ok && e.Validate() == nil && (id > 0 && r.ID == id || id == 0 && e.Commit == commit) {
			found = append(found, Boundary{Time: r.RecordedAt, EventID: r.ID, Commit: e.Commit})
		}
	}
	if len(found) != 1 {
		return Boundary{}, fmt.Errorf("boundary %q must be RFC3339 or identify exactly one ConfigApplied event (event:<id> or commit:<full SHA>); found %d", value, len(found))
	}
	return found[0], nil
}
func (b Boundary) reached(r store.JournalEvent) bool {
	if b.EventID != 0 {
		return r.ID >= b.EventID
	}
	return !r.RecordedAt.Before(b.Time)
}

type Window struct {
	From Boundary `json:"from"`
	To   Boundary `json:"to"`
}

func (w Window) contains(r store.JournalEvent) bool { return w.From.reached(r) && !w.To.reached(r) }
func (w Window) validate() error {
	if w.From.Time.IsZero() || !w.To.Time.After(w.From.Time) {
		return fmt.Errorf("window must have a positive duration")
	}
	if w.From.EventID > 0 && w.To.EventID > 0 && w.To.EventID <= w.From.EventID {
		return fmt.Errorf("window event IDs are reversed")
	}
	return nil
}

type Admission struct {
	EventID     int64     `json:"event_id"`
	PR          int       `json:"pr"`
	AdmissionID uint64    `json:"admission_id"`
	AcceptedAt  time.Time `json:"accepted_at"`
	EnqueuedAt  time.Time `json:"enqueued_at"`
	QueueLength int       `json:"queue_length"`
}
type Change struct {
	PR               int       `json:"pr"`
	AdmissionID      uint64    `json:"admission_id"`
	AdmissionEventID int64     `json:"admission_event_id"`
	LandedEventID    int64     `json:"landed_event_id"`
	BatchID          string    `json:"batch_id"`
	EnqueuedAt       time.Time `json:"enqueued_at"`
	FirstEnqueuedAt  time.Time `json:"first_enqueued_at"`
	LandedAt         time.Time `json:"landed_at"`
	Seconds          float64   `json:"seconds"`
	FirstSeconds     float64   `json:"first_seconds"`
	Position         uint64    `json:"chain_position"`
	Tested           bool      `json:"tested"`
}
type Percentiles struct {
	Median float64 `json:"median_seconds"`
	P95    float64 `json:"p95_seconds"`
}
type WindowPoint struct {
	EventID     int64     `json:"event_id,omitempty"`
	At          time.Time `json:"at"`
	W           int       `json:"w"`
	OpenBatches int       `json:"open_batches"`
	MaxDepth    int       `json:"max_depth"`
}
type Halving struct {
	EventID int64               `json:"event_id"`
	At      time.Time           `json:"at"`
	Before  int                 `json:"before"`
	After   int                 `json:"after"`
	Failure queue.WindowFailure `json:"failure"`
}
type Dequeue struct {
	AdmissionID uint64    `json:"admission_id"`
	PR          int       `json:"pr"`
	EventID     int64     `json:"event_id"`
	At          time.Time `json:"at"`
}
type HeldArrival struct {
	PR         int       `json:"pr"`
	EligibleAt time.Time `json:"first_eligible_at"`
	AdmittedAt time.Time `json:"admitted_at"`
}
type Report struct {
	ReportAt           time.Time     `json:"report_at"`
	QueueDrainedAt     time.Time     `json:"queue_drained_at"`
	DrainEnd           time.Time     `json:"drain_end"`
	DrainSeconds       float64       `json:"drain_seconds"`
	DrainComplete      bool          `json:"drain_complete"`
	NextConfig         *Boundary     `json:"next_config,omitempty"`
	Warnings           []string      `json:"warnings,omitempty"`
	Batches            []string      `json:"started_batches"`
	HeldArrivals       []HeldArrival `json:"held_arrivals"`
	Dequeues           []Dequeue     `json:"dequeues"`
	Dequeued           int           `json:"dequeued"`
	Pending            int           `json:"pending"`
	LandedAtClose      int           `json:"landed_at_window_close"`
	EngagedAtClose     int           `json:"position_ge_2_at_window_close"`
	DeepEngagedAtClose int           `json:"position_ge_3_at_window_close"`
	DeepEngaged        int           `json:"landed_at_position_ge_3"`
	AbortedReason      string        `json:"aborted_reason,omitempty"`

	Version              int            `json:"version"`
	Queue                store.QueueKey `json:"queue"`
	Window               Window         `json:"window"`
	CIConfig             string         `json:"ci_config"`
	LogReviewed          bool           `json:"log_reviewed"`
	Changes              []Change       `json:"changes"`
	Admissions           []Admission    `json:"admissions"`
	Landed               int            `json:"landed"`
	Engaged              int            `json:"landed_at_position_ge_2"`
	TimeToMerge          Percentiles    `json:"time_to_merge"`
	FirstEnqueue         Percentiles    `json:"first_enqueue_to_merge"`
	ArrivalsPerDay       float64        `json:"arrivals_per_day"`
	ArrivalsByDay        map[string]int `json:"arrivals_by_utc_day"`
	WindowHistory        []WindowPoint  `json:"window_history"`
	Halvings             []Halving      `json:"halvings"`
	NonIdleSeconds       float64        `json:"non_idle_seconds"`
	MultipleBatchSeconds float64        `json:"multiple_batch_seconds"`
	MultipleBatchShare   float64        `json:"multiple_batch_share_of_non_idle"`
	Runs                 []Attempt      `json:"run_attempts"`
	RunAttempts          int            `json:"run_attempt_count"`
	RunsPerMerge         float64        `json:"runs_per_merge"`
	Audit                *AuditResult   `json:"audit,omitempty"`
}

type landing struct {
	Record   store.JournalEvent
	Previous string
	Observed string
	Batch    *queue.Batch
	Accepted bool
}

func entry(s queue.State, pr int) *queue.Entry {
	for _, e := range s.Entries {
		if e.PR == pr {
			return &e
		}
	}
	return nil
}
func effectiveConfig(cfg queue.Config, before, after queue.State, e queue.Event) queue.Config {
	changed := before.ConfigDigest != after.ConfigDigest || !before.WindowInitialized() && after.WindowInitialized()
	if c, ok := e.(queue.ConfigApplied); ok && changed && c.Validate() == nil && after.ConfigDigest == c.Digest {
		cfg.SpeculationMinDepth, cfg.SpeculationMaxDepth = 1, 1
		if c.Window != nil {
			cfg.SpeculationMinDepth, cfg.SpeculationMaxDepth = c.Window.MinDepth, c.Window.MaxDepth
		}
	}
	return cfg
}
func point(s queue.State, cfg queue.Config, id int64, at time.Time) WindowPoint {
	return WindowPoint{EventID: id, At: at, W: s.Window(cfg), OpenBatches: len(s.Batches()), MaxDepth: cfg.MaxDepth()}
}
func samePoint(a, b WindowPoint) bool {
	return a.W == b.W && a.OpenBatches == b.OpenBatches && a.MaxDepth == b.MaxDepth
}

func reportJournal(j store.Journal, w Window) (Report, error) {
	return reportJournalAt(j, w, time.Now().UTC())
}

func reportJournalAt(j store.Journal, w Window, asOf time.Time) (Report, error) {
	r := Report{Version: 2, Queue: j.Key, Window: w, ReportAt: asOf, ArrivalsByDay: map[string]int{}}
	if err := w.validate(); err != nil {
		return r, err
	}
	if asOf.Before(w.To.Time) {
		return r, fmt.Errorf("report time precedes admission close")
	}
	s, cfg := j.Seed, queue.Config{}
	admissions := map[uint64]Admission{}
	cohort := map[uint64]bool{}
	pending := map[uint64]bool{}
	first := map[int]time.Time{}
	lastTime := w.From.Time
	initialized, periodEnded := false, false
	idle := func() bool { return len(s.Entries) == 0 && len(s.Batches()) == 0 && len(pending) == 0 }
	for _, rec := range j.Events {
		if rec.RecordedAt.After(asOf) {
			continue
		}
		if rec.RecordedAt.IsZero() {
			return r, fmt.Errorf("event %d has no recorded timestamp", rec.ID)
		}
		inside := w.contains(rec)
		inPeriod := w.From.reached(rec) && !periodEnded
		if inside && !initialized {
			r.WindowHistory = append(r.WindowHistory, point(s, cfg, 0, w.From.Time))
			if len(s.Entries) > 0 || len(s.Batches()) > 0 {
				r.Warnings = append(r.Warnings, "queue was not idle at period start")
			}
			initialized = true
		}
		if inPeriod {
			if w.To.reached(rec) && r.QueueDrainedAt.IsZero() && idle() {
				r.QueueDrainedAt = maxTime(w.To.Time, lastTime)
			}
			nextTime := maxTime(lastTime, rec.RecordedAt)
			accumulate(&r, s, lastTime, nextTime)
			lastTime = nextTime
		}
		before := s
		s = queue.Apply(s, rec.Event)
		cfg = effectiveConfig(cfg, before, s, rec.Event)
		if e, ok := rec.Event.(queue.ConfigApplied); ok && before.ConfigDigest != s.ConfigDigest && w.From.reached(rec) {
			// The starting ConfigApplied is part of the period. A later changed
			// configuration ends ownership, even if pushed before the drain finished.
			isStart := rec.ID == w.From.EventID || w.From.EventID == 0 && rec.RecordedAt.Equal(w.From.Time)
			if !isStart && r.NextConfig == nil {
				r.NextConfig = &Boundary{Time: rec.RecordedAt, EventID: rec.ID, Commit: e.Commit}
				periodEnded = true
				if inside {
					r.Warnings = append(r.Warnings, "configuration changed inside admission window")
				}
			}
		}
		if inPeriod && !periodEnded {
			id := ""
			switch e := rec.Event.(type) {
			case queue.BatchStarted:
				id = e.BatchID
			case queue.ChainedBatchStarted:
				id = e.BatchID
			}
			if id != "" && before.Batch(id) == nil && s.Batch(id) != nil {
				r.Batches = append(r.Batches, id)
			}
		}
		if e, ok := rec.Event.(queue.EnqueueRequested); ok {
			a, b := entry(before, e.PR), entry(s, e.PR)
			if b != nil && (a == nil || a.AdmissionID != b.AdmissionID) {
				ad := Admission{EventID: rec.ID, PR: e.PR, AdmissionID: b.AdmissionID, AcceptedAt: rec.RecordedAt, EnqueuedAt: b.EnqueuedAt, QueueLength: len(s.Entries)}
				admissions[b.AdmissionID] = ad
				if _, ok := first[e.PR]; !ok {
					first[e.PR] = e.At
				}
				if inside {
					cohort[b.AdmissionID], pending[b.AdmissionID] = true, true
					r.Admissions = append(r.Admissions, ad)
					r.ArrivalsByDay[rec.RecordedAt.UTC().Format("2006-01-02")]++
				} else if inPeriod && !periodEnded {
					r.Warnings = append(r.Warnings, fmt.Sprintf("admission %d accepted after window close; operator must stop queueing during drain", b.AdmissionID))
					r.QueueDrainedAt = time.Time{}
				}
			}
		}
		landed := map[uint64]bool{}
		if e, ok := rec.Event.(queue.Landed); ok {
			b := before.Batch(e.BatchID)
			accepted := b != nil && b.Phase == queue.Landing && s.Batch(e.BatchID) == nil
			if !accepted && inPeriod {
				return r, fmt.Errorf("landed event %d was not accepted by replay; run audit", rec.ID)
			}
			if accepted {
				for _, pr := range b.PRs {
					en := entry(before, pr.PR)
					if en == nil {
						return r, fmt.Errorf("landing %d has no admission for #%d", rec.ID, pr.PR)
					}
					landed[en.AdmissionID] = true
					if !cohort[en.AdmissionID] {
						continue
					}
					ad := admissions[en.AdmissionID]
					c := Change{PR: pr.PR, AdmissionID: en.AdmissionID, AdmissionEventID: ad.EventID, LandedEventID: rec.ID, BatchID: b.ID, EnqueuedAt: ad.EnqueuedAt, FirstEnqueuedAt: first[pr.PR], LandedAt: rec.RecordedAt, Position: max(uint64(1), b.ChainPosition), Tested: b.TestedSHA != "" && before.ChecksPassed(b.TestedSHA)}
					c.Seconds, c.FirstSeconds = c.LandedAt.Sub(c.EnqueuedAt).Seconds(), c.LandedAt.Sub(c.FirstEnqueuedAt).Seconds()
					if c.Seconds < 0 || c.FirstSeconds < 0 {
						return r, fmt.Errorf("landing %d precedes enqueue timestamp", rec.ID)
					}
					r.Changes = append(r.Changes, c)
					if inside {
						r.LandedAtClose++
					}
					if c.Tested && c.Position >= 2 {
						r.Engaged++
						if inside {
							r.EngagedAtClose++
						}
					}
					if c.Tested && c.Position >= 3 {
						r.DeepEngaged++
						if inside {
							r.DeepEngagedAtClose++
						}
					}
				}
			}
		}
		for _, en := range before.Entries {
			if !pending[en.AdmissionID] {
				continue
			}
			after := entry(s, en.PR)
			if after == nil || after.AdmissionID != en.AdmissionID {
				delete(pending, en.AdmissionID)
				if !landed[en.AdmissionID] {
					r.Dequeues = append(r.Dequeues, Dequeue{AdmissionID: en.AdmissionID, PR: en.PR, EventID: rec.ID, At: rec.RecordedAt})
				}
			}
		}
		if inPeriod && !periodEnded && w.To.reached(rec) && idle() && r.QueueDrainedAt.IsZero() {
			r.QueueDrainedAt = maxTime(w.To.Time, lastTime)
		}
		if inside {
			p := point(s, cfg, rec.ID, lastTime)
			if !samePoint(r.WindowHistory[len(r.WindowHistory)-1], p) {
				r.WindowHistory = append(r.WindowHistory, p)
			}
			f, old := s.LastWindowFailure(), before.LastWindowFailure()
			if f != nil && (old == nil || f.Revision != old.Revision) {
				r.Halvings = append(r.Halvings, Halving{EventID: rec.ID, At: rec.RecordedAt, Before: before.Window(cfg), After: s.Window(cfg), Failure: *f})
			}
		}
	}
	if !initialized {
		r.WindowHistory = append(r.WindowHistory, point(s, cfg, 0, w.From.Time))
	}
	accumulate(&r, s, lastTime, w.To.Time)
	if !periodEnded && idle() && r.QueueDrainedAt.IsZero() {
		r.QueueDrainedAt = maxTime(w.To.Time, lastTime)
	}
	r.Landed, r.Dequeued, r.Pending = len(r.Changes), len(r.Dequeues), len(pending)
	var times, firstTimes []float64
	for _, c := range r.Changes {
		times = append(times, c.Seconds)
		firstTimes = append(firstTimes, c.FirstSeconds)
	}
	r.TimeToMerge, r.FirstEnqueue = percentiles(times), percentiles(firstTimes)
	r.ArrivalsPerDay = float64(len(r.Admissions)) / w.To.Time.Sub(w.From.Time).Hours() * 24
	if r.NonIdleSeconds > 0 {
		r.MultipleBatchShare = r.MultipleBatchSeconds / r.NonIdleSeconds
	}
	return r, nil
}

// First eligible observation is a conservative arrival proxy: PRs observed
// earlier or already admitted do not count. Unadmitted PRs remain visible.
func heldArrivals(j store.Journal, closedAt, end, asOf time.Time) []HeldArrival {
	s := j.Seed
	first := map[int]time.Time{}
	admitted := map[int]time.Time{}
	for _, rec := range j.Events {
		if rec.RecordedAt.After(asOf) {
			continue
		}
		if e, ok := rec.Event.(queue.PRObserved); ok && e.Eligible {
			if _, seen := first[e.PR]; !seen {
				first[e.PR] = rec.RecordedAt
			}
		}
		before := s
		s = queue.Apply(s, rec.Event)
		if e, ok := rec.Event.(queue.EnqueueRequested); ok {
			a, b := entry(before, e.PR), entry(s, e.PR)
			if b != nil && (a == nil || a.AdmissionID != b.AdmissionID) {
				if _, seen := admitted[e.PR]; !seen {
					admitted[e.PR] = rec.RecordedAt
				}
			}
		}
	}
	var result []HeldArrival
	for pr, at := range first {
		ad := admitted[pr]
		if !at.Before(closedAt) && at.Before(end) && (ad.IsZero() || !ad.Before(end)) {
			result = append(result, HeldArrival{PR: pr, EligibleAt: at, AdmittedAt: ad})
		}
	}
	slices.SortFunc(result, func(a, b HeldArrival) int { return a.PR - b.PR })
	return result
}

func accumulate(r *Report, s queue.State, from, to time.Time) {
	from = maxTime(from, r.Window.From.Time)
	to = minTime(to, r.Window.To.Time)
	if !to.After(from) {
		return
	}
	seconds := to.Sub(from).Seconds()
	if len(s.Entries) > 0 || len(s.Batches()) > 0 {
		r.NonIdleSeconds += seconds
	}
	if len(s.Batches()) > 1 {
		r.MultipleBatchSeconds += seconds
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func percentiles(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}
	values = slices.Clone(values)
	slices.Sort(values)
	return Percentiles{Median: values[int(math.Ceil(float64(len(values))*.5))-1], P95: values[int(math.Ceil(float64(len(values))*.95))-1]}
}
