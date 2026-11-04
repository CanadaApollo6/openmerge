package serve

import (
	"context"
	"fmt"
	"reflect"
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
	batchChanged := func(id string) bool {
		old := before.Batch(id)
		if old == nil {
			return false
		}
		if !reflect.DeepEqual(old, after.Batch(id)) {
			return true
		}
		for _, pin := range old.PRs {
			if before.PendingDequeue(pin.PR) != after.PendingDequeue(pin.PR) {
				return true
			}
		}
		return false
	}
	failed := func(id string, reason queue.DequeueReason) {
		if !batchChanged(id) {
			return
		}
		b := before.Batch(id)
		view := before
		// Diagnostic helpers predate chains and describe Active only.
		view.Active, view.Following = b, nil
		batchLog("batch failed", string(reason), b, "detail", view.DequeueDetail(b.PRs[0].PR, reason))
	}
	built := func(id, sha string) {
		old, batch := before.Batch(id), after.Batch(id)
		if old != nil && old.Phase == queue.Building && batch != nil && batch.TestedSHA == sha && batch.Phase == queue.Testing {
			batchLog("test ref built", "", batch, "ref", batch.TestRef)
		}
	}
	checks := func(batch *queue.Batch, result string) {
		if batch == nil || w.logChecks[batch.ID] == result {
			return
		}
		if w.logChecks == nil {
			w.logChecks = make(map[string]string)
		}
		if result == "pending" {
			batchLog("checks result", "pending", batch)
		} else {
			batchLog("checks result", "", batch, "success", result == "true")
		}
		w.logChecks[batch.ID] = result
	}
	checksForSHA := func(sha string) *queue.Batch {
		for _, b := range before.Batches() {
			if b.TestedSHA == sha && b.Phase == queue.Testing && b.InvalidationReason == "" {
				return b
			}
		}
		return nil
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
				if b := before.BatchForPR(e.PR); batch == "" && b != nil {
					batch = b.ID
				}
				log("dequeued", e.PR, batch, old.HeadSHA, string(e.Reason), "detail", before.DequeueDetail(e.PR, e.Reason))
			}
		}
	case queue.FastForwardRefBuilt:
		built(e.BatchID, e.TestedSHA)
	case queue.FastForwardBlocked:
		if batchChanged(e.BatchID) {
			batchLog("fast-forward blocked", "fast-forward-required", before.Batch(e.BatchID))
		}
	case queue.FastForwardMergeCommitsBlocked:
		if batchChanged(e.BatchID) {
			log("fast-forward blocked", e.PR, e.BatchID, "", string(queue.MergeCommitsNotAllowed))
		}
	case queue.TestRefBuilt:
		built(e.BatchID, e.TestedSHA)
	case queue.ChainedTestRefBuilt:
		built(e.BatchID, e.TestedSHA)
	case queue.CheckProvenanceObserved:
		if detail := after.BatchCheckProvenanceDetail(e.BatchID); before.BatchCheckProvenanceDetail(e.BatchID) != detail {
			reason := "unbound-check-provenance"
			if detail == "" {
				reason = ""
			}
			batchLog("check provenance", reason, after.Batch(e.BatchID), "detail", detail)
		}
	case queue.BatchFailed:
		failed(e.BatchID, e.Reason)
	case queue.ChecksFailureConfirmed:
		failed(e.BatchID, e.Reason)
	case queue.BatchDiagnosed:
		failed(e.BatchID, e.Reason)
	case queue.ChecksObserved:
		b := after.Batch(e.BatchID)
		if b == nil || b.Phase != queue.Testing || b.InvalidationReason != "" || b.TestedSHA != e.TestedSHA || e.Validate() != nil {
			break
		}
		r := b.CheckRetry
		if r == nil || r.Confirmed || r.Unsupported || r.OutcomeUnknown || r.Timeout != e.Timeout || !r.OriginalDeadline.Equal(e.OriginalDeadline) {
			break
		}
		stale := (r.Requested || e.Success && len(r.FailedRuns) > 0) && !r.NewAttempts(e.Runs)
		for _, run := range e.Runs {
			for _, latest := range r.ObservedRuns {
				if run.Name == latest.Name && run.Source == latest.Source && (run.ID < latest.ID || run.ID == latest.ID && max(run.Attempt, 1) < max(latest.Attempt, 1)) {
					stale = true
				}
			}
		}
		if e.Terminal && stale && !batchChanged(e.BatchID) {
			break
		}
		// Repeated terminal observations can leave the fold unchanged after a
		// pending read. Mixed stale reads can instead revoke an earlier pass.
		result := "pending"
		if e.Terminal && !stale {
			result = fmt.Sprint(after.ChecksPassed(b.TestedSHA))
		}
		checks(b, result)
	case queue.ChecksCompleted:
		if b := checksForSHA(e.TestedSHA); b != nil && (b.CheckRetry == nil || !b.CheckRetry.Confirmed) && (!e.Success || after.ChecksPassed(e.TestedSHA)) {
			checks(b, fmt.Sprint(e.Success))
		}
	case queue.ChecksReopened:
		if b := checksForSHA(e.TestedSHA); b != nil && batchChanged(b.ID) {
			checks(b, "pending")
		}
	case queue.OutsidePushRecorded:
		if e.Validate() == nil {
			log("outside push recorded", 0, "", e.NewSHA, "outside-queue", "previous_sha", e.PreviousSHA, "rewrite", e.Rewrite, "ancestry_known", e.AncestryKnown, "history_known", e.HistoryKnown, "truncated", e.Truncated, "possible_stale_observation", e.PossibleStaleObservation, "verification_before_landing", e.VerificationBeforeLanding)
		}
	case queue.BaseMoved:
		if before.Active != nil && after.Active == nil {
			batchLog("abandoned", "base-moved", before.Active)
		}
	case queue.ConfigApplied:
		// The first application at startup is not a change worth an INFO line.
		if before.ConfigDigest != "" && before.ConfigDigest != after.ConfigDigest {
			log("configuration changed", 0, "", after.BaseSHA, "", "commit", e.Commit, "digest", e.Digest)
		}
		if before.Active != nil && (after.Active == nil || after.Active.ID != before.Active.ID) {
			batchLog("abandoned", "config-changed", before.Active)
		}
	case queue.LandingStarted:
		if b := after.Batch(e.BatchID); b != nil && b.Phase == queue.Landing && b.LandingAttempt.ID == e.AttemptID && batchChanged(e.BatchID) {
			batchLog("landing started", "", b, "attempt", e.AttemptID)
		}
	case queue.Landed:
		if b := before.Batch(e.BatchID); b != nil && after.Batch(e.BatchID) == nil {
			batchLog("landed", "", b)
		}
	case queue.LandingAbandoned:
		if b := before.Batch(e.BatchID); b != nil && after.Batch(e.BatchID) == nil {
			batchLog("abandoned", "fenced", b)
		}
	case queue.BatchesInvalidated:
		for _, id := range e.BatchIDs {
			if b := before.Batch(id); b != nil && after.Batch(id) == nil {
				batchLog("abandoned", e.Reason, b)
			}
		}
	case queue.TestRefBuildFailed:
		if batchChanged(e.BatchID) {
			batchLog("test ref build failed", "", before.Batch(e.BatchID), "conflict", e.Conflict)
		}
	}
	for id := range w.logChecks {
		if after.Batch(id) == nil {
			delete(w.logChecks, id)
		}
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
