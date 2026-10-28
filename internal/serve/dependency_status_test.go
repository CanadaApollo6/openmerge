package serve

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func TestDependencyStatusActivationRemainsPrivate(t *testing.T) {
	j, b, ref, grant := dependencytest.StatusJournal(t, "dependency-landed", 1)
	inactive, err := dependencyJournal(j, nil)
	must(t, err)
	if inactive.DependencyStatus != nil {
		t.Fatal("inactive queue gained dependency presentation")
	}
	pass := &dependencyPass{binding: b, audience: map[string][]dependency.Grant{ref: {grant}}, sources: dependencytest.StatusSources()}
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
