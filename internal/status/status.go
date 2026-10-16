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

// Response carries exactly one of queues, queue, or queue + PR + why.
type Response struct {
	Version int     `json:"version"`
	Queues  []Queue `json:"queues,omitempty"`
	Queue   *Queue  `json:"queue,omitempty"`
	PR      *Entry  `json:"pr,omitempty"`
	Why     *Why    `json:"why,omitempty"`
}

// Queue describes one served installation/repository/base/lane partition.
type Queue struct {
	Installation string        `json:"installation"`
	Repository   string        `json:"repo"`
	Base         string        `json:"base"`
	Lane         string        `json:"lane"`
	Watermark    int64         `json:"watermark"`
	BaseSHA      string        `json:"base_sha"`
	Paused       bool          `json:"paused"`
	PauseReason  string        `json:"pause_reason"`
	PauseDetail  string        `json:"pause_detail"`
	Blocks       []Block       `json:"blocks"`
	ActiveBatch  *Batch        `json:"active_batch"`
	Entries      []Entry       `json:"entries"`
	RulesSources []RulesSource `json:"rules_sources,omitempty"`
}

// RulesSource lists one source's independently revisioned policy findings.
type RulesSource struct {
	Source   string   `json:"source"`
	Problems []string `json:"problems"`
	Revision uint64   `json:"revision"`
}

// Block describes one active cause preventing new work; multiple may coexist.
type Block struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// Batch pins the tested inputs and the operation currently awaiting an outcome.
type Batch struct {
	ID        string    `json:"id"`
	Phase     string    `json:"phase"`
	BaseSHA   string    `json:"base_sha"`
	TestedSHA string    `json:"tested_sha"`
	TestRef   string    `json:"test_ref"`
	Method    string    `json:"method"`
	PRs       []BatchPR `json:"prs"`
}

// BatchPR identifies an exact head in a batch.
type BatchPR struct {
	PR      int    `json:"pr"`
	HeadSHA string `json:"head_sha"`
}

// Entry uses position zero for PRs outside the queue; admission is the latest
// successful admission, retained on a terminal PR detail response.
type Entry struct {
	Position          int        `json:"position"`
	PR                int        `json:"pr"`
	HeadSHA           string     `json:"head_sha"`
	AdmittedAt        *time.Time `json:"admitted_at"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	LastDequeueReason string     `json:"last_dequeue_reason"`
}

// Why links a current-state explanation to recent committed evidence.
type Why struct {
	Summary string  `json:"summary"`
	Limit   int     `json:"limit"`
	Events  []Event `json:"events"`
}

// Event exposes stable evidence metadata without the private persistence payload.
type Event struct {
	ID      int64     `json:"id"`
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
}

// Project replays committed history. A positive PR selects its explanation;
// queue lists retain only admitted entries. Stale result events never invent
// outcomes: terminal states require an admission to actually leave the fold.
func Project(j store.Journal, pr, limit int, sanitizer *diagnostic.Sanitizer) (Queue, *Entry, *Why) {
	s := j.Seed
	known := make(map[int]Entry)
	heads := make(map[int]string)
	tested, inheritedPause := explanationContext(j, pr)
	events := make([]Event, 0)
	var watermark int64
	for _, record := range j.Events {
		before := s
		s = queue.Apply(s, record.Event)
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
					e.Status, e.Reason = "not-queued", ""
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
				}
			}
			known[n] = e
		}
		if _, landed := record.Event.(queue.Landed); landed && before.Active != nil {
			for _, pin := range before.Active.PRs {
				if entryIn(before, pin.PR) && !entryIn(s, pin.PR) {
					e := known[pin.PR]
					e.Status, e.Reason, e.HeadSHA = "merged", "", pin.HeadSHA
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
			events = append(events, Event{record.ID, kind, record.RecordedAt.UTC(), sanitizer.Sanitize(eventSummary(record.Event))})
			if len(events) > limit {
				events = slices.Delete(events, 0, 1)
			}
		}
	}
	for n, head := range heads {
		e := known[n]
		e.HeadSHA = head
		known[n] = e
	}
	q := Queue{Installation: j.Key.Installation, Repository: j.Key.Repository, Base: j.Key.BaseBranch, Lane: j.Key.Lane,
		Watermark: watermark, BaseSHA: s.BaseSHA, PauseReason: sanitizer.Sanitize(s.PauseReason(queue.Config{})), PauseDetail: sanitizer.Sanitize(s.PauseDetail(queue.Config{})), Blocks: make([]Block, 0), Entries: make([]Entry, 0, len(s.Entries))}
	for _, block := range s.Blocks(queue.Config{}) {
		q.Blocks = append(q.Blocks, Block{sanitizer.Sanitize(block.Reason), sanitizer.Sanitize(block.Detail)})
	}
	for _, source := range s.RulesSources() {
		problems := make([]string, 0, len(source.Problems))
		for _, problem := range source.Problems {
			problems = append(problems, sanitizer.Sanitize(problem))
		}
		q.RulesSources = append(q.RulesSources, RulesSource{string(source.Source), problems, source.Revision})
	}
	q.Paused = len(q.Blocks) > 0
	if b := s.Active; b != nil {
		q.ActiveBatch = &Batch{ID: b.ID, Phase: string(b.Phase), BaseSHA: b.BaseSHA, TestedSHA: b.TestedSHA, TestRef: b.TestRef, Method: string(b.Method), PRs: make([]BatchPR, 0, len(b.PRs))}
		for _, pin := range b.PRs {
			q.ActiveBatch.PRs = append(q.ActiveBatch.PRs, BatchPR{pin.PR, pin.HeadSHA})
		}
	}
	for i, admitted := range s.Entries {
		e := known[admitted.PR]
		e.HeadSHA = admitted.HeadSHA
		e.Position, e.Status, e.Reason = i+1, "queued", ""
		if inBatch(s, e.PR) {
			e.Status = string(s.Active.Phase)
		}
		if reason := s.PendingDequeue(e.PR); reason != "" {
			e.Status, e.Reason = "dequeue-pending", string(reason)
		}
		if q.Paused {
			e.Status, e.Reason = "paused", q.PauseReason
		}
		q.Entries = append(q.Entries, e)
		known[e.PR] = e
	}
	e, ok := known[pr]
	if !ok {
		return q, nil, nil
	}
	summary := fmt.Sprintf("PR #%d: %s", pr, e.Status)
	if e.Reason != "" {
		summary += " (" + e.Reason + ")"
	}
	if q.Paused && e.Position > 0 {
		for _, block := range q.Blocks {
			summary += "; " + block.Reason + ": " + block.Detail
		}
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
	return q, &e, &Why{Summary: summary, Limit: limit, Events: events}
}

func directPR(event queue.Event) int {
	switch e := event.(type) {
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
	return s.Active != nil && slices.ContainsFunc(s.Active.PRs, func(p queue.BatchPR) bool { return p.PR == pr })
}

// Correlate CI that arrived before TestRefBuilt with the batch that ultimately
// consumed it. Looking only at the state at arrival would lose retained evidence.
func explanationContext(j store.Journal, pr int) (map[string]bool, map[int64]bool) {
	result := make(map[string]bool)
	causes := make(map[string]int64)
	inherited := make(map[int64]bool)
	if pr == 0 {
		return result, inherited
	}
	s := j.Seed
	for _, record := range j.Events {
		before := s
		s = queue.Apply(s, record.Event)
		switch e := record.Event.(type) {
		case queue.PauseRequested:
			causes["pause"] = record.ID
		case queue.ResumeRequested:
			delete(causes, "pause")
			delete(causes, "poison")
		case queue.ActionPoisoned:
			if causes["poison"] == 0 {
				causes["poison"] = record.ID
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
		if inBatch(s, pr) && s.Active.TestedSHA != "" {
			result[s.Active.TestedSHA] = true
		}
	}
	if entryIn(s, pr) {
		for _, id := range causes {
			inherited[id] = true
		}
	}
	return result, inherited
}

func eventSummary(event queue.Event) string {
	switch e := event.(type) {
	case queue.PRObserved:
		return fmt.Sprintf("Observed PR #%d; eligible=%t, trusted=%t", e.PR, e.Eligible, e.Trusted)
	case queue.BaseMoved:
		return "Observed base branch movement"
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
	case queue.BatchStarted:
		return "Started constructing a test batch"
	case queue.BatchFailed:
		return "Batch failed: " + string(e.Reason)
	case queue.TestRefBuilt:
		return "Recorded the batch test commit"
	case queue.TestRefBuildFailed:
		if e.Conflict {
			return "Test ref build failed with a merge conflict"
		}
		return "Test ref build failed; retry policy applies"
	case queue.ChecksCompleted:
		return fmt.Sprintf("Recorded terminal CI result; success=%t", e.Success)
	case queue.ChecksReopened:
		return "CI reopened; previous success revoked"
	case queue.ChecksRefreshed:
		return "Refreshed the batch CI observations"
	case queue.LandingStarted:
		return "Recorded a landing attempt; outcome awaits fence resolution"
	case queue.Landed:
		return "Confirmed batch landing"
	case queue.LandingAbandoned:
		return "Confirmed landing abandonment through the fence"
	case queue.Dequeued:
		return fmt.Sprintf("Recorded PR #%d dequeue: %s", e.PR, e.Reason)
	default:
		return "Recorded queue event"
	}
}

func affects(event queue.Event, pr int, before, after queue.State, tested map[string]bool) bool {
	if n := directPR(event); n > 0 {
		return n == pr
	}
	switch e := event.(type) {
	case queue.RulesBlocked, queue.RulesCleared:
		return before.RulesRevision() != after.RulesRevision() && (entryIn(before, pr) || entryIn(after, pr))
	case queue.RulesSourceBlocked:
		return before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.RulesSourceCleared:
		return before.RulesSourceRevision(e.Source) != after.RulesSourceRevision(e.Source) && (entryIn(before, pr) || entryIn(after, pr))
	case queue.BatchStarted:
		return slices.ContainsFunc(e.PRs, func(p queue.BatchPR) bool { return p.PR == pr })
	case queue.ChecksCompleted:
		return tested[e.TestedSHA]
	case queue.ChecksReopened:
		return tested[e.TestedSHA]
	case queue.ChecksRefreshed:
		return inBatch(before, pr) && before.Active.ID == e.BatchID && before.Active.TestedSHA == e.TestedSHA
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
