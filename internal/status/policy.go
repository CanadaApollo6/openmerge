package status

import (
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ApplyPolicy supplies runtime configuration which the historical digest does
// not encode. No default window is guessed when that configuration is absent.
func ApplyPolicy(q *Queue, selected *Entry, cfg queue.Config, now time.Time) {
	q.Window = q.state.Window(cfg)
	q.BatchSize = q.state.BatchSize(cfg)
	failures, samples := q.state.FailureEstimate()
	q.FailureEstimate = &FailureEstimate{Failures: failures, Samples: samples, TargetPassRate: cfg.BatchTargetPassRate}
	if samples > 0 {
		q.FailureEstimate.Rate = float64(failures) / float64(samples)
	}
	q.LastWindowFailure = q.state.LastWindowFailure()
	for i := range q.Entries {
		q.Entries[i].Gate = holdingGate(q, q.Entries[i], cfg, now)
		q.Entries[i].WaitingReason = DeriveWaitingReason(q, q.Entries[i])
	}
	if selected != nil {
		selected.Gate = holdingGate(q, *selected, cfg, now)
		selected.WaitingReason = DeriveWaitingReason(q, *selected)
	}
}

func holdingGate(q *Queue, e Entry, cfg queue.Config, now time.Time) string {
	if e.Position == 0 {
		return ""
	}
	if q.Readiness != nil && !q.Readiness.Ready {
		return q.Readiness.Reason
	}
	if q.Paused {
		return q.PauseReason
	}
	if e.Status == "dequeue-pending" {
		return "dequeue"
	}
	if e.Status == "invalidating" {
		return "invalidation"
	}
	if b := q.state.BatchForPR(e.PR); b != nil {
		if b.InvalidationReason != "" {
			return "invalidation"
		}
		if b.Phase == queue.Building {
			return "build"
		}
		if b.Phase == queue.Landing {
			return "landing-settlement"
		}
		if !q.state.ChecksPassed(b.TestedSHA) {
			return "checks"
		}
		if b != q.state.Active {
			return "predecessor-settlement"
		}
		return "landing-preflight"
	}
	if e.Change != nil && len(e.Change.WaitingReasons) > 0 {
		return "approvals"
	}
	if q.Window == 0 {
		return ""
	}
	if q.state.PendingBisection() {
		return "bisection"
	}
	for _, b := range q.state.Batches() {
		if b.FromBisection {
			return "bisection"
		}
	}
	if len(q.Chain) >= q.Window {
		return "window-full"
	}
	for _, b := range q.state.Batches() {
		if b.ProvisionalFailure != "" || b.CheckRetry != nil && b.CheckRetry.RerunState == "failed" {
			return "predecessor-checks"
		}
		if b.Phase == queue.Building {
			return "predecessor-build"
		}
	}
	unbatched := 0
	var oldest time.Time
	for _, entry := range q.Entries {
		if entry.BatchID == "" {
			unbatched++
			if entry.AdmittedAt != nil && (oldest.IsZero() || entry.AdmittedAt.Before(oldest)) {
				oldest = *entry.AdmittedAt
			}
		}
	}
	if cfg.BatchMaxWait > 0 && unbatched < q.BatchSize && !oldest.IsZero() && now.Before(oldest.Add(cfg.BatchMaxWait)) {
		return "max_wait"
	}
	return "batch-admission"
}

// LedgerSummary bounds the check summary while the API and CLI retain history.
// The forge applies its normal sanitization to the complete outgoing summary.
func LedgerSummary(entry *Entry) string {
	if entry == nil {
		return ""
	}
	text := ""
	if entry.Gate != "" {
		text = "; gate: " + entry.Gate
	}
	if len(entry.RunLedger) == 0 {
		return text
	}
	text += "\n\nCI run ledger:\n"
	rows := entry.RunLedger
	if len(rows) > 8 {
		text += fmt.Sprintf("%d earlier run(s)\n", len(rows)-8)
		rows = rows[len(rows)-8:]
	}
	for i, row := range rows {
		if i > 0 {
			text += "\n"
		}
		identity := "aggregate (identity unavailable)"
		if row.Identity != nil {
			identity = fmt.Sprintf("%s/%s#%d.%d", row.Identity.Name, row.Identity.Source, row.Identity.ID, row.Identity.Attempt)
		}
		sha := row.TestedSHA[:min(12, len(row.TestedSHA))]
		fragment := fmt.Sprintf("%s %s %s=%s", row.BatchID, sha, identity, row.Outcome)
		if row.WastedReason != "" {
			fragment += " wasted: " + row.WastedReason
		}
		if row.Cancellation != "" {
			fragment += " cancellation: " + row.Cancellation
		}
		// Bound individual untrusted names independently of the final check limit.
		runes := []rune(fragment)
		if len(runes) > 160 {
			fragment = string(runes[:160]) + "…"
		}
		text += fragment
	}
	return text
}

// AdaptiveSummary is bounded queue-level evidence shared by CLI and PR checks.
func AdaptiveSummary(q *Queue) string {
	text := fmt.Sprintf("window: %d; batch size: %d", q.Window, q.BatchSize)
	if e := q.FailureEstimate; e != nil {
		if e.Samples == 0 {
			text += "; p̂: n/a (0 samples)"
		} else {
			text += fmt.Sprintf("; p̂: %.4f (%d/%d)", e.Rate, e.Failures, e.Samples)
		}
		if e.TargetPassRate > 0 {
			text += fmt.Sprintf("; target: %g", e.TargetPassRate)
		}
	}
	if len(q.Chain) > 0 {
		text += "; chain: "
		for i, b := range q.Chain[:min(8, len(q.Chain))] {
			if i > 0 {
				text += " → "
			}
			text += fmt.Sprintf("%.40s(%.24s)", b.ID, b.Phase)
		}
		if len(q.Chain) > 8 {
			text += fmt.Sprintf(" (+%d)", len(q.Chain)-8)
		}
	}
	if f := q.LastWindowFailure; f != nil {
		text += fmt.Sprintf("; last window failure: revision %d %.40s %.24s", f.Revision, f.BatchID, f.Reason)
	}
	return text
}
