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
	case queue.BlockSourceBlocked:
		if before.BlockSourceRevision(e.Source) != after.BlockSourceRevision(e.Source) {
			log("queue block set", 0, "", after.BaseSHA, string(e.Source), "source", e.Source, "detail", e.Detail, "revision", after.BlockSourceRevision(e.Source))
		}
	case queue.BlockSourceCleared:
		if before.BlockSourceRevision(e.Source) != after.BlockSourceRevision(e.Source) {
			log("queue block cleared", 0, "", after.BaseSHA, string(e.Source), "source", e.Source, "revision", after.BlockSourceRevision(e.Source))
		}
	case queue.RulesBlocked:
		if before.RulesRevision() != after.RulesRevision() {
			log("repository rules blocked", 0, "", after.BaseSHA, "repository-rules", "detail", strings.Join(e.Problems, "; "))
		}
	case queue.RulesCleared:
		if before.RulesRevision() != after.RulesRevision() {
			log("repository rules cleared", 0, "", after.BaseSHA, "repository-rules")
		}
	case queue.RulesSourceBlocked:
		if before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) {
			log("repository rules blocked", 0, "", after.BaseSHA, "repository-rules", "source", e.Source, "detail", strings.Join(e.Problems, "; "))
		}
	case queue.RulesSourceCleared:
		if before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) {
			log("repository rules cleared", 0, "", after.BaseSHA, "repository-rules", "source", e.Source)
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
				log("dequeued", e.PR, batch, old.HeadSHA, string(e.Reason), "detail", before.DequeueDetail(e.PR, e.Reason))
			}
		}
	case queue.FastForwardRefBuilt:
		if after.Active != nil && after.Active.TestedSHA == e.TestedSHA {
			batchLog("test ref built", "", after.Active, "ref", after.Active.TestRef)
		}
	case queue.FastForwardBlocked:
		batchLog("fast-forward blocked", "fast-forward-required", before.Active)
	case queue.FastForwardMergeCommitsBlocked:
		log("fast-forward blocked", e.PR, e.BatchID, "", string(queue.MergeCommitsNotAllowed))
	case queue.TestRefBuilt:
		if after.Active != nil && after.Active.TestedSHA == e.TestedSHA {
			batchLog("test ref built", "", after.Active, "ref", after.Active.TestRef)
		}
	case queue.CheckProvenanceObserved:
		if before.CheckProvenanceDetail() != after.CheckProvenanceDetail() {
			reason := "unbound-check-provenance"
			if after.CheckProvenanceDetail() == "" {
				reason = ""
			}
			batchLog("check provenance", reason, after.Active, "detail", after.CheckProvenanceDetail())
		}
	case queue.BatchFailed:
		if before.Active != nil && before.Active.ID == e.BatchID && before.Active.Phase == queue.Testing && !before.ChecksPassed(before.Active.TestedSHA) {
			batchLog("batch failed", string(e.Reason), before.Active, "detail", before.DequeueDetail(before.Active.PRs[0].PR, e.Reason))
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
	case queue.OutsidePushRecorded:
		log("outside push recorded", 0, "", e.NewSHA, "outside-queue", "previous_sha", e.PreviousSHA, "rewrite", e.Rewrite, "ancestry_known", e.AncestryKnown, "history_known", e.HistoryKnown, "truncated", e.Truncated, "possible_stale_observation", e.PossibleStaleObservation, "verification_before_landing", e.VerificationBeforeLanding)
	case queue.BaseMoved:
		if before.Active != nil && after.Active == nil {
			batchLog("abandoned", "base-moved", before.Active)
		}
	case queue.ConfigApplied:
		// The first application at startup is not a change worth an INFO line.
		if before.ConfigDigest != "" && before.ConfigDigest != e.Digest {
			log("configuration changed", 0, "", after.BaseSHA, "", "commit", e.Commit, "digest", e.Digest)
		}
		if before.Active != nil && (after.Active == nil || after.Active.ID != before.Active.ID) {
			batchLog("abandoned", "config-changed", before.Active)
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
