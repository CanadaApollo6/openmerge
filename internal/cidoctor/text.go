package cidoctor

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// WriteText renders the same evidence as JSON, including observations below
// the minimum and the classifier's unknown steps. No totals imply savings.
func WriteText(out io.Writer, r Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "OpenMerge CI doctor v%d (advisory, read-only)\nQueue: %s %s (installation %s; lane %q)\nWindow: %s -> %s (exclusive)\nQueue workflow runs: %d; gaps: %d\n", r.Version, r.Repository, r.BaseBranch, r.Installation, r.Lane, r.Since.Format("2006-01-02T15:04:05Z07:00"), r.Until.Format("2006-01-02T15:04:05Z07:00"), r.QueueRuns, r.GapCount)
	fmt.Fprintln(&b, "Minutes at stake are measured comparisons, never projected savings. Findings overlap; do not sum them.")
	keys := []string{}
	for k := range r.Thresholds {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fmt.Fprintln(&b, "\nThresholds:")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s = %g\n", k, r.Thresholds[k])
	}
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
	fmt.Fprintln(&b, "\nObservations (medians; below 10 qualifying runs produces no finding):")
	for _, s := range r.Assessments {
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
