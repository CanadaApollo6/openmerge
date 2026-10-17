package main

import (
	"fmt"
	"maps"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

// summarizeEvidence derives comparison inputs from the retained evidence. It is
// also used to reject edited or stale scalar summaries in an offline report.
func summarizeEvidence(r Report) Report {
	r.Landed = len(r.Changes)
	r.LandedAtClose, r.Engaged, r.DeepEngaged, r.EngagedAtClose, r.DeepEngagedAtClose = 0, 0, 0, 0, 0
	var times, first []float64
	for _, c := range r.Changes {
		times = append(times, c.Seconds)
		first = append(first, c.FirstSeconds)
		inside := r.Window.contains(store.JournalEvent{ID: c.LandedEventID, RecordedAt: c.LandedAt})
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
	r.TimeToMerge, r.FirstEnqueue = percentiles(times), percentiles(first)
	r.ArrivalsPerDay = float64(len(r.Admissions)) / r.Window.To.Time.Sub(r.Window.From.Time).Hours() * 24
	r.ArrivalsByDay = map[string]int{}
	for _, a := range r.Admissions {
		r.ArrivalsByDay[a.AcceptedAt.UTC().Format("2006-01-02")]++
	}
	r.RunAttempts = len(r.Runs)
	r.RunsPerMerge = 0
	if r.Landed > 0 {
		r.RunsPerMerge = float64(r.RunAttempts) / float64(r.Landed)
	}
	return r
}

func validateEvidence(r Report) error {
	want := summarizeEvidence(r)
	if r.Landed != want.Landed || r.LandedAtClose != want.LandedAtClose || r.Engaged != want.Engaged || r.DeepEngaged != want.DeepEngaged || r.EngagedAtClose != want.EngagedAtClose || r.DeepEngagedAtClose != want.DeepEngagedAtClose || r.TimeToMerge != want.TimeToMerge || r.FirstEnqueue != want.FirstEnqueue || r.ArrivalsPerDay != want.ArrivalsPerDay || !maps.Equal(r.ArrivalsByDay, want.ArrivalsByDay) || r.RunAttempts != want.RunAttempts || r.RunsPerMerge != want.RunsPerMerge {
		return fmt.Errorf("summary fields do not match per-change, per-admission and per-run evidence")
	}
	checked := map[int64]bool{}
	if r.Audit != nil {
		for _, id := range r.Audit.CheckedEventIDs {
			checked[id] = true
		}
	}
	changes := map[uint64]bool{}
	for _, c := range r.Changes {
		if c.AdmissionID == 0 || changes[c.AdmissionID] || c.LandedEventID <= 0 || c.LandedAt.After(r.ReportAt) || c.Seconds < 0 || c.FirstSeconds < 0 || c.Seconds != c.LandedAt.Sub(c.EnqueuedAt).Seconds() || c.FirstSeconds != c.LandedAt.Sub(c.FirstEnqueuedAt).Seconds() {
			return fmt.Errorf("invalid per-change evidence for admission %d", c.AdmissionID)
		}
		changes[c.AdmissionID] = true
		if !checked[c.LandedEventID] {
			return fmt.Errorf("audit is missing cohort landed event %d", c.LandedEventID)
		}
	}
	admissions := map[uint64]bool{}
	for _, a := range r.Admissions {
		if a.AdmissionID == 0 || admissions[a.AdmissionID] || !r.Window.contains(store.JournalEvent{ID: a.EventID, RecordedAt: a.AcceptedAt}) {
			return fmt.Errorf("invalid admission evidence %d", a.AdmissionID)
		}
		admissions[a.AdmissionID] = true
	}
	for _, c := range r.Changes {
		if !admissions[c.AdmissionID] {
			return fmt.Errorf("change has no cohort admission %d", c.AdmissionID)
		}
	}
	batches := map[string]bool{}
	for _, id := range r.Batches {
		batches["openmerge/queue/"+r.Queue.BaseBranch+"/"+id] = true
	}
	attempts := map[[2]int64]bool{}
	for _, run := range r.Runs {
		key := [2]int64{run.ID, int64(run.Number)}
		if run.ID <= 0 || run.Number < 1 || attempts[key] || !batches[run.Branch] || run.At.IsZero() || run.At.After(r.ReportAt) || run.Status != "completed" || run.CompletedAt.Before(run.At) || run.CompletedAt.After(r.DrainEnd) {
			return fmt.Errorf("invalid or undrained run evidence %d/%d", run.ID, run.Number)
		}
		attempts[key] = true
	}
	return nil
}
