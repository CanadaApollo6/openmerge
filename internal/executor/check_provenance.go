package executor

import (
	"cmp"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (x *Executor) observeCheckProvenance(checks []forge.CheckRun, batchID, sha string, emit func(queue.Event) error) error {
	ref := "openmerge/queue/" + x.Base + "/" + batchID
	bound := make(map[string]bool)
	for _, check := range checks {
		if strings.HasPrefix(check.Source, "status:") || check.HeadBranch == ref {
			bound[check.Name] = true
		}
	}
	unbound := make([]queue.UnboundCheck, 0)
	for _, check := range checks {
		required := slices.Contains(x.RequiredChecks, check.Name)
		if x.ReferencesCheck != nil {
			required = x.ReferencesCheck(check.Name)
		}
		if required && !bound[check.Name] {
			unbound = append(unbound, queue.UnboundCheck{Name: check.Name, Source: check.Source, HeadBranch: check.HeadBranch})
		}
	}
	slices.SortFunc(unbound, func(a, b queue.UnboundCheck) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Source, b.Source), cmp.Compare(a.HeadBranch, b.HeadBranch))
	})
	unbound = slices.Compact(unbound)
	if slices.Equal(unbound, x.PreviousUnboundChecks) {
		return nil
	}
	return emit(queue.CheckProvenanceObserved{BatchID: batchID, TestedSHA: sha, Checks: unbound})
}
