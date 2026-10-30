package status

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// TestDependencyWaitAgeAuthorizedOnly flags only an authorized, blocking wait
// at least the threshold old. The zero option keeps 5-1 output unchanged, and
// the flag never changes WaitingReason precedence or blocking.
func TestDependencyWaitAgeAuthorizedOnly(t *testing.T) {
	sources := statusfixture.StatusSources()
	for _, tc := range []struct {
		name, code string
		after      time.Duration
		authorized bool
		flagged    bool
	}{
		{"old authorized wait", "dependency-waiting", 2 * time.Hour, true, true},
		{"young authorized wait", "dependency-waiting", 30 * time.Minute, true, false},
		{"old unauthorized wait", "dependency-waiting", 2 * time.Hour, false, false},
		{"eligible", "dependency-landed", 2 * time.Hour, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, binding, ref, grant := statusfixture.StatusJournal(t, tc.code, 1)
			audience := map[string][]dependency.Grant{}
			if tc.authorized {
				audience[ref] = []dependency.Grant{grant}
			}
			legacy, err := ProjectDependencies(j, binding, audience, sources)
			if err != nil {
				t.Fatal(err)
			}
			at := j.Events[0].RecordedAt
			got, err := ProjectDependenciesAt(j, binding, audience, sources, DependencyOptions{Now: at.Add(tc.after), WaitAge: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			item, base := got.DependencyStatus[2], legacy.DependencyStatus[2]
			if !tc.flagged {
				if !reflect.DeepEqual(item, base) {
					t.Fatalf("unflagged view changed:\n%+v\n%+v", item, base)
				}
				return
			}
			if item.WaitAge == nil || !item.WaitAge.Since.Equal(at) || item.WaitAge.Threshold != time.Hour || !slices.Equal(item.Details[:len(base.Details)], base.Details) || item.Details[len(item.Details)-1] != "dependency wait older than 1h0m0s (non-actionable; for operator review)" || item.Blocking != base.Blocking || item.Text != base.Text {
				t.Fatalf("flag %+v", item)
			}
			_, e, _ := Project(got, 2, 20, nil)
			_, legacyEntry, _ := Project(legacy, 2, 20, nil)
			if e.Dependency.WaitAge == nil || e.Dependency.WaitAge.OlderThan != "1h0m0s" || e.WaitingReason.Code != legacyEntry.WaitingReason.Code || e.WaitingReason.Text != legacyEntry.WaitingReason.Text {
				t.Fatalf("presentation %+v / %+v", e.Dependency, e.WaitingReason)
			}
		})
	}
}

// TestDependencyWaitAgeKeepsDetailBound keeps an authorized summary with a
// wait-age line within the documented cap, with the age line visible.
func TestDependencyWaitAgeKeepsDetailBound(t *testing.T) {
	for n := range dependency.MaxStatusDetails + 3 {
		var details []string
		for i := range n {
			details = append(details, fmt.Sprintf("line %d", i))
		}
		item := dependencyItem(dependency.StatusView{Details: dependency.BoundDetails(details), Blocking: true}, false, &store.DependencyWaitAge{Threshold: time.Hour})
		if len(item.Details) > dependency.MaxStatusDetails || !slices.Contains(item.Details, "dependency wait older than 1h0m0s (non-actionable; for operator review)") {
			t.Fatalf("%d details: %d lines %v", n, len(item.Details), item.Details)
		}
	}
}

// TestDependencyWaitAgeOwnHistory starts a change's wait at its own declaration,
// never at another change's earlier private read, and shows it only while the
// whole queue is unredacted. A, PR 5, depends on an explicitly pinned source
// reference; B, PR 2, on the unpinned one, so one audience can read A's source
// but not B's.
func TestDependencyWaitAgeOwnHistory(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	r := dependencytest.Request()
	at := j.Events[0].RecordedAt
	appendAt := func(offset time.Duration, event any) uint64 {
		t.Helper()
		var raw []byte
		var qe queue.Event
		var err error
		switch e := event.(type) {
		case queue.Event:
			_, raw, err = queue.EncodeEvent(e)
			qe = e
		case dependency.Event:
			_, raw, err = dependency.EncodeEvent(e)
		}
		if err != nil {
			t.Fatal(err)
		}
		id := uint64(len(j.Events) + 1)
		j.Events = append(j.Events, store.JournalEvent{ID: int64(id), RecordedAt: at.Add(offset), Payload: raw, Event: qe})
		return id
	}
	// A is queued before it declares anything.
	appendAt(10*time.Minute, queue.PRObserved{PR: 5, HeadSHA: fixtureHead5, Eligible: true, Trusted: true})
	appendAt(10*time.Minute, queue.EnqueueRequested{PR: 5, At: at.Add(10 * time.Minute)})
	// B's private read, recorded before A's declaration.
	var observed dependency.Observed
	var prior uint64
	for _, record := range j.Events {
		if e, err := dependency.DecodeEvent(record.Payload); err == nil {
			if o, ok := e.(dependency.Observed); ok {
				observed, prior = o, uint64(record.ID)
			}
		}
	}
	observed.ExpectedPrior, observed.ReadSequence = prior, observed.ReadSequence+1
	id := appendAt(20*time.Minute, observed)
	appendAt(20*time.Minute, dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: id, Source: r.Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: observed.Declaration, Grant: grant, Observation: observed, Code: "dependency-waiting"}})
	// A declares the pinned reference, then its own source is read.
	scope := dependency.Scope{ForgeInstance: binding.ForgeInstance, Installation: binding.Installation, JournalNamespace: binding.Namespace}
	parsed, err := dependency.ParseReference(ref, scope)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Revision = r.Edge.Baseline.Revision
	pinned, err := parsed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	changeID, err := j.Key.ChangeID(5)
	if err != nil {
		t.Fatal(err)
	}
	decl := r.Consumer
	decl.ChangeID, decl.Alias.Number = string(changeID), 5
	decl.Edges = []dependency.DeclaredEdge{r.Edge}
	decl.Edges[0].Reference, decl.Edges[0].PinnedHead = pinned, r.Edge.Baseline.Head
	decl.Digest = dependency.DeclarationDigest(decl.Edges)
	declared := 30 * time.Minute
	declID := appendAt(declared, decl)
	o := dependency.Observed{Namespace: binding.Namespace, Alias: decl.Alias, ChangeID: decl.ChangeID, Revision: 1, Declaration: dependency.Basis{EventID: declID, Digest: decl.Digest}, Source: pinned, ReadSequence: 1, SourceRevision: r.Source.Current, SourceBase: r.Source.BaseSHA, Result: "waiting"}
	oID := appendAt(40*time.Minute, o)
	appendAt(40*time.Minute, dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: oID, Source: r.Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: o.Declaration, Grant: grant, Observation: o, Code: "dependency-waiting"}})
	opts := DependencyOptions{Now: at.Add(48 * time.Hour), WaitAge: time.Hour}
	full, err := ProjectDependenciesAt(j, binding, map[string][]dependency.Grant{ref: {grant}, pinned: {grant}}, statusfixture.StatusSources(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if a := full.DependencyStatus[5]; full.DependencyRedacted || a.WaitAge == nil || !a.WaitAge.Since.Equal(at.Add(declared)) {
		t.Fatalf("complete audience: redacted=%t %+v", full.DependencyRedacted, a.WaitAge)
	}
	mixed, err := ProjectDependenciesAt(j, binding, map[string][]dependency.Grant{pinned: {grant}}, statusfixture.StatusSources(), opts)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mixed.DependencyStatus[5], mixed.DependencyStatus[2]
	if !mixed.DependencyRedacted || a.Redacted || !a.Blocking || !b.Redacted || a.WaitAge != nil || b.WaitAge != nil {
		t.Fatalf("mixed audience: redacted=%t A %+v B %+v", mixed.DependencyRedacted, a, b)
	}
}

// fixtureHead5 is PR 5's head in TestDependencyWaitAgeOwnHistory.
var fixtureHead5 = strings.Repeat("5", 40)

// statusViewFields pins every StatusView field so a new one must be classified.
var statusViewFields = []string{"Diagnostics", "Text", "Details", "URL", "Redacted", "Blocking", "SelfAcknowledgement", "Warnings"}

// TestDependencyItemReflectAllowlist fills a complete StatusView and requires
// the stored presentation to keep only allowlisted leaves. The journal event
// reference appears only while the queue's journal positions are public.
func TestDependencyItemReflectAllowlist(t *testing.T) {
	var names []string
	for i := range reflect.TypeFor[dependency.StatusView]().NumField() {
		names = append(names, reflect.TypeFor[dependency.StatusView]().Field(i).Name)
	}
	if !slices.Equal(names, statusViewFields) {
		t.Fatalf("StatusView fields %v must be classified for redaction", names)
	}
	var view dependency.StatusView
	reflectFill(t, reflect.ValueOf(&view).Elem(), "", map[reflect.Type]int{})
	allowed := []string{".Text", ".Details[]", ".URL", ".Warnings[]", ".Diagnostics[].Code", ".Diagnostics[].Source", ".SelfAcknowledgement.Kind", ".SelfAcknowledgement.Actor", ".SelfAcknowledgement.Overridden[]", ".SelfAcknowledgement.GoverningCommit"}
	for _, redacted := range []bool{true, false} {
		item := dependencyItem(view, redacted, nil)
		leaves := map[string]string{}
		reflectLeaves(reflect.ValueOf(item), "", leaves)
		for path, value := range leaves {
			source, filled := strings.CutPrefix(value, "filled:")
			switch {
			case path == ".SelfAcknowledgement.EventID":
				if redacted {
					t.Fatalf("journal reference %s survived redaction", value)
				}
			case filled && slices.Contains(allowed, source):
			case value == "true" && (path == ".Redacted" || path == ".Blocking" || path == ".SelfAcknowledgement.Revoked"):
			default:
				t.Fatalf("non-allowlisted leaf %s=%s", path, value)
			}
		}
		if _, ok := leaves[".SelfAcknowledgement.EventID"]; ok == redacted {
			t.Fatalf("event reference presence %t with redacted=%t", ok, redacted)
		}
	}
}
