package serve

import (
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
)

func TestDependencyCheckGrantRevocation(t *testing.T) {
	s, w, f, driver := fixture(t, repositoryConfig)
	_, binding, _, _ := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	r := dependencytest.Request()
	r.Consumer.Namespace = binding.Namespace
	r.Consumer.Alias.Number = 1
	r.Consumer.Queue = dependency.QueueKey{Repository: "https://github.com/" + w.Key.Repository, Base: w.Key.BaseBranch}
	id, err := w.Key.ChangeID(1)
	must(t, err)
	r.Consumer.ChangeID = string(id)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.PRObserved{PR: 1, HeadSHA: strings.Repeat("b", 40), Eligible: true, Trusted: true}}, "")
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
	must(t, err)
	depSide(t, s.store, w.id, binding, "status-init", binding)
	depSide(t, s.store, w.id, binding, "status-policy", depPolicy(binding, r.Consumer.Alias, r.Consumer.Queue, []string{r.Edge.Queue.Repository}, []string{}))
	declarationID := depSide(t, s.store, w.id, binding, "status-declaration", r.Consumer)
	grant := dependency.Grant{SubjectID: "reader", ReturnedID: "reader", RepositoryID: r.Source.Alias.RepositoryID, ReadID: "read", Fresh: true, Allowed: true}
	o := dependency.Observed{Namespace: binding.Namespace, Alias: r.Consumer.Alias, ChangeID: r.Consumer.ChangeID, Revision: 1, Declaration: dependency.Basis{EventID: declarationID, Digest: r.Consumer.Digest}, Source: r.Edge.Reference, ReadSequence: 1, SourceRevision: r.Source.Current, SourceBase: r.Source.BaseSHA, Result: "unavailable"}
	oid := depSide(t, s.store, w.id, binding, "observation", o)
	depSide(t, s.store, w.id, binding, "diagnostic", dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: oid, Source: r.Source.Alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: o.Declaration, Grant: grant, Observation: o, Code: "dependency-closed"}})
	w.dependencies = &dependencyPass{binding: binding, checkAudience: map[string][]dependency.Grant{r.Edge.Reference: {grant}}, sources: statusfixture.StatusSources()}
	authorized, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if got := w.ledgerCache.projection.LedgerEntry(1).Dependency.Diagnostics; len(got) != 1 {
		t.Fatal("expected authorized diagnostic", got)
	}
	if !strings.Contains(authorized, "dependency-closed") || !strings.Contains(strings.ReplaceAll(authorized, `\_`, "_"), r.Edge.Reference) {
		t.Fatal("fixture did not expose authorized evidence", authorized)
	}
	must(t, s.status(t.Context(), w, 1, strings.Repeat("b", 40), forge.Status{State: forge.Testing, Reason: "dependency-closed", Summary: r.Edge.Reference}))
	cached := w.ledgerCache
	watermark, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	grant.Allowed = false
	w.dependencies.checkAudience[r.Edge.Reference] = []dependency.Grant{grant}
	summary, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if strings.Contains(summary, "dependency-closed") || strings.Contains(summary, r.Edge.Reference) || !strings.Contains(summary, "Dependency cannot be verified") {
		t.Fatal("revoked reader still receives private check output", summary)
	}
	if cached == w.ledgerCache || w.ledgerCache.watermark != watermark {
		t.Fatal("authorization change did not rebuild at same watermark")
	}
	must(t, s.status(t.Context(), w, 1, strings.Repeat("b", 40), forge.Status{State: forge.Testing, Reason: "dependency-closed", Summary: r.Edge.Reference}))
	sent := f.Statuses(repo, 1)
	if !strings.Contains(sent[strings.Repeat("b", 40)].Summary, "Dependency cannot be verified") || strings.Contains(sent[strings.Repeat("b", 40)].Summary, r.Edge.Reference) {
		t.Fatal("emitted stale check", sent)
	}
	if driver.pulls != 0 || driver.checks != 0 {
		t.Fatal("rendering fetched forge evidence")
	}
	check := status.DependencyCheckStatus(&w.ledgerCache.projection, w.ledgerCache.projection.LedgerEntry(1), forge.Status{State: forge.Testing, Reason: "dependency-closed", Summary: r.Edge.Reference})
	if check.State != forge.Queued || strings.Contains(check.Summary, r.Edge.Reference) {
		t.Fatal(check)
	}
	// Losing the entire audience or source-binding snapshot must also fail closed.
	for _, revoke := range []func(){func() { w.dependencies.checkAudience = nil }, func() { w.dependencies.sources = nil }} {
		w.dependencies.checkAudience = map[string][]dependency.Grant{r.Edge.Reference: {dependency.Grant{SubjectID: "reader", ReturnedID: "reader", RepositoryID: r.Source.Alias.RepositoryID, ReadID: "read", Fresh: true, Allowed: true}}}
		w.dependencies.sources = statusfixture.StatusSources()
		_, err = s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		revoke()
		got, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if strings.Contains(got, r.Edge.Reference) || !strings.Contains(got, "Dependency cannot be verified") {
			t.Fatal(got)
		}
	}
}
