package main

import (
	"fmt"
	"time"
)

type Comparison struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

func compare(a, b Report) Comparison {
	c := Comparison{Status: "inconclusive"}
	add := func(reason string) { c.Reasons = append(c.Reasons, reason) }
	for i, r := range []Report{a, b} {
		label := []string{"A", "B"}[i]
		if r.Version != 1 {
			add(label + ": unsupported report version")
		}
		if err := r.Window.validate(); err != nil {
			add(label + ": " + err.Error())
		}
		if r.Landed < 50 {
			add(fmt.Sprintf("%s has %d landed changes; needs 50", label, r.Landed))
		}
		if r.Landed != len(r.Changes) {
			add(label + ": landed count does not match per-change evidence")
		}
		if r.Audit == nil {
			add(label + ": independent landing audit is missing")
		} else if r.Audit.Version != 1 || r.Audit.Queue != r.Queue || r.Audit.Since.After(r.Window.From.Time) || r.Audit.Until.Before(r.Window.To.Time) {
			add(label + ": independent audit does not cover the period")
		}
		if !r.LogReviewed {
			add(label + ": operator has not confirmed the change/incident log contains no material change or abort")
		}
	}
	// Incorrect landings trigger the abort condition even before sample gates pass.
	for i, r := range []Report{a, b} {
		if r.Audit != nil && len(r.Audit.Violations) > 0 {
			c.Status = "fail"
			add(fmt.Sprintf("%s has %d audit violations: abort and restart after fixing the cause", []string{"A", "B"}[i], len(r.Audit.Violations)))
		}
	}
	if a.Queue != b.Queue {
		add("periods refer to different queues")
	}
	if !a.Window.To.Time.Equal(b.Window.From.Time) || a.Window.To.EventID != b.Window.From.EventID {
		add("periods are not adjacent at the same boundary")
	}
	duration := a.Window.To.Time.Sub(a.Window.From.Time)
	if duration < 14*24*time.Hour {
		add("A has not run for 14 days")
	}
	bDuration := b.Window.To.Time.Sub(b.Window.From.Time)
	if duration != bDuration {
		// The decision rule permits extending B when its initial equal-length
		// period was not comparable on traffic. Keep every day in the result.
		initialArrivals := 0
		for _, admission := range b.Admissions {
			if !admission.AcceptedAt.Before(b.Window.From.Time) && admission.AcceptedAt.Before(b.Window.From.Time.Add(duration)) {
				initialArrivals++
			}
		}
		initialRate := float64(initialArrivals) / duration.Hours() * 24
		if bDuration < duration || len(b.Admissions) == 0 || !trafficMismatch(a.ArrivalsPerDay, initialRate) {
			add("periods do not have equal duration, and admissions do not justify extending B for traffic")
		}
	}
	if a.Engaged < 10 {
		if duration >= 42*24*time.Hour {
			add("A reached six weeks without 10 landings at position >=2; default stays unchanged")
		} else {
			add("A needs 10 landings tested at position >=2")
		}
	}
	if duration >= 42*24*time.Hour && a.Engaged >= 10 {
		byDeadline := 0
		for _, change := range a.Changes {
			if change.Position >= 2 && !change.LandedAt.After(a.Window.From.Time.Add(42*24*time.Hour)) {
				byDeadline++
			}
		}
		if byDeadline < 10 {
			add("A did not meet the engagement floor by six weeks; later landings cannot rescue this comparison")
		}
	}
	if !depthThroughout(a, 4) || !depthThroughout(b, 1) {
		add("periods must use max_depth 4 throughout A and 1 throughout B")
	}
	if a.CIConfig == "" || a.CIConfig != b.CIConfig {
		add("identical CI configuration has not been confirmed")
	}
	if trafficMismatch(a.ArrivalsPerDay, b.ArrivalsPerDay) {
		add("arrivals per day differ by more than a factor of two (or are zero); not comparable: extend B or rerun both periods")
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
