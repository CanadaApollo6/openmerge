package status

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ProjectDependencies is read-only. Full replay deliberately keeps diagnostics out
// of authorization snapshots; missing identity or evidence fails closed.
func ProjectDependencies(j store.Journal, binding dependency.JournalInitialized, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord) (store.Journal, error) {
	records := map[uint64][]byte{}
	for _, e := range j.Events {
		records[uint64(e.ID)] = e.Payload
	}
	reader := journalreader.New(j.Seed, binding, func(id uint64) ([]byte, error) {
		raw, ok := records[id]
		if !ok {
			return nil, journalreader.ErrReference
		}
		return raw, nil
	})
	reader.WithChangeIdentity(func(pr int) (queue.ChangeID, error) { return j.Key.ChangeID(pr) })
	diagnostics := dependency.DiagnosticProjection{}
	sourceAccess := map[string]bool{}
	for _, record := range j.Events {
		decoded, err := reader.Apply(uint64(record.ID), record.Payload)
		if err != nil {
			return j, errors.New("dependency status unavailable")
		}
		if d, ok := decoded.Dependency.(dependency.DependenciesDeclared); ok {
			for _, edge := range d.Edges {
				if _, seen := sourceAccess[edge.Reference]; !seen {
					sourceAccess[edge.Reference] = false
				}
			}
		}
		if d, ok := decoded.Dependency.(dependency.DiagnosticRecorded); ok {
			ref, err := dependency.ParseReference(d.Evidence.Observation.Source, dependency.Scope{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, JournalNamespace: binding.Namespace})
			if err != nil {
				continue
			}
			source, found := sources[ref.ChangeID]
			if !found || source.Alias != d.Source || source.Reference.Scope != ref.Scope || source.Reference.ChangeID != ref.ChangeID || source.Reference.Revision != 0 {
				continue
			}
			sourceAccess[d.Evidence.Observation.Source] = d.Scoped().View(audience[d.Evidence.Observation.Source]).Code != ""
			state, err := reader.Dependencies()
			if err != nil {
				return j, errors.New("dependency status unavailable")
			}
			diagnostics, err = diagnostics.Apply(state, uint64(record.ID), d)
			if err != nil {
				return j, errors.New("dependency status unavailable")
			}
		}
	}
	state, err := reader.Dependencies()
	if err != nil {
		return j, errors.New("dependency status unavailable")
	}
	composed := reader.Queue
	j.DependencyQueue = &composed
	j.DependencyRedacted = false
	j.DependencyStatus = map[int]store.DependencyStatus{}
	for _, allowed := range sourceAccess {
		if !allowed {
			j.DependencyRedacted = true
		}
	}
	for pr, rev := range reader.Queue.ChangeRevisions() {
		id, err := j.Key.ChangeID(pr)
		if err != nil {
			return j, errors.New("dependency status unavailable")
		}
		v, ok := state.Status(string(id), uint64(rev.Revision), diagnostics, audience)
		if !ok {
			continue
		}
		if v.Redacted {
			j.DependencyRedacted = true
		}
		item := store.DependencyStatus{Text: v.Text, Details: v.Details, URL: v.URL, Redacted: v.Redacted, Blocking: v.Blocking}
		for _, d := range v.Diagnostics {
			item.Diagnostics = append(item.Diagnostics, store.DependencyDiagnostic{Code: d.Code, Source: d.Source})
		}
		j.DependencyStatus[pr] = item
	}
	return j, nil
}

// maskDependencyEntry constructs the complete public entry from an allowlist.
// Every queue member can be influenced by a private dependency through batching.
// Admission identity, public terminal outcomes and independent admission gates are
// retained; batch progress, CI detail and newly added fields default to absent.
// LandingStarted precedes any forge call, so an unconfirmed landing is batch
// progress too: only a confirmed Landed (status merged) is public.
func maskDependencyEntry(e *Entry) {
	view := Entry{
		PR: e.PR, HeadSHA: e.HeadSHA, Position: e.Position, AdmittedAt: e.AdmittedAt,
		Status: e.Status, Reason: e.Reason, LastDequeueReason: e.LastDequeueReason,
		Change: e.Change, Dependency: e.Dependency, dependencyWaiting: e.dependencyWaiting,
	}
	if e.Landing != nil {
		view.Landing = &LandingView{Outcome: e.Landing.Outcome, SHA: e.Landing.SHA, EventID: e.Landing.EventID}
	}
	switch e.Status {
	case "queued", "building", "testing", "invalidating", "landing":
		view.Status, view.Reason = "queued", ""
	case "waiting-entry":
		view.Detail = e.Detail
	case "dequeue-pending":
		view.Gate = "dequeue"
	}
	*e = view
}

func maskDependencyBatches(q *Queue, j store.Journal) {
	if !j.DependencyRedacted {
		return
	}
	// No batch presentation fields are public while dependency evidence is
	// redacted. Confirmed settlement is projected independently on entries.
	// Constructing the empty projection also keeps future fields private.
	q.Chain = nil
	q.ActiveBatch = nil
	q.Invalidations = nil
}

// DependencyCheckStatus applies the same private hold to the entire check,
// including the existing prefix, before any driver publishes it.
func DependencyCheckStatus(q *Queue, e *Entry, previous forge.Status) forge.Status {
	if !q.dependencyRedacted {
		return previous
	}
	if e == nil {
		return forge.Status{State: forge.Queued, Summary: "Waiting for queue status"}
	}
	state := forge.Queued
	switch e.Status {
	case "merged":
		state = forge.Merged
	case "closed", "dequeued":
		state = forge.Dequeued
	case "blocked":
		state = forge.Blocked
	case "paused":
		state = forge.Paused
	}
	r := deriveWaitingReason(q, *e, false)
	reason := string(r.Code)
	if state == forge.Dequeued || state == forge.Blocked || state == forge.Paused || e.Status == "dequeue-pending" {
		reason = e.Reason
	}
	return forge.Status{State: state, Reason: reason, Summary: WaitingSummary(r)}
}

// RequireDependencyDecisionScope withholds retained positions that could expose
// private history. Authorized paging keeps its existing response contract.
func RequireDependencyDecisionScope(j store.Journal) error {
	if j.DependencyRedacted {
		return errors.New("dependency evidence unavailable")
	}
	return nil
}
