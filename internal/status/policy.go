package status

import (
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ApplyPolicy supplies runtime configuration which the historical digest does
// not encode. No default window is guessed when that configuration is absent.
func ApplyPolicy(q *Queue, selected *Entry, cfg queue.Config, now time.Time) {
	q.Window = cfg.MaxDepth()
	q.BatchSize = max(1, cfg.BatchMaxSize)
	for i := range q.Entries {
		q.Entries[i].Gate = holdingGate(q, q.Entries[i], cfg, now)
	}
	if selected != nil {
		selected.Gate = holdingGate(q, *selected, cfg, now)
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
	if len(q.Chain) >= cfg.MaxDepth() {
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
	if cfg.BatchMaxWait > 0 && unbatched < max(1, cfg.BatchMaxSize) && !oldest.IsZero() && now.Before(oldest.Add(cfg.BatchMaxWait)) {
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
