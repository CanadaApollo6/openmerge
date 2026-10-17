package main

import (
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

type LatencySensitivity struct {
	With           Percentiles `json:"with_holdovers"`
	Without        Percentiles `json:"without_holdovers"`
	WithCount      int         `json:"with_count"`
	WithoutCount   int         `json:"without_count"`
	HoldoverLanded int         `json:"holdover_landed"`
}
type Comparison struct {
	Status   string             `json:"status"`
	Reasons  []string           `json:"reasons"`
	BLatency LatencySensitivity `json:"b_latency_sensitivity"`
}

func holdoverSensitivity(a, b Report) LatencySensitivity {
	held := map[int]bool{}
	for _, h := range a.HeldArrivals {
		held[h.PR] = true
	}
	var with, without []float64
	for _, c := range b.Changes {
		with = append(with, c.Seconds)
		if !held[c.PR] {
			without = append(without, c.Seconds)
		}
	}
	return LatencySensitivity{With: percentiles(with), Without: percentiles(without), WithCount: len(with), WithoutCount: len(without), HoldoverLanded: len(with) - len(without)}
}

func engagementBy(r Report, deadline time.Time) (int, int) {
	two, three := 0, 0
	for _, c := range r.Changes {
		if !r.Window.To.reached(store.JournalEvent{ID: c.LandedEventID, RecordedAt: c.LandedAt}) && !c.LandedAt.After(deadline) && c.Tested {
			if c.Position >= 2 {
				two++
			}
			if c.Position >= 3 {
				three++
			}
		}
	}
	return two, three
}

func compare(a, b Report) Comparison {
	c := Comparison{Status: "inconclusive", BLatency: holdoverSensitivity(a, b)}
	add := func(reason string) { c.Reasons = append(c.Reasons, reason) }
	for i, r := range []Report{a, b} {
		label := []string{"A", "B"}[i]
		if r.AbortedReason != "" {
			c.Status = "aborted"
			add(label + ": " + r.AbortedReason)
		}
		if r.Audit != nil && len(r.Audit.Violations) > 0 {
			c.Status = "aborted"
			add(fmt.Sprintf("%s has %d audit violations: abort and restart after fixing the cause", label, len(r.Audit.Violations)))
		}
	}
	if c.Status == "aborted" {
		return c
	}
	for i, r := range []Report{a, b} {
		label := []string{"A", "B"}[i]
		if r.Version != 2 {
			add(label + ": unsupported report version; regenerate under approved plan")
		}
		if err := r.Window.validate(); err != nil {
			add(label + ": " + err.Error())
		}
		if err := validateEvidence(r); err != nil {
			add(label + ": " + err.Error())
		}
		if r.ReportAt.Before(a.DrainEnd) || r.ReportAt.Before(b.DrainEnd) {
			add(label + ": regenerate both reports after both drain ends")
		}
		if !r.DrainComplete || r.DrainEnd.Before(r.Window.To.Time) || r.ReportAt.Before(r.DrainEnd) {
			add(label + ": drain incomplete or not verified at report time")
		}
		for _, warning := range r.Warnings {
			add(label + ": " + warning)
		}
		if n := summarizeEvidence(r).LandedAtClose; n < 50 {
			add(fmt.Sprintf("%s has %d landed changes at window close; needs 50", label, n))
		}
		if r.Landed != len(r.Changes) {
			add(label + ": landed count does not match per-change evidence")
		}
		if r.Audit == nil {
			add(label + ": independent landing audit is missing")
		} else if r.Audit.Version != 2 || r.Audit.Queue != r.Queue || r.Audit.Since.After(r.Window.From.Time) || !r.Audit.Until.After(r.DrainEnd) {
			add(label + ": independent audit does not cover the complete drain")
		}
		if !r.LogReviewed {
			add(label + ": operator has not confirmed the change/incident log contains no material change or abort")
		}
	}
	a, b = summarizeEvidence(a), summarizeEvidence(b)
	if a.Queue != b.Queue {
		add("periods refer to different queues")
	}
	if a.NextConfig == nil || !a.NextConfig.Time.Equal(b.Window.From.Time) || a.NextConfig.EventID != b.Window.From.EventID || b.Window.From.Time.Before(a.DrainEnd) {
		add("periods are not adjacent at a drained configuration boundary")
	}
	duration := a.Window.To.Time.Sub(a.Window.From.Time)
	if duration < 14*24*time.Hour {
		add("A has not run for 14 days")
	}
	if duration != b.Window.To.Time.Sub(b.Window.From.Time) {
		add("admission windows must have equal duration; B cannot be extended")
	}
	deadline := a.Window.From.Time.Add(42 * 24 * time.Hour)
	two, three := engagementBy(a, deadline)
	if two < 10 || three < 3 {
		if duration >= 42*24*time.Hour {
			add("A reached six weeks without both engagement thresholds; later landings cannot rescue this comparison")
		} else {
			add("A needs 10 changes landed from tested position >=2 and 3 from position >=3 before window close")
		}
	}
	if !depthThroughout(a, 4) || !depthThroughout(b, 1) {
		add("periods must use max_depth 4 throughout A and 1 throughout B")
	}
	if a.CIConfig == "" || a.CIConfig != b.CIConfig {
		add("identical CI configuration has not been confirmed")
	}
	if trafficMismatch(a.ArrivalsPerDay, b.ArrivalsPerDay) {
		add("arrivals per day differ by more than a factor of two (or are zero); not comparable; pair is inconclusive")
	}
	if len(c.Reasons) > 0 {
		return c
	}
	c.Status = "pass"
	if a.TimeToMerge.Median > b.TimeToMerge.Median {
		add("A median time to merge is worse than B")
	}
	if a.TimeToMerge.P95 > b.TimeToMerge.P95 {
		add("A p95 time to merge is worse than B")
	}
	if a.RunsPerMerge > b.RunsPerMerge {
		add("A runs per merged change is worse than B")
	}
	if len(c.Reasons) > 0 {
		c.Status = "fail"
	} else {
		add("all pre-registered gates pass; median, p95 and runs/merge are each no worse, with zero audited incorrect landings")
	}
	return c
}

func trafficMismatch(a, b float64) bool {
	return a <= 0 || b <= 0 || a > 2*b || b > 2*a
}
func depthThroughout(r Report, depth int) bool {
	if len(r.WindowHistory) == 0 {
		return false
	}
	found := false
	for i, p := range r.WindowHistory {
		end := r.Window.To.Time
		if i+1 < len(r.WindowHistory) {
			end = r.WindowHistory[i+1].At
		}
		if end.After(p.At) {
			found = true
			if p.MaxDepth != depth {
				return false
			}
		}
	}
	return found
}
