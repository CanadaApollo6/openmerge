package serve

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestReportBuildingBatchAsTesting(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, false))
	pr := w.prs[1]
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{
		queue.EnqueueRequested{PR: 1, At: time.Now()},
		queue.BatchStarted{BatchID: "b1", BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, At: time.Now()},
	}, "")
	must(t, err)
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Building {
		t.Fatal("fixture must remain in the building phase")
	}
	must(t, s.report(t.Context(), w))
	if got := f.Statuses(repo, 1)[pr.HeadSHA]; got.State != forge.Testing {
		t.Fatalf("building batch status = %+v, want testing", got)
	}
}
