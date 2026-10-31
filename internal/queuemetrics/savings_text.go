package queuemetrics

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// WriteSavingsText renders the human report. Every number has the same source
// as the JSON fields; the definitions are always printed.
func WriteSavingsText(out io.Writer, s Savings) error {
	var head strings.Builder
	queue := s.Repository + " " + s.BaseBranch
	if s.Lane != "" {
		queue += " lane " + s.Lane
	}
	fmt.Fprintf(&head, "OpenMerge CI savings report\nQueue:  %s (installation %s)\nWindow: %s -> %s (exclusive)\n\n", queue, s.Installation, s.Since.Format(time.RFC3339), s.Until.Format(time.RFC3339))
	if _, err := io.WriteString(out, head.String()); err != nil {
		return err
	}
	var table bytes.Buffer
	t := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	row := func(label, value, detail string) { fmt.Fprintf(t, "%s\t%s\t%s\n", label, value, detail) }
	b, r, m := s.Batches, s.Runs, s.Minutes
	row("PRs landed", fmt.Sprint(s.PRsLanded), "")
	row("Batches landed", fmt.Sprint(b.Landed), fmt.Sprintf("mean %.2f PRs; p50 %d; p90 %d; max %d", b.Mean, b.P50, b.P90, b.Max))
	row("CI runs executed", fmt.Sprint(r.Executed), fmt.Sprintf("%d test refs + %d rerun(s); %d carried in from before --since", r.TestRefsBuilt, r.Reruns, r.CarriedIn))
	row("  test ref outcomes", "", fmt.Sprintf("landed %d; invalidated %d; discarded %d; open %d", r.Outcomes.Landed, r.Outcomes.Invalidated, r.Outcomes.Discarded, r.Outcomes.Open))
	row("Baseline CI runs", fmt.Sprint(r.Baseline), fmt.Sprintf("%d landed PRs + %d rerun(s)", s.PRsLanded, r.Reruns))
	row("CI runs saved", fmt.Sprint(r.Saved), fmt.Sprintf("%.1f%% of baseline; %.2f runs per landed PR", 100*r.SavedFraction, r.RunsPerLanded))
	if m.Source == MinutesUnavailable {
		row("CI minutes", "unavailable", m.Note)
	} else {
		row("CI minutes per run", fmt.Sprintf("%.1f", m.PerRun), "source: "+m.Source+" ("+m.Note+")")
		row("CI minutes executed", fmt.Sprintf("%.1f", m.ExecutedMinutes), "")
		row("CI minutes baseline", fmt.Sprintf("%.1f", m.BaselineMinutes), "")
		row("CI minutes saved", fmt.Sprintf("%.1f", m.SavedMinutes), "")
	}
	if m.ObservedRuns > 0 {
		row("Observed run duration", fmt.Sprintf("%.1f", m.ObservedMedian), fmt.Sprintf("median minutes over %d completed runs; max %.1f", m.ObservedRuns, m.ObservedMax))
	}
	l := s.EnqueueToLanded
	row("Enqueue to landed", fmt.Sprintf("p50 %s", seconds(l.P50)), fmt.Sprintf("p90 %s (%d PRs)", seconds(l.P90), l.Count))
	row("Bisections", fmt.Sprint(s.Isolation.Bisections), fmt.Sprintf("%d bisection runs", s.Isolation.BisectionRuns))
	var reasons []string
	for _, reason := range slices.Sorted(maps.Keys(s.Isolation.ByReason)) {
		reasons = append(reasons, fmt.Sprintf("%s %d", reason, s.Isolation.ByReason[reason]))
	}
	row("Dequeued", fmt.Sprint(s.Isolation.Dequeued), strings.Join(reasons, "; "))
	if err := t.Flush(); err != nil {
		return err
	}
	// Rows without detail would otherwise end in column padding.
	var tail strings.Builder
	for line := range strings.Lines(table.String()) {
		tail.WriteString(strings.TrimRight(line, " \n") + "\n")
	}
	fmt.Fprintf(&tail, "\nBaseline: %s\nExecuted: %s\n", s.Baseline, s.Executed)
	for _, w := range s.Warnings {
		fmt.Fprintf(&tail, "WARNING: %s\n", w)
	}
	_, err := io.WriteString(out, tail.String())
	return err
}

func seconds(v float64) string {
	return (time.Duration(v * float64(time.Second))).Round(time.Second).String()
}
