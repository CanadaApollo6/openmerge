package serve

import (
	"context"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Read the committed journal so retries, rejected commands and stale emissions
// do not masquerade as transitions. This cursor is process-local; logs are best
// effort, while the journal remains the durable source of truth.
func (s *Server) logCommitted(ctx context.Context, w *worker) {
	records, err := s.store.EventsAfter(ctx, w.id, w.logWatermark)
	if err != nil {
		s.options.Logger.Warn("read operational events", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
		return
	}
	for _, record := range records {
		before := w.logState
		after := queue.Apply(before, record.Event)
		s.logEvent(ctx, w, before, after, record.Event)
		w.logState, w.logWatermark = after, record.ID
	}
}

func (s *Server) logEvent(ctx context.Context, w *worker, before, after queue.State, event queue.Event) {
	log := func(message string, pr int, batch, sha, reason string, extra ...any) {
		attrs := []any{"repository", w.Key.Repository, "base", w.Key.BaseBranch, "pr", pr, "batch", batch, "sha", sha, "reason", reason}
		s.options.Logger.InfoContext(ctx, message, append(attrs, extra...)...)
	}
	batchLog := func(message, reason string, batch *queue.Batch, extra ...any) {
		if batch == nil {
			return
		}
		for _, pin := range batch.PRs {
			log(message, pin.PR, batch.ID, batch.TestedSHA, reason, extra...)
		}
	}
	for _, entry := range after.Entries {
		found := false
		for _, old := range before.Entries {
			found = found || old.AdmissionID == entry.AdmissionID
		}
		if !found {
			log("enqueued", entry.PR, "", entry.HeadSHA, "requested")
		}
	}
	switch e := event.(type) {
	case queue.RulesBlocked:
		if before.RulesRevision() != after.RulesRevision() {
			log("repository rules blocked", 0, "", after.BaseSHA, "repository-rules", "detail", strings.Join(e.Problems, "; "))
		}
	case queue.RulesCleared:
		if before.RulesRevision() != after.RulesRevision() {
			log("repository rules cleared", 0, "", after.BaseSHA, "repository-rules")
		}
	case queue.Dequeued:
		for _, old := range before.Entries {
			removed := true
			for _, entry := range after.Entries {
				removed = removed && entry.AdmissionID != e.AdmissionID
			}
			if removed && old.AdmissionID == e.AdmissionID && old.PR == e.PR {
				batch := e.BatchID
				if batch == "" && before.Active != nil {
					batch = before.Active.ID
				}
				log("dequeued", e.PR, batch, old.HeadSHA, string(e.Reason))
			}
		}
	case queue.TestRefBuilt:
		if after.Active != nil && after.Active.TestedSHA == e.TestedSHA {
			batchLog("test ref built", "", after.Active, "ref", after.Active.TestRef)
		}
	case queue.ChecksCompleted:
		result := fmt.Sprint(e.Success)
		if before.Active != nil && before.Active.TestedSHA == e.TestedSHA && w.logChecks != result {
			batchLog("checks result", "", before.Active, "success", e.Success)
			w.logChecks = result
		}
	case queue.ChecksReopened:
		if before.Active != nil && before.Active.TestedSHA == e.TestedSHA && w.logChecks != "pending" {
			batchLog("checks result", "pending", before.Active)
			w.logChecks = "pending"
		}
	case queue.BatchStarted:
		w.logChecks = ""
	case queue.BaseMoved:
		if before.Active != nil && after.Active == nil {
			batchLog("abandoned", "base-moved", before.Active)
		}
	case queue.LandingStarted:
		if after.Active != nil && after.Active.LandingAttempt.ID == e.AttemptID {
			batchLog("landing started", "", after.Active, "attempt", e.AttemptID)
		}
	case queue.Landed:
		if before.Active != nil && after.Active == nil {
			batchLog("landed", "", before.Active)
		}
	case queue.LandingAbandoned:
		if before.Active != nil && after.Active == nil {
			batchLog("abandoned", "fenced", before.Active)
		}
	case queue.TestRefBuildFailed:
		batchLog("test ref build failed", "", before.Active, "conflict", e.Conflict)
	}
	cfg := w.plannerConfig(s.options.PollInterval)
	oldReason, newReason := before.PauseReason(cfg), after.PauseReason(cfg)
	if oldReason != newReason || before.PauseDetail(cfg) != after.PauseDetail(cfg) {
		batch, sha := "", after.BaseSHA
		if after.Active != nil {
			batch, sha = after.Active.ID, after.Active.TestedSHA
		}
		if newReason == "" {
			log("queue resumed", 0, batch, sha, oldReason)
		} else {
			log("queue paused", 0, batch, sha, newReason, "detail", after.PauseDetail(cfg))
			if newReason == "invalid-config" {
				log("configuration error", 0, batch, sha, newReason, "error", after.PauseDetail(cfg))
			}
		}
	}
}
