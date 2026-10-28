package status

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
 "github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// The nil embedded forge is a tripwire for every forge method: rendering may
// only call Journals. An accidental forge call panics instead of reaching I/O.
type dependencyRenderSource struct {
	forge.Driver
	journal store.Journal
}

func (s dependencyRenderSource) Journals(_ context.Context, _ []store.QueueKey) ([]store.Journal, error) {
	return []store.Journal{s.journal}, nil
}

func TestDependencyPrivacyGoldensNoRenderingFetch(t *testing.T) {
	states := []string{"missing", "dependency-unresolved", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope", "dependency-source-retargeted", "dependency-not-on-base", "dependency-unverified"}
	var wantAPI, wantCheck []byte
	for i, state := range states {
		t.Run(state, func(t *testing.T) {
			j, binding, ref, grant := statusfixture.StatusJournal(t, state, i+1)
			denied := grant
			denied.Allowed = false
			mismatch := grant
			mismatch.ReturnedID = "different"
			stale := grant
			stale.Fresh = false
			for _, audience := range [][]dependency.Grant{nil, {denied}, {mismatch}, {stale}, {grant, denied}} {
				projected, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: audience}, statusfixture.StatusSources())
				if err != nil {
					t.Fatal(err)
				}
				h := Handler(dependencyRenderSource{journal: projected}, []store.QueueKey{j.Key}, "", nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/acme/client/main/prs/2?events=1", nil))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
				q, e, _ := Project(projected, 2, 1, nil)
				if e == nil {
					t.Fatal("missing PR")
				}
				fullCheck := DependencyCheckStatus(&q, e, forge.Status{State: forge.Testing, Reason: state, Summary: state})
				fullCheck.Summary += "\n\nCurrent state: " + WaitingCheckText(&q, *e, nil) + LedgerSummary(e)
				check, err := json.Marshal(fullCheck)
				if err != nil {
					t.Fatal(err)
				}
				if wantAPI == nil {
					wantAPI = bytes.Clone(w.Body.Bytes())
					wantCheck = bytes.Clone(check)
				}
				if !bytes.Equal(wantAPI, w.Body.Bytes()) || !bytes.Equal(wantCheck, check) {
					t.Fatalf("private source state leaked:\n%s\n%s", w.Body, check)
				}
				if e.WaitingReason.Code != WaitingDependencies || e.WaitingReason.Text != "Dependency cannot be verified" || len(e.Dependency.Details) != 0 || e.Dependency.URL != "" {
					t.Fatal(e.WaitingReason)
				}
			}
		})
	}
	reportingGolden(t, "dependency-private-api.json", wantAPI)
	reportingGolden(t, "dependency-private-check.json", wantCheck)
}

func TestDependencyAuthorizedDiagnosticsAndPrecedence(t *testing.T) {
	for _, code := range []string{"dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-source-retargeted"} {
		j, binding, ref, grant := statusfixture.StatusJournal(t, code, 1)
		j, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
		if err != nil {
			t.Fatal(err)
		}
		q, e, _ := Project(j, 2, 20, nil)
		raw, _ := json.Marshal(e.Dependency)
		if !bytes.Contains(raw, []byte(code)) {
			t.Fatalf("missing typed diagnosis %s: %s", code, raw)
		}
		if code == "dependency-landed" && e.WaitingReason.Code == WaitingDependencies {
			t.Fatal("verified dependencies still block")
		}
		for _, tc := range []struct {
			status, gate string
			code         WaitingCode
		}{
			{"merged", "", WaitingDone}, {"closed", "", WaitingDone}, {"landing", "", WaitingLanding}, {"queued", "landing-settlement", WaitingLanding}, {"dequeued", "", WaitingDequeued}, {"paused", "", WaitingPaused}, {"waiting-entry", "", WaitingEntry}, {"blocked", "", WaitingEntry},
		} {
			x := *e
			x.Status = tc.status
			x.Gate = tc.gate
			if got := DeriveWaitingReason(&q, x); got.Code != tc.code {
				t.Fatalf("%s precedence: %+v", tc.status, got)
			}
		}
		q.Paused = true
		if got := DeriveWaitingReason(&q, *e); got.Code != WaitingPaused {
			t.Fatal(got)
		}
	}
}

func TestDependencyMissingDiagnosticAndBindingFailClosed(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-closed", 1)
	for i := range j.Events {
		if bytes.Contains(j.Events[i].Payload, []byte(`"type":"dependency_diagnostic_recorded"`)) {
			j.Events = append(j.Events[:i:i], j.Events[i+1:]...)
			break
		}
	}
	j, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	_, e, _ := Project(j, 2, 20, nil)
	if e.Dependency.Text != "Dependency cannot be verified" {
		t.Fatal(e.Dependency)
	}
	binding.Incarnation = strings.Repeat("f", 64)
	_, err = ProjectDependencies(j, binding, nil, nil)
	if err == nil || err.Error() != "dependency status unavailable" {
		t.Fatal("identity failure leaked or accepted", err)
	}
}

func TestDependencySourceGrantConfusionFailsClosed(t *testing.T) {
	j, binding, ref, _ := statusfixture.StatusJournal(t, "dependency-closed", 1)
	var unrelated dependency.Grant
	for i, record := range j.Events {
		if !bytes.Contains(record.Payload, []byte(`"type":"dependency_diagnostic_recorded"`)) {
			continue
		}
		decoded, err := dependency.DecodeEvent(record.Payload)
		if err != nil {
			t.Fatal(err)
		}
		d := decoded.(dependency.DiagnosticRecorded)
		d.Source.RepositoryID = "unrelated-public-repo"
		d.Evidence.Grant.RepositoryID = d.Source.RepositoryID
		unrelated = d.Evidence.Grant
		_, j.Events[i].Payload, err = dependency.EncodeEvent(d)
		if err != nil {
			t.Fatal(err)
		}
	}
	projected, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {unrelated}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	_, e, _ := Project(projected, 2, 1, nil)
	if e.Dependency.Text != "Dependency cannot be verified" || len(e.Dependency.Diagnostics) != 0 || !projected.DependencyRedacted {
		t.Fatal("unrelated repository authorized private source", e.Dependency)
	}
}

func TestDependencyHistoricalRevisionWithholdsCurrentDetails(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-closed", 1)
	j, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	event := queue.PRObserved{PR: 2, HeadSHA: strings.Repeat("d", 40), Eligible: true, Trusted: true}
	_, raw, err := queue.EncodeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Payload: raw, Event: event})
	id, err := j.Key.ChangeID(2)
	if err != nil {
		t.Fatal(err)
	}
	historical, _, found := selectChangeRevision(j, string(id), 1)
	if !found {
		t.Fatal("missing revision 1")
	}
	_, entry, _ := Project(historical, 2, 1, nil)
	if entry.Dependency != nil || len(historical.DependencyStatus) != 0 || len(j.DependencyStatus) == 0 {
		t.Fatal("current diagnostics contaminated historical revision")
	}
	// The current head cannot inherit an older declaration's diagnosis either.
	current, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	_, entry, _ = Project(current, 2, 1, nil)
	if entry.Dependency != nil {
		t.Fatal("old declaration described a new head")
	}
}

func TestDependencyAuthorizedReferencesRemainOriginal(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-closed", 3)
	projected, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	q, _, why := Project(projected, 2, 100, nil)
	last := j.Events[len(j.Events)-1].ID
	if q.Watermark != last || len(why.Events) != 3 || why.Events[len(why.Events)-1].ID != last {
		t.Fatalf("original references lost: watermark=%d why=%+v expected=%d", q.Watermark, why, last)
	}
}

func TestDependencyUnstartedProgressPrivacy(t *testing.T) {
	var wantAPI, wantCheck, wantHistorical, wantBrief []byte
	for _, phase := range []string{"queued", "building", "testing", "discarded", "invalidating", "retrying", "fast-forward"} {
		j, b, ref, grant := statusfixture.StatusJournal(t, "dependency-landed", 1)
		records := map[uint64][]byte{}
		reader := journalreader.New(j.Seed, b, func(id uint64) ([]byte, error) { return records[id], nil })
		reader.WithChangeIdentity(func(pr int) (queue.ChangeID, error) { return j.Key.ChangeID(pr) })
		var observed dependency.Observed
		var observedID uint64
		for _, record := range j.Events {
			records[uint64(record.ID)] = record.Payload
			decoded, err := reader.Apply(uint64(record.ID), record.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if o, ok := decoded.Dependency.(dependency.Observed); ok {
				observed = o
				observedID = uint64(record.ID)
			}
		}
		appendRecord := func(event any) uint64 {
			var raw []byte
			var qe queue.Event
			var err error
			switch e := event.(type) {
			case queue.Event:
				_, raw, err = queue.EncodeEvent(e)
				qe = e
			case dependency.Event:
				_, raw, err = dependency.EncodeEvent(e)
			default:
				t.Fatalf("unsupported %T", e)
			}
			if err != nil {
				t.Fatal(err)
			}
			id := uint64(len(j.Events) + 1)
			records[id] = raw
			if _, err := reader.Apply(id, raw); err != nil {
				t.Fatal(err)
			}
			j.Events = append(j.Events, store.JournalEvent{ID: int64(id), Payload: raw, Event: qe})
			return id
		}
		at := j.Events[0].RecordedAt
		var compositionID uint64
		if phase != "queued" {
			startID := appendRecord(queue.BatchStarted{BatchID: "b1", BaseSHA: j.Seed.BaseSHA, PRs: []queue.BatchPR{{PR: 2, HeadSHA: strings.Repeat("b", 40)}}, At: at})
			state, err := reader.Dependencies()
			if err != nil {
				t.Fatal(err)
			}
			id, err := j.Key.ChangeID(2)
			if err != nil {
				t.Fatal(err)
			}
			member, err := state.CompositionMember(string(id), 1, strings.Repeat("b", 40))
			if err != nil {
				t.Fatal(err)
			}
			compositionID = appendRecord(dependency.CompositionRecorded{Namespace: b.Namespace, BatchID: "b1", StartEventID: startID, BaseSHA: j.Seed.BaseSHA, Members: []dependency.CompositionMember{member}})
		}
		if phase == "retrying" {
			appendRecord(queue.TestRefBuildFailed{BatchID: "b1", At: at})
		}
		if phase == "fast-forward" {
			appendRecord(queue.FastForwardBatchSelected{BatchID: "b1", PRs: []queue.BatchPR{{PR: 2, HeadSHA: strings.Repeat("b", 40)}}})
			appendRecord(queue.FastForwardRefBuilt{BatchID: "b1", TestedSHA: strings.Repeat("e", 40), HeadSHA: strings.Repeat("b", 40), At: at})
		}
		if phase == "testing" || phase == "discarded" || phase == "invalidating" {
			appendRecord(queue.TestRefBuilt{BatchID: "b1", TestedSHA: strings.Repeat("e", 40), Method: queue.Merge, At: at})
		}
		if phase == "discarded" || phase == "invalidating" {
			observed.ExpectedPrior = observedID
			observed.ReadSequence++
			observed.Result = "waiting"
			cause := appendRecord(observed)
			appendRecord(dependency.DiagnosticRecorded{Namespace: b.Namespace, ObservationEventID: cause, Source: dependencytest.Request().Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: observed.Declaration, Grant: grant, Observation: observed, Code: "dependency-waiting"}})
			if reader.Queue.Active == nil || reader.Queue.Active.InvalidationReason != "dependency-changed" {
				t.Fatal("fixture did not latch invalidation")
			}
			if phase == "discarded" {
				appendRecord(queue.DependencyCompositionsInvalidated{BatchIDs: []string{"b1"}, CompositionIDs: []uint64{compositionID}, CauseEventIDs: []uint64{cause}, Reason: "dependency-changed"})
				appendRecord(queue.CheckCancellationRequested{BatchID: "b1", TestedSHA: strings.Repeat("e", 40), Reason: "dependency-changed", TestRef: "openmerge/queue/main/b1", Runs: []queue.CheckRun{{ID: 1, Name: "ci", Source: "app", State: "pending"}}})
			}
		}
		if phase == "building" || phase == "testing" || phase == "fast-forward" {
			expected := queue.Building
			if phase != "building" {
				expected = queue.Testing
			}
			if reader.Queue.Active == nil || reader.Queue.Active.Phase != expected {
				t.Fatalf("%s fixture did not exercise %s", phase, expected)
			}
		}
		if phase == "discarded" && (reader.Queue.Active != nil || len(reader.Queue.Invalidations) == 0) {
			t.Fatal("fixture did not discard a real composition")
		}
		if phase == "retrying" && (reader.Queue.Active != nil || !reader.Queue.SourceBlocked(queue.BuildFailed)) {
			t.Fatal("fixture did not exercise retryable build failure")
		}
		if phase == "invalidating" {
			authorized, err := ProjectDependencies(j, b, map[string][]dependency.Grant{ref: {grant}}, statusfixture.StatusSources())
			if err != nil {
				t.Fatal(err)
			}
			_, e, _ := Project(authorized, 2, 100, nil)
			if e.Status != "invalidating" {
				t.Fatal("status lost composed invalidation", e.Status)
			}
		}
		j, err := ProjectDependencies(j, b, nil, statusfixture.StatusSources())
		if err != nil {
			t.Fatal(err)
		}
		q, e, why := Project(j, 2, 100, nil)
		ApplyPolicy(&q, e, queue.Config{MergeMethod: queue.Merge}, at)
		raw, err := json.Marshal(Response{Version: Version, Queue: &q, PR: e, Why: why})
		if err != nil {
			t.Fatal(err)
		}
		check := DependencyCheckStatus(&q, e, forge.Status{State: forge.Testing, Reason: phase, Summary: phase})
		check.Summary += "\n\nCurrent state: " + WaitingCheckText(&q, *e, nil) + LedgerSummary(e)
		checkRaw, err := json.Marshal(check)
		if err != nil {
			t.Fatal(err)
		}
		if wantAPI == nil {
			wantAPI = raw
			wantCheck = checkRaw
		}
		if !bytes.Equal(wantAPI, raw) || !bytes.Equal(wantCheck, checkRaw) {
			t.Fatalf("%s exposed unstarted dependency progress:\n%s\n%s", phase, raw, checkRaw)
		}
		id, err := j.Key.ChangeID(2)
		if err != nil {
			t.Fatal(err)
		}
		historical, _, found := selectChangeRevision(j, string(id), 1)
		if !found {
			t.Fatal("missing historical revision")
		}
		hq, he, hw := Project(historical, 2, 100, nil)
		historicalRaw, err := json.Marshal(Response{Version: Version, Queue: &hq, PR: he, Why: hw})
		if err != nil {
			t.Fatal(err)
		}
		view, found, err := projectBrief(historical, string(id), "reviewer")
		if err != nil || !found {
			t.Fatal(found, err)
		}
		briefRaw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if wantHistorical == nil {
			wantHistorical = historicalRaw
			wantBrief = briefRaw
		}
		if !bytes.Equal(wantHistorical, historicalRaw) || !bytes.Equal(wantBrief, briefRaw) {
			t.Fatalf("%s historical source progress leaked:\n%s\n%s", phase, historicalRaw, briefRaw)
		}
	}
}

func TestDependencyExplicitPinPresentation(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	scope := dependency.Scope{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, JournalNamespace: binding.Namespace}
	parsed, err := dependency.ParseReference(ref, scope)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Revision = 7
	pinned, err := parsed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	for i, record := range j.Events {
		if record.Event != nil {
			continue
		}
		event, err := dependency.DecodeEvent(record.Payload)
		if err != nil {
			t.Fatal(err)
		}
		switch e := event.(type) {
		case dependency.DependenciesDeclared:
			e.Edges[0].Reference = pinned
			e.Edges[0].PinnedHead = e.Edges[0].Baseline.Head
			e.Digest = dependency.DeclarationDigest(e.Edges)
			digest = e.Digest
			event = e
		case dependency.Observed:
			e.Source = pinned
			e.Declaration.Digest = digest
			event = e
		case dependency.DiagnosticRecorded:
			e.Evidence.Observation.Source = pinned
			e.Evidence.Observation.Declaration.Digest = digest
			e.Evidence.Declaration.Digest = digest
			event = e
		}
		_, j.Events[i].Payload, err = dependency.EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
	}
	j, err = ProjectDependencies(j, binding, map[string][]dependency.Grant{pinned: {grant}}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	q, e, _ := Project(j, 2, 1, nil)
	if !strings.Contains(WaitingCheckText(&q, *e, nil), "at revision 7") || e.Dependency.Diagnostics[0].Source != pinned {
		t.Fatal("explicit pin missing", e.Dependency)
	}
}

func TestDependencySourceBindingGuards(t *testing.T) {
	j, b, ref, g := statusfixture.StatusJournal(t, "dependency-closed", 1)
	for _, name := range []string{"missing", "alias", "scope", "change", "revision"} {
		sources := statusfixture.StatusSources()
		r := dependencytest.Request().Source
		switch name {
		case "missing":
			sources = nil
		case "alias":
			r.Alias.Number++
		case "scope":
			r.Reference.Scope.Installation = "other"
		case "change":
			r.Reference.ChangeID = "chg1_" + strings.Repeat("e", 64)
		case "revision":
			r.Reference.Revision = 1
		}
		if sources != nil {
			sources[dependencytest.Request().Source.Reference.ChangeID] = r
		}
		projected, err := ProjectDependencies(j, b, map[string][]dependency.Grant{ref: {g}}, sources)
		if err != nil {
			t.Fatal(name, err)
		}
		_, e, _ := Project(projected, 2, 1, nil)
		if e.Dependency.Text != "Dependency cannot be verified" || len(e.Dependency.Diagnostics) != 0 {
			t.Fatal("invalid source binding disclosed diagnostics", name, e.Dependency)
		}
	}
}
