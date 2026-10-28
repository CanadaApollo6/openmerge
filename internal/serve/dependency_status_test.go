package serve

import (
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func TestDependencyStatusActivationRemainsPrivate(t *testing.T) {
	j, b, ref, grant := statusfixture.StatusJournal(t, "dependency-landed", 1)
	inactive, err := dependencyJournal(j, nil)
	must(t, err)
	if inactive.DependencyStatus != nil {
		t.Fatal("inactive queue gained dependency presentation")
	}
	pass := &dependencyPass{binding: b, audience: map[string][]dependency.Grant{ref: {grant}}, sources: statusfixture.StatusSources()}
	api, err := dependencyJournal(j, pass)
	must(t, err)
	_, apiEntry, _ := status.Project(api, 2, 1, nil)
	if apiEntry.Dependency.Text != "Dependencies verified" {
		t.Fatal("authorized API lacks details", apiEntry.Dependency)
	}
	active, err := dependencyCheckJournal(j, pass)
	must(t, err)
	q, e, _ := status.Project(active, 2, 1, nil)
	if e.WaitingReason.Text != "Dependency cannot be verified" || status.WaitingCheckText(&q, *e, nil) != "Dependency cannot be verified" {
		t.Fatal("check leaked source success", e.WaitingReason)
	}
}

func TestDependencyStatusCacheUsesJournalWatermark(t *testing.T) {
	s, w, _, driver := fixture(t, repositoryConfig)
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
	depSide(t, s.store, w.id, binding, "status-init", binding)
	depSide(t, s.store, w.id, binding, "status-policy", depPolicy(binding, r.Consumer.Alias, r.Consumer.Queue, []string{r.Edge.Queue.Repository}, []string{}))
	depSide(t, s.store, w.id, binding, "status-declaration", r.Consumer)
	w.dependencies = &dependencyPass{binding: binding}
	pulls, checks := driver.pulls, driver.checks
	first, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	cached := w.ledgerCache
	if cached.projection.Watermark != 0 {
		t.Fatal("fixture did not withhold private references")
	}
	second, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if first != second || w.ledgerCache != cached {
		t.Fatal("redacted public watermark forced an unchanged journal replay")
	}
	if driver.pulls != pulls || driver.checks != checks {
		t.Fatal("cached rendering fetched the forge")
	}
	if _, _, _, err := s.ChangeDecisions(t.Context(), w.Key, id, 0, 0, 1); err == nil {
		t.Fatal("retained decision positions bypassed dependency reader scope")
	}

}
