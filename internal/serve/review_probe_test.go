package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type reviewCancelDriver struct {
	*countingDriver
	mode string
}

func (d reviewCancelDriver) CancelChecks(ctx context.Context, _ forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	switch d.mode {
	case "confirmed":
		for i := range req.Runs {
			req.Runs[i].State = forge.Cancelled
		}
		return forge.CancelChecksResult{Confirmed: req.Runs}, nil
	case "pending-partial", "unsupported-partial":
		req.Runs[0].State = forge.Cancelled
		result := forge.CancelChecksResult{Confirmed: req.Runs[:1]}
		if d.mode == "unsupported-partial" {
			result.Unsupported = "Actions permission not granted"
		}
		return result, nil
	case "unsupported":
		return forge.CancelChecksResult{Unsupported: "unsupported producer"}, nil
	case "permission-fallback":
		return forge.CancelChecksResult{Unsupported: "Actions permission not granted"}, nil
	case "404":
		return forge.CancelChecksResult{}, errors.New("404 Not Found")
	case "timeout":
		<-ctx.Done()
		return forge.CancelChecksResult{}, ctx.Err()
	default:
		return forge.CancelChecksResult{}, nil
	}
}

func TestReviewCancellationCannotStarveNewHead(t *testing.T) {
	for _, mode := range []string{"confirmed", "pending", "unsupported", "permission-fallback", "missing", "pending-partial", "unsupported-partial", "404", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s, w, f, d := fixture(t, repositoryConfig+"    speculation:\n      max_depth: 4\n")
			w.Driver = reviewCancelDriver{countingDriver: d, mode: mode}
			if mode == "missing" {
				w.Driver = struct{ forge.Driver }{d}
			}
			if mode == "timeout" {
				s.options.ActionTimeout = 5 * time.Second
			}
			f.OnTestRef(func(sha string, _ []int) {
				f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 101, Attempt: 1, Name: "ci", State: forge.Pending}, {ID: 201, Attempt: 1, Name: "optional", State: forge.Pending}})
			})
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			old, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if old.Active == nil || len(old.Active.ObservedRuns) != 2 {
				t.Fatalf("missing initial observed run: %+v", old.Active)
			}
			f.OnTestRef(func(sha string, _ []int) {
				f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 102, Attempt: 1, Name: "ci", State: forge.Success}})
			})
			_, err = f.PushPR(repo, 1, "new head")
			must(t, err)
			_, err = f.OpenPR(repo, 2, old.BaseSHA, true)
			must(t, err)
			f.AddComment(repo, 2, "writer", "@openmerge queue")
			for i := 0; i < 3; i++ {
				must(t, s.cycle(t.Context(), w))
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.BaseSHA == old.BaseSHA {
				t.Fatalf("retired cancellation blocked a passing PR #2: active=%+v entries=%+v", state.Active, state.Entries)
			}
		})
	}
}
