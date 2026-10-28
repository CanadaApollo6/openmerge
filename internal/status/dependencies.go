package status

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"slices"
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

// A private source must not turn its consumer's unstarted composition into a
// success signal. Keep B's own terminal, settlement and admission gates visible.
func maskDependencyEntry(e *Entry) {
	if !e.dependencyWaiting {
		return
	}
	switch e.Status {
	case "queued", "building", "testing", "invalidating":
		e.Status = "queued"
		e.Reason, e.Detail, e.Gate, e.BatchID = "", "", "", ""
		e.BatchPosition = 0
		e.BatchesAhead = nil
		e.RunLedger = nil
	}
}

func maskDependencyBatches(q *Queue, j store.Journal) {
	if !j.DependencyRedacted {
		return
	}
	q.Chain = slices.DeleteFunc(q.Chain, func(b *Batch) bool { return b.Phase != "landing" })
	if q.ActiveBatch != nil && q.ActiveBatch.Phase != "landing" {
		q.ActiveBatch = nil
	}
	q.Invalidations = slices.DeleteFunc(q.Invalidations, func(b queue.BatchesInvalidated) bool { return b.Reason == "dependency-changed" })
}

// DependencyCheckStatus applies the same private hold to the entire check,
// including the existing prefix, before any driver publishes it.
func DependencyCheckStatus(q *Queue, e *Entry, previous forge.Status) forge.Status {
	if e == nil || !e.dependencyRedacted || e.Status != "queued" {
		return previous
	}
	r := deriveWaitingReason(q, *e, false)
	return forge.Status{State: forge.Queued, Reason: string(r.Code), Summary: WaitingSummary(r)}
}

// RequireDependencyDecisionScope withholds retained positions that could expose
// private history. Authorized paging keeps its existing response contract.
func RequireDependencyDecisionScope(j store.Journal) error {
 if j.DependencyRedacted {return errors.New("dependency evidence unavailable")}
 return nil
}
