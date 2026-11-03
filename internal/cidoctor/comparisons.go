package cidoctor

import (
	"fmt"
	"slices"
	"time"
)

func (a *analyzer) maskedSkip(r Run) {
	g := r.Graph
	if g == nil || g.HeadSHA != r.HeadSHA || g.Source == "" || g.Required == nil {
		a.gap(r.ID, "dependency-graph", "missing graph and required-check policy at the run head SHA")
		return
	}
	jobs := map[string]Job{}
	for _, j := range r.Jobs {
		jobs[j.Name] = j
	}

	visiting, done := map[string]bool{}, map[string]bool{}
	var validNode func(string) bool
	validNode = func(name string) bool {
		if done[name] {
			return true
		}
		if visiting[name] {
			return false
		}
		edges, known := g.Needs[name]
		_, exists := jobs[name]
		if !known || !exists {
			return false
		}
		visiting[name] = true
		for _, n := range edges {
			if !validNode(n) {
				return false
			}
		}
		visiting[name] = false
		done[name] = true
		return true
	}
	for _, name := range g.Required {
		if !validNode(name) {
			a.gap(r.ID, "dependency-graph", "missing job or cyclic dependency in required graph")
			return
		}
	}

	for _, name := range g.Required {
		job := jobs[name]
		needs := g.Needs[name]
		if len(needs) == 0 {
			continue
		}
		hit := false
		details := []string{}
		conclusions := append([]string{name}, needs...)
		valid := true
		for _, n := range conclusions {
			j := jobs[n]
			if j.CompletedAt.IsZero() && j.Conclusion == "skipped" {
				continue
			}
			if j.CompletedAt.IsZero() || j.CompletedAt.Before(a.report.Since) || j.CompletedAt.Before(r.CreatedAt) || !j.CompletedAt.Before(a.report.Until) {
				a.gap(r.ID, "masked-skip-data", n+": conclusion completion missing, inconsistent, or outside closed window")
				valid = false
			}
		}
		if !valid {
			continue
		}
		for _, n := range needs {
			dep := jobs[n]
			details = append(details, n+"="+dep.Conclusion)
			if dep.Conclusion == "skipped" || dep.Conclusion == "cancelled" {
				hit = true
			}
		}
		a.add("masked-skip", subject(r, job), observation{id: r.ID, detail: fmt.Sprintf("run %d attempt %d: %s=%s needs %v", r.ID, r.Attempt, name, job.Conclusion, details), hit: hit && job.Conclusion == "success", values: map[string]float64{"dependencies": float64(len(needs))}})
	}
}

func (a *analyzer) runComparisons(r Run, ts []JobTiming, actions Actions, counts map[string]int) {
	end, start := r.Jobs[0].CompletedAt, r.CreatedAt
	runner, checkout, largest := 0.0, 0.0, 0.0
	allCheckout := true
	for i, j := range r.Jobs {
		if j.CompletedAt.After(end) {
			end = j.CompletedAt
		}
		runner += j.CompletedAt.Sub(j.StartedAt).Seconds()
		hasCheckout := false
		for _, s := range ts[i].Steps {
			if s.Class == "checkout" {
				hasCheckout = true
				checkout += s.Seconds
				largest = max(largest, s.Seconds)
			}
		}
		allCheckout = allCheckout && hasCheckout
	}
	a.longPole(r)
	if checkout > 0 {
		hit := allCheckout && checkout > CheckoutFraction*runner || largest > CheckoutSeconds
		a.add("checkout-cost", fmt.Sprintf("%d %s", r.WorkflowID, r.Path), observation{id: r.ID, hit: hit, excess: max(0, choose(allCheckout, checkout-CheckoutFraction*runner, 0), largest-CheckoutSeconds) / 60, values: map[string]float64{"checkout_seconds": checkout, "runner_seconds": runner, "largest_checkout_seconds": largest}})
	}
	foundPR := false
	for _, pr := range actions.Runs {
		if pr.Event != "pull_request" || pr.HeadSHA != r.HeadSHA || pr.WorkflowID != r.WorkflowID {
			continue
		}
		foundPR = true
		if counts[attemptKey(pr.ID, pr.Attempt, "")] != 1 || validateRun(pr, a.report.Until) != "" {
			a.gap(r.ID, "pr-data", "incomplete or ambiguous same-SHA PR run")
			continue
		}
		overlap, valid := 0.0, true
		for _, j := range pr.Jobs {
			if _, err := timing(pr, j, nil, a.report.Until); err != nil {
				a.gap(r.ID, "pr-data", j.Name+": "+err.Error())
				valid = false
				break
			}
			overlap += overlapSeconds(start, end, j.StartedAt, j.CompletedAt)
		}
		if valid {
			a.add("duplicate-pr-run", fmt.Sprintf("%d %s", r.WorkflowID, r.Path), observation{id: r.ID, baseline: []uint64{pr.ID}, hit: overlap > 0, excess: overlap / 60, values: map[string]float64{"overlap_seconds": overlap}})
		}
	}
	if !foundPR {
		a.gap(r.ID, "pr-data", "no recorded same-SHA pull request comparison; absence is not evidence of no duplicate")
	}
}

// Longest paths use job execution time as the node weight. Dependency levels
// keep downstream gates out of the parallel-sibling idle comparison.
func (a *analyzer) longPole(r Run) {
	g := r.Graph
	if g == nil || g.HeadSHA != r.HeadSHA || g.Source == "" {
		a.gap(r.ID, "dependency-graph", "missing graph at the run head SHA for critical path")
		return
	}
	jobs := map[string]Job{}
	for _, j := range r.Jobs {
		jobs[j.Name] = j
	}
	if len(g.Needs) != len(jobs) {
		a.gap(r.ID, "dependency-graph", "incomplete job graph for critical path")
		return
	}
	lengths := map[string]time.Duration{}
	levels := map[string]int{}
	visiting := map[string]bool{}
	var visit func(string) bool
	visit = func(name string) bool {
		if _, done := lengths[name]; done {
			return true
		}
		j, exists := jobs[name]
		needs, known := g.Needs[name]
		if !exists || !known || visiting[name] {
			return false
		}
		visiting[name] = true
		prefix := time.Duration(0)
		for _, n := range needs {
			if !visit(n) || jobs[n].CompletedAt.After(j.StartedAt) {
				return false
			}
			prefix = max(prefix, lengths[n])
			levels[name] = max(levels[name], levels[n]+1)
		}
		lengths[name] = prefix + j.CompletedAt.Sub(j.StartedAt)
		visiting[name] = false
		return true
	}
	longest := time.Duration(0)
	for _, j := range r.Jobs {
		if !visit(j.Name) {
			a.gap(r.ID, "dependency-graph", "missing job, cyclic dependency, or inconsistent dependency timing for critical path")
			return
		}
		longest = max(longest, lengths[j.Name])
	}
	onPath := map[string]bool{}
	dominant := time.Duration(0)
	var trace func(string)
	trace = func(name string) {
		if onPath[name] {
			return
		}
		onPath[name] = true
		j := jobs[name]
		duration := j.CompletedAt.Sub(j.StartedAt)
		dominant = max(dominant, duration)
		for _, n := range g.Needs[name] {
			if lengths[n]+duration == lengths[name] {
				trace(n)
			}
		}
	}
	for _, j := range r.Jobs {
		if lengths[j.Name] == longest {
			trace(j.Name)
		}
	}
	for _, j := range r.Jobs {
		idle := []float64{}
		for _, sibling := range r.Jobs {
			if j.ID != sibling.ID && levels[j.Name] == levels[sibling.Name] {
				idle = append(idle, max(0, j.CompletedAt.Sub(sibling.CompletedAt).Seconds()))
			}
		}
		if len(idle) == 0 {
			continue
		}
		duration := j.CompletedAt.Sub(j.StartedAt)
		hit := onPath[j.Name] && duration == dominant
		a.add("long-pole", subject(r, j), observation{id: r.ID, hit: hit, excess: choose(hit, median(idle)/60, 0), values: map[string]float64{"sibling_idle_seconds": median(idle), "job_seconds": duration.Seconds()}})
	}
}

func overlapSeconds(a, b, c, d time.Time) float64 {
	starts := []time.Time{a, c}
	ends := []time.Time{b, d}
	slices.SortFunc(starts, time.Time.Compare)
	slices.SortFunc(ends, time.Time.Compare)
	return max(0, ends[0].Sub(starts[1]).Seconds())
}
