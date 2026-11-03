package cidoctor

import (
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type push struct {
	sha, ref string
	at       time.Time
	checks   map[uint64]bool
	retries  map[retryIdentity]time.Time
}

type retryIdentity struct {
	id, attempt uint64
	job         string
}

type retrySample struct {
	id  uint64
	job string
}

func attemptKey(id, attempt uint64, name string) string {
	return fmt.Sprintf("%d/%d/%s", id, attempt, name)
}

// Replay only the closed prefix. Push records survive invalidation and late
// completion; only identified, accepted same-SHA retries support retry advice.
func journalPushes(j store.Journal, w Window) []*push {
	state := j.Seed
	byBatch := map[string]*push{}
	requests := map[string]queue.CheckRerunRequested{}
	var pushes []*push
	for _, rec := range j.Events {
		if !rec.RecordedAt.Before(w.Until) {
			break
		}
		before := state
		state = queue.Apply(state, rec.Event)
		var id, sha string
		var at time.Time
		switch e := rec.Event.(type) {
		case queue.TestRefBuilt:
			id, sha, at = e.BatchID, e.TestedSHA, e.At
		case queue.ChainedTestRefBuilt:
			id, sha, at = e.BatchID, e.TestedSHA, e.At
		case queue.ChecksObserved:
			if p := byBatch[e.BatchID]; p != nil && p.sha == e.TestedSHA {
				for _, c := range e.Runs {
					if c.WorkflowRunID != 0 {
						p.checks[c.WorkflowRunID] = true
					}
				}
			}
		case queue.CheckRerunRequested:
			b := state.Batch(e.BatchID)
			old := before.Batch(e.BatchID)
			if b != nil && b.TestedSHA == e.TestedSHA && b.CheckRetry != nil && b.CheckRetry.Requested && b.CheckRetry.Generation == e.Generation && b.CheckRetry.RequestedAt.Equal(e.At) && (old == nil || old.CheckRetry == nil || !old.CheckRetry.Requested) {
				requests[e.BatchID] = e
			}
		case queue.CheckRerunDispatchProgress:
			if e.Validate() == nil && e.Target.Accepted && requests[e.BatchID].Generation == e.Generation {
				recordRetry(byBatch[e.BatchID], requests[e.BatchID], e.TestedSHA, e.Target.WorkflowRunID, e.Target.Attempt)
			}
		case queue.CheckRerunDispatched:
			if e.Validate() == nil && !e.Unsupported && !e.OutcomeUnknown && requests[e.BatchID].Generation == e.Generation {
				req := requests[e.BatchID]
				for _, c := range req.Runs {
					recordRetry(byBatch[e.BatchID], req, e.TestedSHA, c.WorkflowRunID, c.Attempt)
				}
			}
		}
		if id == "" || sha == "" {
			continue
		}
		if p := byBatch[id]; p != nil && p.sha == sha {
			continue
		}
		p := &push{sha: sha, ref: "openmerge/queue/" + j.Key.BaseBranch + "/" + id, at: at, checks: map[uint64]bool{}, retries: map[retryIdentity]time.Time{}}
		byBatch[id] = p
		// Unknown event time is retained for non-dispatch diagnostics. Selection
		// uses recording time, while dispatch always requires the real push time.
		if !rec.RecordedAt.Before(w.Since) {
			pushes = append(pushes, p)
		}
	}
	return pushes
}

func recordRetry(p *push, req queue.CheckRerunRequested, sha string, id, attempt uint64) {
	if p == nil || req.TestedSHA != sha || p.sha != sha || id == 0 || attempt == 0 {
		return
	}
	for _, c := range req.Runs {
		if c.WorkflowRunID == id && c.Attempt == attempt {
			p.retries[retryIdentity{id, attempt + 1, c.Name}] = req.At
		}
	}
}
