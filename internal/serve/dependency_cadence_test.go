package serve

import (
	"bytes"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	checkstatus "github.com/CanadaApollo6/openmerge/internal/status"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
)

func astraCadenceFixture(t *testing.T) (*Server, *worker, *instructionDriver, dependency.JournalInitialized, dependency.Observed, uint64, dependency.SourceAlias, dependency.Grant) {
	s, w, _, _ := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, true))
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
	w.dependencies.checkAudience = nil
	head := strings.Repeat("c", 40)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.PRObserved{PR: 3, HeadSHA: head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 3, At: time.Now()}}, "")
	must(t, err)
	w.prs[1] = forge.PullRequest{Number: 1, HeadSHA: strings.Repeat("b", 40)}
	w.prs[3] = forge.PullRequest{Number: 3, HeadSHA: head}
	driver := &instructionDriver{Driver: w.Driver}
	w.Driver = driver
	return s, w, driver, binding, o, oid, r.Source.Alias, grant
}

func TestAstraR2PersistedCadence(t *testing.T) {
	t.Parallel()
	for _, pinGitHub := range []bool{false, true} {
		t.Run(fmt.Sprint(pinGitHub), func(t *testing.T) {
			s, w, driver, binding, o, oid, alias, grant := astraCadenceFixture(t)
			if pinGitHub {
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.Authority = authority.GitHub
				pin.OpenMerge = nil
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				pin.EffectiveDigest = state.ConfigDigest
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
				must(t, err)
			}
			must(t, s.report(t.Context(), w))
			first, err := s.store.Observation(t.Context(), w.id, "status/3")
			must(t, err)
			writes := driver.writes
			must(t, s.report(t.Context(), w))
			if driver.writes != writes {
				t.Errorf("unchanged report wrote %d extra check updates (first C=%s)", driver.writes-writes, first)
			}
			writes = driver.writes
			o.ExpectedPrior = oid
			o.ReadSequence++
			o.Result = "landed"
			next := depSide(t, s.store, w.id, binding, "private-landed", o)
			depSide(t, s.store, w.id, binding, "private-landed-diagnostic", dependency.DiagnosticRecorded{Namespace: binding.Namespace, ObservationEventID: next, Source: alias, Evidence: dependency.ReadEvidence{Stage: dependency.WorkRead, Declaration: o.Declaration, Grant: grant, Observation: o, Code: "dependency-landed"}})
			must(t, s.report(t.Context(), w))
			after, err := s.store.Observation(t.Context(), w.id, "status/3")
			must(t, err)
			if !bytes.Equal(first, after) {
				t.Errorf("private change altered persisted C: %s -> %s", first, after)
			}
			if driver.writes != writes {
				t.Errorf("private change triggered %d check updates despite identical persisted C", driver.writes-writes)
			}
		})
	}
}

func TestAstraR2RevocationBytes(t *testing.T) {
	t.Parallel()
	s, w, _, _, o, _, _, grant := astraCadenceFixture(t)
	generic, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	w.dependencies.checkAudience = map[string][]dependency.Grant{o.Source: {grant}}
	authorized, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if generic == authorized {
		t.Fatal("fixture never authorized source")
	}
	for _, kind := range []string{"denied", "lookup-failed", "identity-mismatch", "missing"} {
		g := grant
		switch kind {
		case "denied":
			g.Allowed = false
		case "lookup-failed":
			g = dependency.Grant{}
		case "identity-mismatch":
			g.ReturnedID = "other"
		}
		w.dependencies.checkAudience = map[string][]dependency.Grant{o.Source: {g}}
		if kind == "missing" {
			w.dependencies.checkAudience = nil
		}
		got, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if got != generic {
			t.Fatalf("%s not byte-identical generic: %q vs %q", kind, got, generic)
		}
	}
}

func cadenceGitHubPin(t *testing.T, s *Server, w *worker) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	pin.Authority = authority.GitHub
	pin.OpenMerge = nil
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pin.EffectiveDigest = state.ConfigDigest
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{pin}, "")
	must(t, err)
}

func TestDependencyRetainedCheckRevocation(t *testing.T) {
	t.Parallel()
	s, w, _, _, o, _, _, grant := astraCadenceFixture(t)
	cadenceGitHubPin(t, s, w)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.Dequeued{PR: 1, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested}}, "")
	must(t, err)
	w.dependencies.checkAudience = map[string][]dependency.Grant{o.Source: {grant}}
	_, err = s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	q := &w.ledgerCache.projection
	entry := q.LedgerEntry(1)
	authorized := checkstatus.WaitingCheckText(q, *entry, s.options.Sanitizer) + "; dependency-closed authorized source diagnostic"
	must(t, s.writeStatus(t.Context(), w, 1, strings.Repeat("b", 40), forge.Status{State: forge.Dequeued, Summary: authorized + checkstatus.LedgerSummary(entry)}))
	before, err := s.store.Observation(t.Context(), w.id, "status/1")
	must(t, err)
	w.dependencies.checkAudience = nil
	must(t, s.report(t.Context(), w))
	after, err := s.store.Observation(t.Context(), w.id, "status/1")
	must(t, err)
	if bytes.Equal(before, after) {
		t.Fatal("revocation retained authorized check")
	}
	if bytes.Contains(after, []byte(authorized)) && authorized != "" {
		t.Fatalf("authorized diagnostic survived revocation: %s", after)
	}
}

func TestDependencyMissingLedgerEntryDoesNotAbortReport(t *testing.T) {
	t.Parallel()
	s, w, driver, _, _, _, _, _ := astraCadenceFixture(t)
	cadenceGitHubPin(t, s, w)
	w.prs[99] = forge.PullRequest{Number: 99, HeadSHA: strings.Repeat("f", 40)}
	must(t, s.writeStatus(t.Context(), w, 99, strings.Repeat("f", 40), forge.Status{State: forge.Dequeued, Summary: "old check"}))
	writes := driver.writes
	must(t, s.report(t.Context(), w))
	if driver.writes <= writes {
		t.Fatal("other PRs were not reported")
	}
	must(t, s.report(t.Context(), w))
}
