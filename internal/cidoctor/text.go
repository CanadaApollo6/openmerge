package cidoctor

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// WriteText leads with findings and summarizes non-triggering comparisons.
// JSON retains every observation; text retains triggered evidence and step classes.
func WriteText(out io.Writer, r Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "OpenMerge CI doctor v%d (advisory, read-only)\nQueue: %s %s (installation %s; lane %q)\nWindow: %s -> %s (exclusive)\nQueue workflow runs: %d; gaps: %d\n", r.Version, r.Repository, r.BaseBranch, r.Installation, r.Lane, r.Since.Format("2006-01-02T15:04:05Z07:00"), r.Until.Format("2006-01-02T15:04:05Z07:00"), r.QueueRuns, r.GapCount)
	fmt.Fprintln(&b, "Minutes at stake are measured comparisons, never projected savings. Findings overlap; do not sum them.")
	fmt.Fprintln(&b, "\nFindings:")
	if len(r.Findings) == 0 {
		fmt.Fprintln(&b, "  None. Inspect sample counts and gaps before interpreting this as an improvement.")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  %s | %s | %d runs | %.2f measured minutes at stake\n    runs: %v; triggered runs: %v; baseline/comparison runs: %v\n    %s\n", f.Code, f.Subject, f.Evidence.QualifyingRuns, f.MinutesAtStake, f.Evidence.RunIDs, f.Evidence.TriggeredIDs, f.Evidence.BaselineIDs, f.Comparison)
		for _, d := range f.Evidence.Details {
			fmt.Fprintf(&b, "    %s\n", d)
		}
		if f.SuggestedFix != "" {
			fmt.Fprintf(&b, "    Suggested fix: %s\n", f.SuggestedFix)
		}
	}
	keys := []string{}
	for k := range r.Thresholds {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fmt.Fprintln(&b, "\nThresholds:")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s = %g\n", k, r.Thresholds[k])
	}
	fmt.Fprintln(&b, "\nObservations (medians; below 10 qualifying runs produces no finding):")
	nonTriggering := map[string][]Assessment{}
	for _, s := range r.Assessments {
		if s.TriggeredRuns == 0 {
			nonTriggering[s.Code] = append(nonTriggering[s.Code], s)
			continue
		}
		fmt.Fprintf(&b, "  %s | %s | qualifying %d; triggered %d; runs %v; comparison %v\n", s.Code, s.Subject, s.QualifyingRuns, s.TriggeredRuns, s.RunIDs, s.BaselineIDs)
		keys = keys[:0]
		for k := range s.Metrics {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    %s = %.3f\n", k, s.Metrics[k])
		}
	}
	codes := []string{}
	for code := range nonTriggering {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	for _, code := range codes {
		items := nonTriggering[code]
		low, high := items[0].QualifyingRuns, 0
		for _, s := range items {
			low = min(low, s.QualifyingRuns)
			high = max(high, s.QualifyingRuns)
		}
		fmt.Fprintf(&b, "  %s | %d non-triggering comparisons; qualifying %d..%d per comparison; triggered 0\n", code, len(items), low, high)
		slices.SortFunc(items, func(x, y Assessment) int {
			if d := thresholdProximity(y) - thresholdProximity(x); d != 0 {
				if d > 0 {
					return 1
				}
				return -1
			}
			return strings.Compare(x.Subject, y.Subject)
		})
		for _, s := range items[:min(3, len(items))] {
			if thresholdProximity(s) > 0 {
				fmt.Fprintf(&b, "    closest | %s | qualifying %d; triggered 0; threshold proximity %.3f\n", s.Subject, s.QualifyingRuns, thresholdProximity(s))
			}
		}
	}
	fmt.Fprintln(&b, "\nJob time split and heuristic step classification (seconds):")
	for _, j := range r.Jobs {
		dispatch := "unavailable"
		if j.DispatchSeconds != nil {
			dispatch = fmt.Sprintf("%.1f", *j.DispatchSeconds)
		}
		fmt.Fprintf(&b, "  run %d attempt %d | %s | dispatch %s; wait %.1f; setup %.1f; run %.1f\n", j.RunID, j.Attempt, j.Job, dispatch, j.WaitSeconds, j.SetupSeconds, j.RunSeconds)
		for _, s := range j.Steps {
			fmt.Fprintf(&b, "    %s | %s/%s | %.1f\n", s.Name, s.Class, s.Phase, s.Seconds)
		}
	}
	fmt.Fprintln(&b, "\nGaps (no extrapolation):")
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "  run %d | %s | %s\n", g.RunID, g.Code, g.Detail)
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// Proximity is the smallest normalized predicate for rules that require all
// predicates, or the largest for alternatives. It ranks comparisons, not risk.
func thresholdProximity(s Assessment) float64 {
	m := s.Metrics
	switch s.Code {
	case "serial-step":
		return min(m["median_job_seconds"]/LongJobSeconds, m["median_step_fraction"]/SerialStepFraction)
	case "cold-setup":
		return m["median_setup_ratio"] / ColdSetupRatio
	case "long-pole":
		return min(m["trigger_fraction"]/LongPoleFraction, m["median_sibling_idle_seconds"]/max(1, SiblingIdleFraction*m["median_job_seconds"]))
	case "checkout-cost":
		return max(m["median_largest_checkout_seconds"]/CheckoutSeconds, m["median_checkout_seconds"]/max(1, CheckoutFraction*m["median_runner_seconds"]))
	case "retry-habit":
		return m["trigger_fraction"] / RetryFraction
	}
	return 0
}
