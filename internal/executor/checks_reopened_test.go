package executor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecordedSuccessIsRechecked(t *testing.T) {
	for _, operation := range []string{"refresh", "land"} {
		for _, mode := range []string{"pending", "missing", "ambiguous", "failed", "read-error"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				h := setup(t)
				s, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
				checks := []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}}
				switch mode {
				case "pending":
					checks[0].State = forge.Pending
				case "missing":
					checks = checks[1:]
				case "ambiguous":
					checks = append(checks, forge.CheckRun{Name: "ci", Source: "other", State: forge.Success})
				case "failed":
					checks[0].State = forge.Failure
				case "read-error":
					h.f.FailNext("CheckRuns", errIO)
				}
				h.f.SetCheckRuns(repo, land.TestedSHA, checks)
				var action queue.Action = land
				if operation == "refresh" {
					action = queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
				}
				var events []queue.Event
				ids := 0
				h.x.NewID = func() (string, error) { ids++; return "attempt", nil }
				err := h.x.Execute(context.Background(), action, func(e queue.Event) error {
					events = append(events, e)
					s = queue.Apply(s, e)
					return nil
				})
				if ids != 0 || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence || s.Active.Phase != queue.Testing {
					t.Fatalf("unsuccessful preflight started landing: events=%+v ids=%d", events, ids)
				}
				if mode == "read-error" {
					if !errors.Is(err, errIO) || len(events) != 0 {
						t.Fatalf("read failure emitted events: %+v %v", events, err)
					}
					return
				}
				var ambiguity *executor.CheckSourceAmbiguityError
				if mode == "ambiguous" {
					if !errors.As(err, &ambiguity) {
						t.Fatalf("missing ambiguity: %v", err)
					}
				} else if operation == "land" {
					if !errors.Is(err, executor.ErrChecksNotSuccessful) {
						t.Fatalf("declined Land must remain retryable: %v", err)
					}
				} else {
					must(t, err)
				}
				var want queue.Event = queue.ChecksReopened{TestedSHA: land.TestedSHA, At: epoch}
				if mode == "failed" {
					want = queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: false}
				}
				if len(events) == 0 || !reflect.DeepEqual(events[0], want) {
					t.Fatalf("missing revocation: %+v, want %+v", events, want)
				}
				for _, a := range queue.Plan(queue.Config{MergeMethod: queue.Merge}, s, epoch) {
					if _, ok := a.(queue.Land); ok {
						t.Fatal("freshly observed checks still plan Land")
					}
				}
				if mode != "failed" {
					h.f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
					must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
					must(t, h.x.Execute(context.Background(), land, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
					if h.read(t, "main") != land.TestedSHA || s.Active != nil {
						t.Fatal("passing rerun could not land")
					}
				}
			})
		}
	}
}

func TestLandCheckRevocationMustBeDurable(t *testing.T) {
	h := setup(t)
	_, land := reviewReady(t, h, h.base, h.fence, h.prs[0])
	h.f.SetCheckRuns(repo, land.TestedSHA, nil)
	err := h.x.Execute(context.Background(), land, func(queue.Event) error { return errLog })
	if !errors.Is(err, errLog) || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence {
		t.Fatalf("failed revocation allowed landing: %v", err)
	}
}
