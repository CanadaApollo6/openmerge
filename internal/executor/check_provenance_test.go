package executor_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestUnboundRequiredChecksDiagnostic(t *testing.T) {
	for _, operation := range []string{"refresh", "land"} {
		t.Run(operation, func(t *testing.T) {
			h := setup(t)
			s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
			h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{
				{ID: 1, Name: "ci", Source: "app:11", State: forge.Success},
				{ID: 2, Name: "ci", Source: "app:22", State: forge.Success, HeadBranch: "foreign"},
				{ID: 3, Name: "ci", Source: "app:11", State: forge.Success},
				{Name: "unrelated", Source: "app:33", State: forge.Success},
				{Name: "lint", State: forge.Success},
			})
			var action queue.Action = queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
			if operation == "land" {
				action = land
			}
			var observations []queue.CheckProvenanceObserved
			emit := func(event queue.Event) error {
				if observation, ok := event.(queue.CheckProvenanceObserved); ok {
					observations = append(observations, observation)
				}
				return replayChecks(t, &s)(event)
			}
			err := h.x.Execute(t.Context(), action, emit)
			if operation == "land" {
				if !errors.Is(err, executor.ErrChecksNotSuccessful) {
					t.Fatalf("unbound checks authorized landing: %v", err)
				}
			} else {
				must(t, err)
			}
			want := []queue.UnboundCheck{{Name: "ci", Source: "app:11"}, {Name: "ci", Source: "app:22", HeadBranch: "foreign"}}
			if len(observations) != 1 || observations[0].BatchID != land.BatchID || observations[0].TestedSHA != land.TestedSHA || !reflect.DeepEqual(s.UnboundChecks(), want) || s.ChecksPassed(land.TestedSHA) || h.read(t, "main") != h.base {
				t.Fatalf("missing diagnostic or unsafe result: observations=%+v state=%+v", observations, s.Active)
			}
			h.x.PreviousUnboundChecks = s.UnboundChecks()
			must(t, h.x.Execute(t.Context(), queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}, emit))
			if len(observations) != 1 {
				t.Fatal("unchanged refresh duplicated diagnostic")
			}
			for _, source := range []string{"app:11", "status:external-ci"} {
				h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{
					{Name: "ci", Source: source, State: forge.Pending, HeadBranch: land.Ref},
					{Name: "ci", Source: "app:22", State: forge.Success, HeadBranch: "foreign"},
				})
				must(t, h.x.Execute(t.Context(), queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}, emit))
				if s.CheckProvenanceDetail() != "" || len(observations[len(observations)-1].Checks) != 0 || s.ChecksPassed(land.TestedSHA) {
					t.Fatalf("bound pending run did not clear diagnostic safely: %+v", s.Active)
				}
			}
		})
	}
}

func TestCheckProvenanceMustBeDurable(t *testing.T) {
	h := setup(t)
	_, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:11", State: forge.Success}})
	err := h.x.Execute(t.Context(), land, func(event queue.Event) error {
		if _, ok := event.(queue.CheckProvenanceObserved); !ok {
			t.Fatalf("continued after failed diagnostic append: %T", event)
		}
		return errLog
	})
	if !errors.Is(err, errLog) || h.read(t, "main") != h.base {
		t.Fatalf("failed diagnostic append did not stop landing: %v", err)
	}
}
