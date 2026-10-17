package status

import (
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Run is historical evidence, independent of the bounded planner snapshot.
// A nil identity honestly represents aggregate legacy CI, not an invented run.
type Run struct {
	BatchID            string          `json:"batch_id"`
	TestedSHA          string          `json:"tested_sha"`
	TestRef            string          `json:"test_ref"`
	Identity           *queue.CheckRun `json:"identity,omitempty"`
	Outcome            string          `json:"outcome"`
	WastedReason       string          `json:"wasted_reason,omitempty"`
	Cancellation       string          `json:"cancellation,omitempty"`
	CancellationDetail string          `json:"cancellation_detail,omitempty"`
	producerKey        string
	identityKey        string
}

type ledgerBatch struct {
	id, sha, ref string
	members      []int
	runs         []Run
	invalidation string
}

func runKey(run queue.CheckRun) string {
	return fmt.Sprintf("%s/%s/%d/%d/%d", run.Source, run.Name, run.ID, run.WorkflowRunID, run.Attempt)
}

func runLedger(j store.Journal, sanitizer *diagnostic.Sanitizer) map[int][]Run {
	batches := map[string]*ledgerBatch{}
	order := []string{}
	bySHA := map[string]string{}
	s := j.Seed
	remember := func(b *queue.Batch) {
		v := batches[b.ID]
		if v == nil {
			v = &ledgerBatch{id: b.ID, sha: b.TestedSHA, ref: b.TestRef}
			batches[b.ID] = v
			order = append(order, b.ID)
		}
		// Fast-forward selection may narrow an admitted batch before CI starts.
		v.members = nil
		if parent := batches[b.ParentBatchID]; parent != nil {
			v.members = slices.Clone(parent.members)
		}
		for _, pin := range b.PRs {
			if !slices.Contains(v.members, pin.PR) {
				v.members = append(v.members, pin.PR)
			}
		}
		if b.TestedSHA != "" {
			bySHA[b.TestedSHA] = b.ID
		}
	}
	for _, b := range s.Batches() {
		remember(b)
	}
	// Resolve accepted batch inputs first, including checks delivered before the
	// test-ref write is committed. Membership survives landing and invalidation.
	for _, record := range j.Events {
		before := s
		s = queue.Apply(s, record.Event)
		// A retained legacy failure can consume the batch during ref-built.
		// Retain those tested inputs even though the resulting fold is idle.
		var built queue.TestRefBuilt
		switch e := record.Event.(type) {
		case queue.TestRefBuilt:
			built = e
		case queue.FastForwardRefBuilt:
			built = queue.TestRefBuilt{BatchID: e.BatchID, TestedSHA: e.TestedSHA, At: e.At, Method: queue.FastForward}
		}
		if b := before.Batch(built.BatchID); b != nil && b.Phase == queue.Building && s.Batch(b.ID) == nil && built.TestedSHA != "" {
			v := batches[b.ID]
			v.sha, v.ref = built.TestedSHA, b.TestRef
			bySHA[built.TestedSHA] = b.ID
		}
		for _, b := range s.Batches() {
			remember(b)
			if b.TestedSHA != "" {
				v := batches[b.ID]
				v.sha, v.ref = b.TestedSHA, b.TestRef
				bySHA[b.TestedSHA] = b.ID
			}
		}
	}
	s = j.Seed
	observe := func(b *ledgerBatch, runs []queue.CheckRun) {
		if b == nil {
			return
		}
		// Identified evidence replaces an aggregate placeholder for the same commit.
		b.runs = slices.DeleteFunc(b.runs, func(r Run) bool { return r.Identity == nil })
		for _, run := range runs {
			key := runKey(run)
			producerKey := run.Source + "\x00" + run.Name
			i := slices.IndexFunc(b.runs, func(r Run) bool { return r.Identity != nil && r.identityKey == runKey(run) })
			run.Name = sanitizer.Sanitize(run.Name)
			run.Source = sanitizer.Sanitize(run.Source)
			run.URL = sanitizer.Sanitize(run.URL)
			run.WorkflowRunURL = sanitizer.Sanitize(run.WorkflowRunURL)
			run.RetryUnsupported = sanitizer.Sanitize(run.RetryUnsupported)
			row := Run{BatchID: b.id, TestedSHA: b.sha, TestRef: b.ref, Identity: &run, Outcome: sanitizer.Sanitize(run.State), WastedReason: b.invalidation, identityKey: key, producerKey: producerKey}
			if row.Outcome == "" {
				row.Outcome = "pending"
			}
			for j := range b.runs {
				old := &b.runs[j]
				if old.Identity == nil || old.producerKey != producerKey {
					continue
				}
				previous := old.Identity
				if previous.ID < run.ID || previous.ID == run.ID && max(previous.Attempt, 1) < max(run.Attempt, 1) {
					if old.WastedReason == "" {
						old.WastedReason = "rerun"
					}
				} else if run.ID < previous.ID || run.ID == previous.ID && max(run.Attempt, 1) < max(previous.Attempt, 1) {
					if row.WastedReason == "" {
						row.WastedReason = "rerun"
					}
				}
			}
			if i < 0 {
				b.runs = append(b.runs, row)
			} else {
				old := b.runs[i]
				if old.Outcome != "pending" && row.Outcome == "pending" {
					continue
				}
				row.WastedReason = old.WastedReason
				row.Cancellation = old.Cancellation
				row.CancellationDetail = old.CancellationDetail
				b.runs[i] = row
			}
		}
	}
	waste := func(b *ledgerBatch, reason string) {
		if b != nil {
			b.invalidation = reason
			for i := range b.runs {
				if b.runs[i].WastedReason == "" {
					b.runs[i].WastedReason = reason
				}
			}
		}
	}
	for _, record := range j.Events {
		before := s
		s = queue.Apply(s, record.Event)
		for _, b := range s.Batches() {
			remember(b)
			v := batches[b.ID]
			if b.TestedSHA != "" {
				v.sha, v.ref = b.TestedSHA, b.TestRef
				bySHA[b.TestedSHA] = b.ID
			}
		}
		for _, b := range s.Batches() {
			old := before.Batch(b.ID)
			if b.InvalidationReason == "" || old == nil || old.InvalidationReason != "" {
				continue
			}
			reason := b.InvalidationReason
			switch b.InvalidationReason {
			case "predecessor-failed":
				if before.Active != nil {
					reason = "invalidated behind failure " + before.Active.ID
				}
			case "predecessor-abandoned":
				if before.Active != nil {
					reason = "predecessor abandoned " + before.Active.ID
				}
			case "change-left-queue":
				switch e := record.Event.(type) {
				case queue.PRObserved:
					reason = fmt.Sprintf("eligibility or trust changed for PR #%d", e.PR)
					for _, entry := range before.Entries {
						if entry.PR == e.PR && entry.HeadSHA != e.HeadSHA {
							reason = fmt.Sprintf("superseded by a new head for PR #%d", e.PR)
						}
					}
				case queue.DequeueRequested:
					reason = fmt.Sprintf("dequeue of PR #%d", e.PR)
				case queue.PRClosed:
					reason = fmt.Sprintf("closed PR #%d", e.PR)
				}
			}
			waste(batches[b.ID], reason)
		}
		switch e := record.Event.(type) {
		case queue.BatchRunsObserved:
			b := batches[e.BatchID]
			if b != nil && b.sha == e.TestedSHA {
				observe(b, e.Runs)
			}
		case queue.ChecksObserved:
			b := batches[e.BatchID]
			if b != nil && b.sha == e.TestedSHA {
				observe(b, e.Runs)
			}
		case queue.ChecksCompleted:
			b := batches[bySHA[e.TestedSHA]]
			if b != nil {
				outcome := "failure"
				if e.Success {
					outcome = "success"
				}
				if len(b.runs) == 0 {
					b.runs = append(b.runs, Run{BatchID: b.id, TestedSHA: b.sha, TestRef: b.ref, Outcome: outcome, WastedReason: b.invalidation})
				} else if len(b.runs) == 1 && b.runs[0].Identity == nil {
					b.runs[0].Outcome = outcome
				}
			}
		case queue.CheckRerunRequested:
			b := batches[e.BatchID]
			if b != nil && b.sha == e.TestedSHA {
				observe(b, e.Runs)
				for i := range b.runs {
					for _, run := range e.Runs {
						if b.runs[i].Identity != nil && b.runs[i].identityKey == runKey(run) {
							b.runs[i].WastedReason = "rerun"
						}
					}
				}
			}
		case queue.BatchesInvalidated:
			for _, id := range e.BatchIDs {
				b := batches[id]
				if b != nil && b.invalidation == "" {
					waste(b, e.Reason)
				}
			}
		case queue.BatchFailed:
			if e.Reason == queue.ChecksTimeout {
				waste(batches[e.BatchID], "timeout")
			}
		case queue.ChecksFailureConfirmed:
			if e.Reason == queue.ChecksTimeout {
				waste(batches[e.BatchID], "timeout")
			}
		case queue.CheckCancellationRequested:
			b := batches[e.BatchID]
			if b != nil && b.sha == e.TestedSHA {
				observe(b, e.Runs)
				for i := range b.runs {
					for _, run := range e.Runs {
						if b.runs[i].Identity != nil && b.runs[i].identityKey == runKey(run) {
							b.runs[i].Cancellation = "requested"
						}
					}
				}
			}
		case queue.CheckCancellationReported:
			b := batches[e.BatchID]
			if b != nil && b.sha == e.TestedSHA {
				for i := range b.runs {
					r := &b.runs[i]
					if r.Cancellation != "requested" {
						continue
					}
					r.CancellationDetail = sanitizer.Sanitize(e.Detail)
					if e.Unsupported {
						r.Cancellation = "unsupported"
					}
					for _, run := range e.Confirmed {
						if r.Identity != nil && r.identityKey == runKey(run) {
							r.Cancellation = "confirmed"
							r.Outcome = run.State
							if r.Outcome == "cancelled" {
								r.WastedReason = "cancelled: " + b.invalidation
							}
						}
					}
				}
			}
		}
		// Legacy invalidations have no separate event. Explain their historical CI
		// without adding any event or action to the compatible depth-one protocol.
		for _, old := range before.Batches() {
			if s.Batch(old.ID) == nil {
				switch e := record.Event.(type) {
				case queue.ChecksCompleted:
					if !e.Success && len(old.PRs) > 1 {
						waste(batches[old.ID], "confirmed failure (bisection)")
					}
				case queue.ChecksFailureConfirmed:
					if e.Reason == queue.ChecksFailed && len(old.PRs) > 1 {
						waste(batches[old.ID], "confirmed failure (bisection)")
					}
				case queue.BatchFailed:
					if e.Reason == queue.ChecksFailed && len(old.PRs) > 1 {
						waste(batches[old.ID], "confirmed failure (bisection)")
					}
				case queue.PRObserved:
					reason := "eligibility or trust changed"
					for _, entry := range before.Entries {
						if entry.PR == e.PR && entry.HeadSHA != e.HeadSHA {
							reason = "superseded by a new head"
						}
					}
					waste(batches[old.ID], reason)
				case queue.DequeueRequested:
					waste(batches[old.ID], "dequeue")
				case queue.PRClosed:
					waste(batches[old.ID], "closed")
				case queue.ConfigApplied:
					waste(batches[old.ID], "config-changed")
				case queue.BaseMoved:
					waste(batches[old.ID], "base-moved")
				case queue.Dequeued:
					if e.Reason == queue.ChecksTimeout {
						waste(batches[old.ID], "timeout")
					}
				}
			}
		}
	}
	result := map[int][]Run{}
	for _, id := range order {
		b := batches[id]
		for _, pr := range b.members {
			result[pr] = append(result[pr], b.runs...)
		}
	}
	return result
}

// LedgerEntry selects summary evidence without replaying the journal for every
// PR. Terminal changes retain historical runs after leaving Entries.
func (q *Queue) LedgerEntry(pr int) *Entry {
	for _, entry := range q.Entries {
		if entry.PR == pr {
			return &entry
		}
	}
	if runs := q.runLedger[pr]; len(runs) > 0 {
		return &Entry{PR: pr, RunLedger: runs}
	}
	return nil
}
