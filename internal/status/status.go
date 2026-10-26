// Package status owns the versioned read-only API projection, independently of
// runtime queue structs and the persisted event wire format.
package status

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Version is the public response schema version.
const Version = 1

// Response carries queue detail, queue lists, or discovery observations.
type Response struct {
	Version   int        `json:"version"`
	Discovery *Discovery `json:"discovery,omitempty"`
	Queues    []Queue    `json:"queues,omitempty"`
	Queue     *Queue     `json:"queue,omitempty"`
	PR        *Entry     `json:"pr,omitempty"`
	Why       *Why       `json:"why,omitempty"`
}

// Queue describes one served installation/repository/base/lane partition.
type Queue struct {
	Authority         *Authority                 `json:"authority,omitempty"`
	OutsideWarnings   *OutsideWarnings           `json:"outside_warnings,omitempty"`
	OutsidePushes     []OutsidePush              `json:"outside_pushes,omitempty"`
	Readiness         *Readiness                 `json:"readiness,omitempty"`
	State             string                     `json:"state"`
	Installation      string                     `json:"installation"`
	Repository        string                     `json:"repo"`
	Base              string                     `json:"base"`
	Lane              string                     `json:"lane"`
	Watermark         int64                      `json:"watermark"`
	BaseSHA           string                     `json:"base_sha"`
	Paused            bool                       `json:"paused"`
	PauseReason       string                     `json:"pause_reason"`
	PauseDetail       string                     `json:"pause_detail"`
	Blocks            []Block                    `json:"blocks"`
	Chain             []*Batch                   `json:"chain,omitempty"`
	Invalidations     []queue.BatchesInvalidated `json:"invalidations,omitempty"`
	ActiveBatch       *Batch                     `json:"active_batch"`
	Entries           []Entry                    `json:"entries"`
	FailureEstimate   *FailureEstimate           `json:"failure_estimate,omitempty"`
	LastWindowFailure *queue.WindowFailure       `json:"last_window_failure,omitempty"`
	Window            int                        `json:"window,omitempty"`
	BatchSize         int                        `json:"batch_size,omitempty"`
	hasAuthorityPin   bool
	sanitizer         *diagnostic.Sanitizer
	state             queue.State
	policyPins        []briefPolicyPin
	eligibilityPins   map[queue.ChangeID]briefEligibilityPin
	known             map[int]Entry
	runLedger         map[int][]Run
	RulesSources      []RulesSource `json:"rules_sources,omitempty"`
}

// OutsidePush identifies durable evidence for a base move after fence settlement.
// EventID is the journal ID for future operator acknowledgement, not an actor ID.
type OutsidePush struct {
	EventID int64 `json:"event_id"`
	queue.OutsidePushRecorded
}

// RulesSource lists one source's independently revisioned policy findings.
type RulesSource struct {
	Source   string   `json:"source"`
	Problems []string `json:"problems"`
	Revision uint64   `json:"revision"`
}

// Block describes one active cause preventing new work; multiple may coexist.
type Block struct {
	Source   string `json:"source"`
	Revision uint64 `json:"revision"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail"`
}

// Batch pins the tested inputs and the operation currently awaiting an outcome.
type Batch struct {
	Position           uint64                 `json:"position,omitempty"`
	ParentBatchID      string                 `json:"parent_batch_id,omitempty"`
	BatchesAhead       int                    `json:"batches_ahead"`
	LandingSHA         string                 `json:"landing_sha,omitempty"`
	InvalidationReason string                 `json:"invalidation_reason,omitempty"`
	ProvisionalFailure string                 `json:"provisional_failure,omitempty"`
	ID                 string                 `json:"id"`
	Phase              string                 `json:"phase"`
	BaseSHA            string                 `json:"base_sha"`
	TestedSHA          string                 `json:"tested_sha"`
	TestRef            string                 `json:"test_ref"`
	Method             string                 `json:"method"`
	PRs                []BatchPR              `json:"prs"`
	CheckRetry         *queue.CheckRetryState `json:"check_retry,omitempty"`
}

// BatchPR identifies an exact head in a batch.
type BatchPR struct {
	PR      int    `json:"pr"`
	HeadSHA string `json:"head_sha"`
}

// Entry uses position zero for PRs outside the queue; admission is the latest
// successful admission, retained on a terminal PR detail response.
type Entry struct {
	WaitingReason     WaitingReason `json:"waiting_reason"`
	Landing           *LandingView  `json:"landing,omitempty"`
	Change            *ChangeReport `json:"change,omitempty"`
	Gate              string        `json:"gate,omitempty"`
	RunLedger         []Run         `json:"run_ledger,omitempty"`
	BatchID           string        `json:"batch_id,omitempty"`
	BatchPosition     uint64        `json:"batch_position,omitempty"`
	BatchesAhead      *int          `json:"batches_ahead"`
	Position          int           `json:"position"`
	PR                int           `json:"pr"`
	HeadSHA           string        `json:"head_sha"`
	AdmittedAt        *time.Time    `json:"admitted_at"`
	Status            string        `json:"status"`
	Reason            string        `json:"reason"`
	LastDequeueReason string        `json:"last_dequeue_reason"`
	Detail            string        `json:"detail,omitempty"`
}

// Why links a current-state explanation to recent committed evidence.
type Why struct {
	Summary string  `json:"summary"`
	Limit   int     `json:"limit"`
	Events  []Event `json:"events"`
}

// Event exposes stable evidence metadata without the private persistence payload.
type Event struct {
	ID                int64            `json:"id"`
	Type              string           `json:"type"`
	At                time.Time        `json:"at"`
	Summary           string           `json:"summary"`
	Count             int              `json:"count,omitempty"`
	EndID             int64            `json:"end_id,omitempty"`
	Until             *time.Time       `json:"until,omitempty"`
	Runs              []queue.CheckRun `json:"runs,omitempty"`
	RerunState        string           `json:"rerun_state,omitempty"`
	CancellationState string           `json:"cancellation_state,omitempty"`
	pairKey           string
}

// Project replays committed history. A positive PR selects its explanation;
// queue lists retain only admitted entries. Stale result events never invent
// outcomes: terminal states require an admission to actually leave the fold.
func Project(j store.Journal, pr, limit int, sanitizer *diagnostic.Sanitizer) (Queue, *Entry, *Why) {
	s := j.Seed
	policyPins := []briefPolicyPin{}
	eligibilityPins := map[queue.ChangeID]briefEligibilityPin{}
	configPin := queue.ConfigApplied{Commit: j.Seed.ConfigCommit, Digest: j.Seed.ConfigDigest}
	known := make(map[int]Entry)
	heads := make(map[int]string)
	var tested map[string]bool
	var inheritedPause map[int64]bool
	events := make([]Event, 0)
	var outsidePushes []OutsidePush
	// Audit evidence comes only from journal rows, which retain stable IDs.
	var previous queue.Event
	var watermark int64
	visit := func(record store.JournalEvent, before, after queue.State) {
		s = after
		if e, ok := record.Event.(queue.ConfigApplied); ok && e.Commit == after.ConfigCommit && e.Digest == after.ConfigDigest {
			configPin = e
		}
		// Same-digest authority rebinds retain the legacy ConfigCommit in the
		// fold. Their adjacent pair still supplies the actual governing tree
		// used by subsequent classifications and historical brief rendering.
		if pin, ok := record.Event.(queue.PolicyAuthorityApplied); ok && pin.OpenMerge != nil {
			if applied, ok := previous.(queue.ConfigApplied); ok && applied.Commit == pin.GoverningCommit && applied.Digest == after.ConfigDigest && after.AuthorityPin() != nil && after.AuthorityPin().GoverningCommit == pin.GoverningCommit {
				configPin = applied
			}
		}
		governingConfig := configPin.Commit == after.ConfigCommit
		if pin := after.AuthorityPin(); pin != nil && pin.OpenMerge != nil && pin.GoverningCommit == configPin.Commit {
			governingConfig = true
		}
		if e, ok := record.Event.(queue.ChangeClassified); ok && governingConfig && configPin.Digest == after.ConfigDigest {
			policyPins = append(policyPins, briefPolicyPin{e.Evidence, configPin})
		}
		pinBriefEligibility(eligibilityPins, record.Event, before, after)
		if e, ok := record.Event.(queue.OutsidePushRecorded); ok {
			outsidePushes = append(outsidePushes, OutsidePush{EventID: record.ID, OutsidePushRecorded: e})
			if len(outsidePushes) > queue.OutsidePushLimit {
				outsidePushes = outsidePushes[len(outsidePushes)-queue.OutsidePushLimit:]
			}
		}
		watermark = record.ID
		n := directPR(record.Event)
		if n > 0 {
			e := known[n]
			e.PR = n
			if e.Status == "" {
				e.Status = "not-queued"
			}
			switch v := record.Event.(type) {
			case queue.PRObserved:
				heads[n] = v.HeadSHA
				if e.HeadSHA != v.HeadSHA || e.Status != "merged" {
					e.Status, e.Reason, e.Detail = "not-queued", "", ""
					if !v.Eligible {
						e.Status, e.Reason = "blocked", string(queue.NotEligible)
					} else if !v.Trusted {
						e.Status, e.Reason = "blocked", string(queue.Untrusted)
					}
				}
				e.HeadSHA = v.HeadSHA
			case queue.EnqueueRequested:
				if e.HeadSHA == "" && !entryIn(s, n) {
					e.Status, e.Reason = "blocked", string(queue.NotEligible)
				}
			case queue.PRClosed:
				if e.Status != "merged" && e.Status != "dequeued" {
					e.Status, e.Reason = "closed", string(queue.Closed)
				}
			case queue.Dequeued:
				if entryIn(before, n) && !entryIn(s, n) {
					e.Status, e.Reason, e.LastDequeueReason = "dequeued", string(v.Reason), string(v.Reason)
					if (before.CheckRetryDetail() != "" || v.Reason == queue.ChecksTimeout && before.CheckProvenanceDetail() != "") && inBatch(before, n) {
						e.Detail = sanitizer.Sanitize(before.DequeueDetail(n, v.Reason))
					}
				}
			}
			if held, ok := s.HeldRequest(n); ok {
				e.Status, e.Reason, e.Detail = "waiting-entry", string(WaitingEntry), sanitizer.Sanitize(held.Conditions)
			} else if _, wasHeld := before.HeldRequest(n); wasHeld {
				switch record.Event.(type) {
				case queue.DequeueRequested, queue.ChangeRevisionObserved:
					e.Status, e.Reason, e.Detail = "not-queued", "", ""
				}
			}
			known[n] = e
		}
		if landed, ok := record.Event.(queue.Landed); ok && before.Active != nil {
			for _, pin := range before.Active.PRs {
				if entryIn(before, pin.PR) && !entryIn(s, pin.PR) {
					e := known[pin.PR]
					e.Status, e.Reason, e.HeadSHA = "merged", "", pin.HeadSHA
					e.Landing = &LandingView{Outcome: "landed", SHA: landed.NewBaseSHA, EventID: record.ID, BatchID: before.Active.ID, AttemptID: before.Active.LandingAttempt.ID}
					known[pin.PR] = e
				}
			}
		}
		for _, admitted := range s.Entries {
			e := known[admitted.PR]
			e.PR, e.HeadSHA, e.AdmittedAt = admitted.PR, admitted.HeadSHA, &admitted.EnqueuedAt
			known[e.PR] = e
		}
		if pr > 0 && (inheritedPause[record.ID] || affects(record.Event, pr, before, s, tested)) {
			kind, _, _ := queue.EncodeEvent(record.Event)
			evidence := Event{ID: record.ID, Type: kind, At: record.RecordedAt.UTC(), Summary: sanitizer.Sanitize(eventSummary(record.Event, before, s))}
			evidence.Runs, evidence.RerunState = retryEvidence(record.Event, before, s, sanitizer)
			switch e := record.Event.(type) {
			case queue.CheckCancellationDeferred:
				evidence.CancellationState = "requested"
				if e.Abandoned {
					evidence.CancellationState = "abandoned"
				}
			case queue.CheckCancellationRequested:
				evidence.CancellationState = "requested"
			case queue.CheckCancellationReported:
				evidence.CancellationState = "requested"
				if e.Unsupported {
					evidence.CancellationState = "unsupported"
				} else if len(e.Confirmed) > 0 {
					evidence.CancellationState = "confirmed"
				}
			}
			events = appendEvidence(events, evidence, record.Event, previous, before)
			previous = record.Event
			// Keep one extra row until a reopen's matching refresh arrives.
			if len(events) > limit+1 {
				events = slices.Delete(events, 0, len(events)-limit-1)
			}
		}
	}
	var ledger map[int][]Run
	if pr == 0 {
		ledger = runLedger(j, sanitizer, visit)
	} else {
		// Explanations can reference CI observed before its tested SHA was
		// committed. Retain replay frames only for that selected-PR read.
		frames := make([]projectionFrame, 0, len(j.Events))
		ledger = runLedger(j, sanitizer, func(_ store.JournalEvent, before, after queue.State) {
			frames = append(frames, projectionFrame{before, after})
		})
		tested, inheritedPause = explanationContext(j, pr, frames)
		for i, record := range j.Events {
			visit(record, frames[i].before, frames[i].after)
		}
	}
	for n, head := range heads {
		e := known[n]
		e.HeadSHA = head
		known[n] = e
	}
	q := Queue{hasAuthorityPin: j.HasAuthorityPin || s.AuthorityPin() != nil, OutsideWarnings: ProjectOutsideWarnings(j.OutsideWarnings), OutsidePushes: outsidePushes, State: s.LifecycleState(), Installation: j.Key.Installation, Repository: j.Key.Repository, Base: j.Key.BaseBranch, Lane: j.Key.Lane,
		Watermark: watermark, BaseSHA: s.BaseSHA, PauseReason: sanitizer.Sanitize(s.PauseReason(queue.Config{})), PauseDetail: sanitizer.Sanitize(s.PauseDetail(queue.Config{})), Blocks: make([]Block, 0), Entries: make([]Entry, 0, len(s.Entries))}
	for _, block := range s.Blocks(queue.Config{}) {
		q.Blocks = append(q.Blocks, Block{Source: string(block.Source), Revision: block.Revision, Reason: sanitizer.Sanitize(block.Reason), Detail: sanitizer.Sanitize(block.Detail)})
	}
	for _, source := range s.RulesSources() {
		problems := make([]string, 0, len(source.Problems))
		for _, problem := range source.Problems {
			problems = append(problems, sanitizer.Sanitize(problem))
		}
		q.RulesSources = append(q.RulesSources, RulesSource{string(source.Source), problems, source.Revision})
	}
	q.Paused = len(q.Blocks) > 0
	q.Authority = ProjectAuthority(s, time.Now())
	if q.Authority != nil {
		for i := range q.Authority.Elements {
			q.Authority.Elements[i].Element = sanitizer.Sanitize(q.Authority.Elements[i].Element)
		}
	}
	q.state = s
	q.runLedger = ledger
	q.sanitizer = sanitizer
	q.known = known
	q.policyPins = policyPins
	q.eligibilityPins = eligibilityPins
	for i, b := range s.Batches() {
		item := &Batch{ID: b.ID, Phase: string(b.Phase), BaseSHA: b.BaseSHA, TestedSHA: b.TestedSHA, TestRef: b.TestRef, Method: string(b.Method), PRs: make([]BatchPR, 0, len(b.PRs)), CheckRetry: cloneCheckRetry(b.CheckRetry), Position: max(1, b.ChainPosition), ParentBatchID: b.ParentBatchID, BatchesAhead: i, LandingSHA: b.LandingCommit(), InvalidationReason: b.InvalidationReason, ProvisionalFailure: provisionalFailure(b)}
		if item.CheckRetry != nil {
			item.CheckRetry.Detail = sanitizer.Sanitize(item.CheckRetry.Detail)
		}
		for _, pin := range b.PRs {
			item.PRs = append(item.PRs, BatchPR{pin.PR, pin.HeadSHA})
		}
		q.Chain = append(q.Chain, item)
		if i == 0 {
			q.ActiveBatch = item
		}
	}
	q.Invalidations = s.Invalidations
	for i, admitted := range s.Entries {
		e := known[admitted.PR]
		e.HeadSHA = admitted.HeadSHA
		e.RunLedger = ledger[e.PR]
		e.Position, e.Status, e.Reason, e.Detail = i+1, "queued", "", ""
		if b := s.BatchForPR(e.PR); b != nil {
			e.BatchID, e.BatchPosition = b.ID, max(1, b.ChainPosition)
			for i, candidate := range s.Batches() {
				if candidate.ID == b.ID {
					e.BatchesAhead = &i
					break
				}
			}
			e.Status = string(b.Phase)
			if b.InvalidationReason != "" {
				e.Status, e.Reason = "invalidating", b.InvalidationReason
			}
			if b.Phase == queue.Testing {
				e.Detail = sanitizer.Sanitize(s.BatchCheckProvenanceDetail(b.ID))
				if e.Detail != "" {
					e.Reason = "unbound-check-provenance"
				}
			}
		}
		if reason := s.PendingDequeue(e.PR); reason != "" {
			e.Status, e.Reason = "dequeue-pending", string(reason)
			if s.CheckRetryDetail() != "" {
				e.Detail = sanitizer.Sanitize(s.DequeueDetail(e.PR, reason))
			}
		}
		if q.Paused {
			e.Status, e.Reason = "paused", q.PauseReason
		}
		e.Change = reportChange(j.Key, s, e.PR, sanitizer)
		e.Gate = holdingGate(&q, e, queue.Config{}, time.Time{})
		e.WaitingReason = DeriveWaitingReason(&q, e)
		q.Entries = append(q.Entries, e)
		known[e.PR] = e
	}
	e, ok := known[pr]
	if !ok {
		return q, nil, nil
	}
	if s.LifecycleState() != "active" && e.Position == 0 && e.Status != "merged" && e.Status != "closed" {
		e.Status, e.Reason = "paused", q.State
	}
	e.Change = reportChange(j.Key, s, pr, sanitizer)
	e.RunLedger = ledger[pr]
	e.Gate = holdingGate(&q, e, queue.Config{}, time.Time{})
	e.WaitingReason = DeriveWaitingReason(&q, e)
	summary := fmt.Sprintf("PR #%d: %s", pr, e.Status)
	if e.Reason != "" {
		summary += " (" + e.Reason + ")"
	}
	if q.Paused && (e.Position > 0 || s.LifecycleState() != "active") {
		for _, block := range q.Blocks {
			summary += "; " + block.Reason + " [" + block.Source + "]: " + block.Detail
		}
		summary += "; " + queue.BlockRecovery(s.Blocks(queue.Config{}))
		if inBatch(s, pr) && s.Active.Phase == queue.Landing {
			summary += "; landing outcome is still being resolved through its fence"
		}
	} else if e.Position > 0 {
		summary += fmt.Sprintf("; queue position %d", e.Position)
	}
	switch e.Status {
	case "not-queued":
		summary += "; no active admission; a writer can request @openmerge queue"
	case "blocked":
		if e.Reason == string(queue.Untrusted) {
			summary += "; the current head has not passed the exact-head trust gate"
		} else {
			summary += "; queue entry conditions are not satisfied"
		}
	case "queued":
		summary += "; waiting for the next test batch"
	case "building":
		summary += "; constructing the batch test ref"
	case "testing":
		summary += "; awaiting CI reconciliation and a passing landing preflight"
	case "landing":
		summary += "; resolving the durable landing attempt through its fence"
	case "dequeue-pending":
		summary += "; dequeue requested; an in-flight landing must settle first if present"
	}
	if e.Detail != "" {
		summary += "; " + e.Detail
	}
	if len(events) > limit {
		events = slices.Delete(events, 0, len(events)-limit)
	}
	return q, &e, &Why{Summary: summary, Limit: limit, Events: events}
}

func directPR(event queue.Event) int {
	switch e := event.(type) {
	case queue.ChangeRevisionObserved:
		return e.PR
	case queue.PRObserved:
		return e.PR
	case queue.PRClosed:
		return e.PR
	case queue.EnqueueRequested:
		return e.PR
	case queue.DequeueRequested:
		return e.PR
	case queue.Dequeued:
		return e.PR
	}
	return 0
}

func entryIn(s queue.State, pr int) bool {
	return slices.ContainsFunc(s.Entries, func(e queue.Entry) bool { return e.PR == pr })
}

func inBatch(s queue.State, pr int) bool {
	return s.BatchForPR(pr) != nil
}

func cloneCheckRetry(retry *queue.CheckRetryState) *queue.CheckRetryState {
	if retry == nil {
		return nil
	}
	cloned := *retry
	cloned.Dispatches = slices.Clone(retry.Dispatches)
	cloned.FailedRuns = slices.Clone(retry.FailedRuns)
	cloned.ObservedRuns = slices.Clone(retry.ObservedRuns)
	if cloned.RerunState == "requested" && cloned.Dispatched {
		cloned.RerunState = "running"
	}
	return &cloned
}

func retryEvidence(event queue.Event, before, after queue.State, sanitizer *diagnostic.Sanitizer) ([]queue.CheckRun, string) {
	var runs []queue.CheckRun
	var state string
	switch e := event.(type) {
	case queue.BatchRunsObserved:
		runs = e.Runs
	case queue.CheckCancellationRequested:
		runs = e.Runs
	case queue.CheckCancellationReported:
		runs = e.Confirmed
	case queue.ChecksObserved:
		runs, state = e.Runs, retryState(after, e.BatchID, e.TestedSHA)
		if state == "" {
			if !e.Terminal {
				state = "running"
			} else if e.Success {
				state = "passed"
			} else {
				state = "failed"
			}
		}
	case queue.CheckRerunRequested:
		runs, state = e.Runs, "requested"
	case queue.CheckRerunDispatchProgress:
		if before.Active != nil && before.Active.CheckRetry != nil {
			runs = before.Active.CheckRetry.FailedRuns
		}
		state = "requested"
		if e.Target.Accepted {
			state = "running"
		}
	case queue.CheckRerunDispatched:
		if before.Active != nil && before.Active.CheckRetry != nil {
			runs = before.Active.CheckRetry.FailedRuns
		}
		if e.OutcomeUnknown {
			state = "unknown"
		} else if e.Unsupported {
			state = "unsupported"
		} else {
			state = "running"
		}
	case queue.ChecksFailureConfirmed:
		if before.Active != nil && before.Active.CheckRetry != nil {
			runs = before.Active.CheckRetry.FailedRuns
		}
		state = "failed"
	}
	if len(runs) == 0 {
		return nil, state
	}
	runsCopy := slices.Clone(runs)
	for i := range runsCopy {
		runsCopy[i].Name = sanitizer.Sanitize(runsCopy[i].Name)
		runsCopy[i].Source = sanitizer.Sanitize(runsCopy[i].Source)
		runsCopy[i].State = sanitizer.Sanitize(runsCopy[i].State)
		runsCopy[i].URL = sanitizer.Sanitize(runsCopy[i].URL)
		runsCopy[i].WorkflowRunURL = sanitizer.Sanitize(runsCopy[i].WorkflowRunURL)
	}
	return runsCopy, state
}

func retryState(s queue.State, batchID, testedSHA string) string {
	b := s.Batch(batchID)
	if b == nil || b.ID != batchID || b.TestedSHA != testedSHA || b.CheckRetry == nil {
		return ""
	}
	state := b.CheckRetry.RerunState
	if state == "requested" && b.CheckRetry.Dispatched {
		return "running"
	}
	return state
}

// Correlate CI that arrived before TestRefBuilt with the batch that ultimately
// consumed it. Looking only at the state at arrival would lose retained evidence.
type projectionFrame struct{ before, after queue.State }

func explanationContext(j store.Journal, pr int, frames []projectionFrame) (map[string]bool, map[int64]bool) {
	result := make(map[string]bool)
	causes := make(map[string]int64)
	inherited := make(map[int64]bool)
	if pr == 0 {
		return result, inherited
	}
	s := j.Seed
	for i, record := range j.Events {
		before := frames[i].before
		s = frames[i].after
		switch e := record.Event.(type) {
		case queue.LifecycleChanged:
			if s.LifecycleState() == "active" {
				delete(causes, "lifecycle")
			} else {
				causes["lifecycle"] = record.ID
			}
		case queue.PauseRequested:
			causes["pause"] = record.ID
		case queue.ResumeRequested:
			delete(causes, "pause")
			delete(causes, "poison")
		case queue.ActionPoisoned:
			if causes["poison"] == 0 {
				causes["poison"] = record.ID
			}
		case queue.BlockSourceBlocked:
			if s.BlockSourceRevision(e.Source) != before.BlockSourceRevision(e.Source) {
				key := "block/" + string(e.Source)
				if e.Source == queue.Operator {
					delete(causes, "pause")
				}
				if e.Source == queue.ActionPoison {
					delete(causes, "poison")
				}
				causes[key] = record.ID
			}
		case queue.BlockSourceCleared:
			if s.BlockSourceRevision(e.Source) != before.BlockSourceRevision(e.Source) {
				delete(causes, "block/"+string(e.Source))
				if e.Source == queue.Operator {
					delete(causes, "pause")
				}
				if e.Source == queue.ActionPoison {
					delete(causes, "poison")
				}
			}
		case queue.RulesBlocked:
			if s.RulesRevision() != before.RulesRevision() {
				causes["rules/rulesets"] = record.ID
			}
		case queue.RulesCleared:
			if s.RulesRevision() != before.RulesRevision() {
				delete(causes, "rules/rulesets")
			}
		case queue.RulesSourceBlocked:
			if s.RulesSourceRevision(e.Source) != before.RulesSourceRevision(e.Source) {
				causes["rules/"+string(e.Source)] = record.ID
			}
		case queue.RulesSourceCleared:
			if s.RulesSourceRevision(e.Source) != before.RulesSourceRevision(e.Source) {
				delete(causes, "rules/"+string(e.Source))
			}
		}
		if b := s.BatchForPR(pr); b != nil && b.TestedSHA != "" {
			result[b.TestedSHA] = true
		}
	}
	if entryIn(s, pr) {
		for _, id := range causes {
			inherited[id] = true
		}
	}
	if id := causes["lifecycle"]; id != 0 {
		inherited[id] = true
	}
	return result, inherited
}

func eventSummary(event queue.Event, before, after queue.State) string {
	switch e := event.(type) {
	case queue.PRObserved:
		return fmt.Sprintf("Observed PR #%d; eligible=%t, trusted=%t", e.PR, e.Eligible, e.Trusted)
	case queue.LifecycleChanged:
		return "Queue lifecycle: " + e.State + ": " + e.Detail
	case queue.OutsidePushRecorded:
		if e.PossibleStaleObservation {
			return fmt.Sprintf("Observed possible rewrite %s → %s; possible stale base observation: may reflect a stale read rather than a real rollback; verify before acknowledgement; no prior OpenMerge verification", e.PreviousSHA, e.NewSHA)
		}
		return fmt.Sprintf("Observed outside push %s → %s; rewrite=%t, ancestry_known=%t, history_known=%t; no prior OpenMerge verification", e.PreviousSHA, e.NewSHA, e.Rewrite, e.AncestryKnown, e.HistoryKnown)
	case queue.BaseMoved:
		return "Observed base branch movement"
	case queue.ConfigApplied:
		commit := e.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if before.Active != nil && (after.Active == nil || after.Active.ID != before.Active.ID) {
			return fmt.Sprintf("Applied configuration from %s; rebuilt the active batch under the new policy", commit)
		}
		return fmt.Sprintf("Applied configuration from %s", commit)
	case queue.EnqueueRequested:
		return fmt.Sprintf("Requested admission for PR #%d", e.PR)
	case queue.DequeueRequested:
		return fmt.Sprintf("Requested dequeue for PR #%d", e.PR)
	case queue.PRClosed:
		return fmt.Sprintf("Observed PR #%d closed", e.PR)
	case queue.PauseRequested:
		return "Queue pause requested (" + e.Reason + "): " + e.Detail
	case queue.ResumeRequested:
		return "Requested clearing generic pauses, build failures and action poison"
	case queue.BlockSourceBlocked:
		return "Queue block (" + string(e.Source) + "): " + e.Detail
	case queue.BlockSourceCleared:
		if before.BlockSourceRevision(e.Source) == after.BlockSourceRevision(e.Source) {
			return "Ignored stale clear for " + string(e.Source)
		}
		return "Queue block cleared (" + string(e.Source) + ")"
	case queue.RulesBlocked:
		return "Repository rules block: " + strings.Join(e.Problems, "; ")
	case queue.RulesCleared:
		return "Repository rules passed; rules block cleared"
	case queue.RulesSourceBlocked:
		return "Repository rules block (" + string(e.Source) + "): " + strings.Join(e.Problems, "; ")
	case queue.RulesSourceCleared:
		return "Repository rules passed; " + string(e.Source) + " block cleared"
	case queue.ActionPoisoned:
		return "Action quarantined after repeated invalid execution: " + e.Reason
	case queue.RefreshRequested:
		return "Requested fresh CI observations"
	case queue.ChainedBatchStarted:
		return fmt.Sprintf("Started chain batch %s at position %d on %s", e.BatchID, e.Position, e.ParentBatchID)
	case queue.ChainedTestRefBuilt:
		return fmt.Sprintf("Recorded chain batch %s: tested %s, landing %s", e.BatchID, e.TestedSHA, e.LandingSHA)
	case queue.BatchesInvalidated:
		return fmt.Sprintf("Invalidated batches %s: %s", strings.Join(e.BatchIDs, ", "), e.Reason)
	case queue.BatchDiagnosed:
		return "Diagnosed oldest batch " + e.BatchID + ": " + string(e.Reason)
	case queue.BatchStarted:
		return "Started constructing a test batch"
	case queue.BatchFailed:
		if before.Active != nil {
			return "Batch failed: " + before.DequeueDetail(before.Active.PRs[0].PR, e.Reason)
		}
		return "Batch failed: " + string(e.Reason)
	case queue.FastForwardBatchSelected:
		return "Selected a consecutive fast-forward chain"
	case queue.FastForwardRefBuilt:
		return "Recorded a unique marker testing the head tree and history"
	case queue.FastForwardBlocked:
		return "Head cannot fast-forward onto the current base; update the branch and queue it again"
	case queue.FastForwardMergeCommitsBlocked:
		return "Base requires linear history; remove merge commits from the branch and queue it again"
	case queue.TestRefBuilt:
		return "Recorded the batch test commit"
	case queue.TestRefBuildFailed:
		if e.Conflict {
			return "Test ref build failed with a merge conflict"
		}
		return "Test ref build failed; retry policy applies"
	case queue.CheckProvenanceObserved:
		if detail := after.CheckProvenanceDetail(); detail != "" {
			return detail
		}
		return "Check provenance diagnostic cleared"
	case queue.ChecksCompleted:
		return fmt.Sprintf("Recorded terminal CI result; success=%t", e.Success)
	case queue.BatchRunsObserved:
		return "Observed attributed batch CI runs: " + checkRunSummary(e.Runs)
	case queue.ChecksObserved:
		if !e.Terminal {
			return "Observed pending retry-aware CI; runs: " + checkRunSummary(e.Runs)
		}
		return fmt.Sprintf("Observed retry-aware terminal CI result; success=%t; runs: %s", e.Success, checkRunSummary(e.Runs))
	case queue.CheckCancellationRequested:
		return fmt.Sprintf("Requested cancellation for retired batch %s (%s); runs: %s", e.BatchID, e.Reason, checkRunSummary(e.Runs))
	case queue.CheckCancellationDeferred:
		if e.Abandoned {
			return "Abandoned cancellation follow-up for batch " + e.BatchID + ": " + e.Detail
		}
		return "Deferred cancellation follow-up for batch " + e.BatchID + ": " + e.Detail
	case queue.CheckCancellationReported:
		if e.Unsupported {
			return "Cancellation unsupported for batch " + e.BatchID + ": " + e.Detail
		}
		if len(e.Confirmed) > 0 {
			return "Confirmed completed or cancelled runs for batch " + e.BatchID + ": " + checkRunSummary(e.Confirmed)
		}
		return "Cancellation requested for batch " + e.BatchID + "; completion unconfirmed"
	case queue.CheckRerunRequested:
		return fmt.Sprintf("Requested bounded retry of %d failed check(s): %s", len(e.Runs), checkRunSummary(e.Runs))
	case queue.CheckRerunDispatchProgress:
		phase := "intent"
		if e.Target.Accepted {
			phase = "acceptance"
		} else if e.Target.NotDispatched {
			phase = "definite refusal"
		}
		return fmt.Sprintf("Recorded Actions rerun %s for workflow %d after attempt %d", phase, e.Target.WorkflowRunID, e.Target.Attempt)
	case queue.CheckRerunDispatched:
		if e.OutcomeUnknown {
			return queue.ActionsPermissionLostDetail
		}
		if e.Unsupported {
			if e.Detail != "" {
				return "Check retry unsupported: " + e.Detail
			}
			return "Check retry unsupported by the forge"
		}
		if e.Detail != "" {
			return "Dispatched bounded check retry: " + e.Detail
		}
		return "Dispatched bounded check retry"
	case queue.ChecksFailureConfirmed:
		detail := string(e.Reason)
		if retry := before.CheckRetryDetail(); retry != "" {
			detail += ": " + retry
		}
		return "Confirmed check failure: " + detail
	case queue.ChecksReopened:
		if before.ChecksPassed(e.TestedSHA) && !after.ChecksPassed(e.TestedSHA) {
			return "CI reopened; previous success revoked"
		}
		if after.ChecksPassed(e.TestedSHA) {
			return "Observed pending CI; landing already started"
		}
		if detail := after.CheckProvenanceDetail(); detail != "" {
			return "CI still pending; " + detail
		}
		return "CI still pending"
	case queue.ChecksRefreshed:
		return "Refreshed the batch CI observations"
	case queue.LandingStarted:
		return "Recorded a landing attempt; outcome awaits fence resolution"
	case queue.Landed:
		return "Confirmed batch landing"
	case queue.LandingAbandoned:
		return "Confirmed landing abandonment through the fence"
	case queue.Dequeued:
		return fmt.Sprintf("Recorded PR #%d dequeue: %s", e.PR, before.DequeueDetail(e.PR, e.Reason))
	default:
		return "Recorded queue event"
	}
}

func checkRunSummary(runs []queue.CheckRun) string {
	if len(runs) == 0 {
		return "none"
	}
	identities := make([]string, 0, len(runs))
	for _, run := range runs {
		identity := fmt.Sprintf("%s/%s#%d.%d=%s", run.Name, run.Source, run.ID, run.Attempt, run.State)
		if run.WorkflowRunID > 0 {
			identity += fmt.Sprintf(" workflow=%d %s", run.WorkflowRunID, run.WorkflowRunURL)
		}
		if run.URL != "" {
			identity += " job=" + run.URL
		}
		identities = append(identities, identity)
	}
	return strings.Join(identities, ", ")
}

func affects(event queue.Event, pr int, before, after queue.State, tested map[string]bool) bool {
	before.Active = before.BatchForPR(pr)
	before.Following = nil
	after.Active = after.BatchForPR(pr)
	after.Following = nil
	if n := directPR(event); n > 0 {
		return n == pr
	}
	switch e := event.(type) {
	case queue.BlockSourceBlocked:
		return before.BlockSourceRevision(e.Source) != after.BlockSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.BlockSourceCleared:
		return before.BlockSourceRevision(e.Source) != after.BlockSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.RulesBlocked, queue.RulesCleared:
		return before.RulesRevision() != after.RulesRevision() && (entryIn(before, pr) || entryIn(after, pr))
	case queue.RulesSourceBlocked:
		return before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.RulesSourceCleared:
		return before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.ChainedBatchStarted:
		return slices.ContainsFunc(e.PRs, func(p queue.BatchPR) bool { return p.PR == pr })
	case queue.ChainedTestRefBuilt:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.BatchesInvalidated:
		return inBatch(before, pr) && slices.Contains(e.BatchIDs, before.Active.ID)
	case queue.BatchDiagnosed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.BatchStarted:
		return slices.ContainsFunc(e.PRs, func(p queue.BatchPR) bool { return p.PR == pr })
	case queue.ChecksCompleted:
		return tested[e.TestedSHA]
	case queue.CheckCancellationRequested:
		return tested[e.TestedSHA]
	case queue.CheckCancellationDeferred:
		return tested[e.TestedSHA]
	case queue.CheckCancellationReported:
		return tested[e.TestedSHA]
	case queue.BatchRunsObserved:
		return tested[e.TestedSHA]
	case queue.ChecksObserved:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.CheckRerunRequested:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.CheckRerunDispatchProgress:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.CheckRerunDispatched:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.ChecksFailureConfirmed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.ChecksReopened:
		return tested[e.TestedSHA]
	case queue.CheckProvenanceObserved:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.ChecksRefreshed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
	case queue.FastForwardBatchSelected:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.FastForwardRefBuilt:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.FastForwardBlocked:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.FastForwardMergeCommitsBlocked:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.TestRefBuilt:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.TestRefBuildFailed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.BatchFailed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.LandingStarted:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.Landed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.LandingAbandoned:
		return inBatch(before, pr) && before.Active.ID == e.BatchID
	case queue.ActionCompleted:
		return false // transport acknowledgements do not explain queue decisions
	default:
		return entryIn(before, pr) || entryIn(after, pr)
	}
}

// Readiness is a process-local admission gate, separate from durable blocks.
type Readiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

// Identified failures on later batches have not yet consumed their head-only
// rerun. Expose that distinction alongside the raw check retry state.
func provisionalFailure(b *queue.Batch) string {
	if b.ProvisionalFailure != "" {
		return string(b.ProvisionalFailure)
	}
	if b.CheckRetry != nil && !b.CheckRetry.Confirmed && b.CheckRetry.RerunState == "failed" {
		return string(queue.ChecksFailed)
	}
	return ""
}

// FailureEstimate exposes the exact planner inputs; Rate is display-only.
type FailureEstimate struct {
	Failures       int     `json:"failures"`
	Samples        int     `json:"samples"`
	Rate           float64 `json:"rate"`
	TargetPassRate float64 `json:"target_pass_rate,omitempty"`
}
