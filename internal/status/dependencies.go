package status

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/audit"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// DependencyOptions enables authorized-only presentation that needs a clock.
// The zero value keeps the slice 5-1 projection byte-identical.
type DependencyOptions struct {
	// Now is supplied by the caller: rendering reads no clock or forge state.
	Now time.Time
	// WaitAge flags authorized dependency waits at least this old; zero disables it.
	WaitAge time.Duration
}

// dependencyWait is a change revision's current continuous dependency wait.
type dependencyWait struct {
	revision uint64
	since    time.Time
}

// ProjectDependencies is read-only. Full replay deliberately keeps diagnostics out
// of authorization snapshots; missing identity or evidence fails closed.
func ProjectDependencies(j store.Journal, binding dependency.JournalInitialized, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord) (store.Journal, error) {
	return ProjectDependenciesAt(j, binding, audience, sources, DependencyOptions{})
}

// ProjectDependenciesAt also derives the authorized wait-age flag from recorded
// journal times. Generic views never carry it: a wait can start or end at a
// private source transition, so its age is source evidence.
func ProjectDependenciesAt(j store.Journal, binding dependency.JournalInitialized, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord, opts DependencyOptions) (store.Journal, error) {
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
	waits := map[int]dependencyWait{}
	var lastAt time.Time
	for _, record := range j.Events {
		decoded, err := reader.Apply(uint64(record.ID), record.Payload)
		if err != nil {
			return j, errors.New("dependency status unavailable")
		}
		if opts.WaitAge > 0 && decoded.Dependency != nil {
			lastAt = later(lastAt, record.RecordedAt)
			if err := trackDependencyWaits(j, reader, waits, lastAt); err != nil {
				return j, err
			}
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
	views := map[int]dependency.StatusView{}
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
		views[pr] = v
	}
	for pr, v := range views {
		j.DependencyStatus[pr] = dependencyItem(v, j.DependencyRedacted, waitAge(reader.Queue, pr, v, waits[pr], opts))
	}
	return j, nil
}

// dependencyItem constructs the presentation from an allowlist. A journal event
// reference is added only when the queue's journal positions are public.
func dependencyItem(v dependency.StatusView, redacted bool, age *store.DependencyWaitAge) store.DependencyStatus {
	item := store.DependencyStatus{Text: v.Text, Details: slices.Clone(v.Details), URL: v.URL, Redacted: v.Redacted, Blocking: v.Blocking, Warnings: slices.Clone(v.Warnings)}
	for _, d := range v.Diagnostics {
		item.Diagnostics = append(item.Diagnostics, store.DependencyDiagnostic{Code: d.Code, Source: d.Source})
	}
	if a := v.SelfAcknowledgement; a != nil {
		item.SelfAcknowledgement = &store.DependencySelfAcknowledgement{Kind: a.Kind, Actor: a.Actor, Overridden: slices.Clone(a.Overridden), GoverningCommit: a.GoverningCommit, Revoked: a.Revoked}
		if !redacted {
			item.SelfAcknowledgement.EventID = int64(a.EventID)
		}
	}
	if age != nil {
		item.WaitAge = age
		item.Details = append(item.Details, "dependency wait older than "+age.Threshold.String()+" (non-actionable; for operator review)")
	}
	return item
}

// waitAge flags only an authorized, blocking view of a queued change.
func waitAge(q queue.State, pr int, v dependency.StatusView, w dependencyWait, opts DependencyOptions) *store.DependencyWaitAge {
	if opts.WaitAge <= 0 || v.Redacted || !v.Blocking || w.since.IsZero() {
		return nil
	}
	since := w.since
	for _, e := range q.Entries {
		if e.PR != pr {
			continue
		}
		since = later(since, e.EnqueuedAt)
		if opts.Now.Sub(since) < opts.WaitAge {
			return nil
		}
		return &store.DependencyWaitAge{Since: since.UTC(), Threshold: opts.WaitAge}
	}
	return nil
}

// trackDependencyWaits restarts a change's wait on a new revision and clears it
// whenever recorded admission is satisfied.
func trackDependencyWaits(j store.Journal, reader journalreader.Reader, waits map[int]dependencyWait, at time.Time) error {
	state, err := reader.Dependencies()
	if err != nil {
		return errors.New("dependency status unavailable")
	}
	for pr, rev := range reader.Queue.ChangeRevisions() {
		id, err := j.Key.ChangeID(pr)
		if err != nil {
			return errors.New("dependency status unavailable")
		}
		w := waits[pr]
		if w.revision != uint64(rev.Revision) {
			w = dependencyWait{revision: uint64(rev.Revision)}
		}
		if state.Admission(string(id), w.revision).Code == "" {
			w.since = time.Time{}
		} else if w.since.IsZero() {
			w.since = at
		}
		waits[pr] = w
	}
	return nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
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

var errDependencyExport = errors.New("dependency export unavailable")

// DependencyExport builds the scoped audit export of one queue's dependency
// evidence for a recorded audience. It replays only the supplied journal through
// the real reader and makes no forge, database or clock reads.
//
// A complete audience may read every source the journal declares and receives
// every dependency record with its journal metadata. Any other audience receives
// a redacted export constructed as new values from consumer-repository facts:
// governing policy, declaration holds and (self-)acknowledgements. Observations,
// diagnostics, compositions, decisions and invalidations are withheld entirely,
// and so are journal IDs and recording times, because private source state
// decides whether and when those records exist.
func DependencyExport(j store.Journal, binding dependency.JournalInitialized, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord) (string, []audit.DependencyEntry, error) {
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
	scope := dependency.Scope{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, JournalNamespace: binding.Namespace}
	complete := true
	var full, redacted []audit.DependencyEntry
	var policy dependency.PolicyApplied
	for _, record := range j.Events {
		decoded, err := reader.Apply(uint64(record.ID), record.Payload)
		if err != nil {
			return "", nil, errDependencyExport
		}
		typ, kind, exported := journalreader.ExportKind(record.Payload)
		if !exported {
			continue
		}
		full = append(full, audit.DependencyEntry{Kind: kind, Source: &audit.DependencySource{ID: record.ID, Type: typ, RecordedAt: record.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000Z")}, Evidence: json.RawMessage(record.Payload)})
		state, err := reader.Dependencies()
		if err != nil {
			return "", nil, errDependencyExport
		}
		if d, ok := decoded.Dependency.(dependency.DependenciesDeclared); ok {
			for _, edge := range d.Edges {
				complete = complete && sourceAuthorized(edge.Reference, scope, audience, sources)
			}
		}
		if entry, ok := redactedEntry(kind, decoded.Dependency, state, policy); ok {
			redacted = append(redacted, entry)
		}
		if p, ok := decoded.Dependency.(dependency.PolicyApplied); ok {
			policy = p
		}
	}
	if complete {
		return audit.DependencyAudienceComplete, full, nil
	}
	return audit.DependencyAudienceRedacted, redacted, nil
}

// redactedEntry constructs new evidence from consumer-repository facts only.
// Every other kind, including any added later, is withheld by default. policy
// is the governing companion before event; state includes event.
func redactedEntry(kind string, event dependency.Event, state dependency.State, policy dependency.PolicyApplied) (audit.DependencyEntry, bool) {
	var evidence any
	switch e := event.(type) {
	case dependency.DependenciesDeclared:
		evidence = audit.DependencyDeclarationEvidence{ChangeID: e.ChangeID, Revision: e.Revision, Held: e.Hold.Held, UnknownEditor: e.Hold.Unknown, Editors: ids(e.Hold.Editors), Redeclaration: e.Redeclaration}
	case dependency.PolicyApplied:
		evidence = audit.DependencyPolicyEvidence{Enabled: e.Enabled, SingleMaintainer: e.Policy.SingleMaintainer, GoverningCommit: e.Commit, PolicyDigest: e.Digest, Writers: len(e.Writers)}
	case dependency.Acknowledged:
		d, _ := state.Declaration(e.Alias)
		evidence = audit.DependencyAcknowledgementEvidence{ChangeID: d.ChangeID, Writer: e.Writer.SubjectID, Exclusions: ids(e.Exclusions), GoverningCommit: e.GoverningCommit, PolicyDigest: e.PolicyDigest}
	case dependency.SelfAcknowledged:
		a := e.Acknowledged
		d, _ := state.Declaration(a.Alias)
		evidence = audit.DependencySelfAcknowledgementEvidence{ChangeID: d.ChangeID, Writer: a.Writer.SubjectID, Overridden: ids(e.Overridden), SingleMaintainer: policy.Enabled && policy.Policy.SingleMaintainer, GoverningCommit: a.GoverningCommit, PolicyDigest: a.PolicyDigest}
	default:
		return audit.DependencyEntry{}, false
	}
	return audit.DependencyEntry{Kind: kind, Evidence: evidence}, true
}

// DependencyExportBytes encodes DependencyExport as manifest and stream bytes.
func DependencyExportBytes(j store.Journal, binding dependency.JournalInitialized, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord) ([]byte, []byte, error) {
	scope, entries, err := DependencyExport(j, binding, audience, sources)
	if err != nil {
		return nil, nil, err
	}
	key := audit.Key{Installation: j.Key.Installation, Repository: j.Key.Repository, Base: j.Key.BaseBranch, Lane: j.Key.Lane}
	_, manifest, stream, err := audit.EncodeDependencyExport(key, scope, entries)
	return manifest, stream, err
}

// sourceAuthorized requires the recorded revision-less source binding and a
// complete recorded audience for that source repository.
func sourceAuthorized(reference string, scope dependency.Scope, audience map[string][]dependency.Grant, sources map[string]dependency.SourceRecord) bool {
	ref, err := dependency.ParseReference(reference, scope)
	if err != nil {
		return false
	}
	source, ok := sources[ref.ChangeID]
	if !ok || source.Reference.Scope != ref.Scope || source.Reference.ChangeID != ref.ChangeID || source.Reference.Revision != 0 {
		return false
	}
	return dependency.AuthorizedAudience(audience[reference], source.Alias.RepositoryID)
}

func ids(v []string) []string {
	if v == nil {
		return []string{}
	}
	return slices.Clone(v)
}
