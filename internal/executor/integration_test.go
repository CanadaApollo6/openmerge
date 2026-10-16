package executor_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestSerialQueueEndToEnd(t *testing.T) {
	for _, loseLandingResult := range []bool{false, true} {
		name := "normal"
		if loseLandingResult {
			name = "recover-lost-landing-result"
		}
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			now := epoch
			h.x.Now = func() time.Time { return now }
			cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute, ResolveAfter: time.Minute}
			s := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			for _, pr := range h.prs {
				s = queue.Apply(s, queue.PRObserved{PR: pr.Number, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
				s = queue.Apply(s, queue.EnqueueRequested{PR: pr.Number, At: now})
			}
			h.f.OnTestRef(func(sha string, _ []int) {
				h.f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			})
			var tested, landed, testRefs []string
			lost := false
			for steps := 0; ; steps++ {
				if steps > 100 {
					t.Fatalf("queue did not converge: %+v", s)
				}
				actions := queue.Plan(cfg, s, now)
				if len(actions) == 0 {
					break
				}
				a := actions[0]
				if build, ok := a.(queue.BuildTestRef); ok {
					testRefs = append(testRefs, build.Ref)
				}
				err := h.x.Execute(context.Background(), a, func(e queue.Event) error {
					if result, ok := e.(queue.Landed); ok && loseLandingResult && len(landed) == 1 && !lost {
						lost = true
						if h.read(t, "main") != result.NewBaseSHA {
							t.Fatal("test did not lose the result after a successful push")
						}
						return errLog
					}
					s = queue.Apply(s, e)
					switch e := e.(type) {
					case queue.TestRefBuilt:
						tested = append(tested, e.TestedSHA)
					case queue.Landed:
						landed = append(landed, e.NewBaseSHA)
					}
					return nil
				})
				if err != nil {
					if !loseLandingResult || !lost || !errors.Is(err, errLog) || s.Active == nil || s.Active.Phase != queue.Landing {
						t.Fatalf("unexpected execution failure: %v", err)
					}
					now = now.Add(2 * time.Minute)
				}
			}
			if len(s.Entries) != 0 || s.Active != nil || len(tested) != 3 || !slices.Equal(tested, landed) {
				t.Fatalf("queue lost PRs or landed untested commits: tested %v, landed %v, state %+v", tested, landed, s)
			}
			if loseLandingResult != lost {
				t.Fatal("crash-recovery scenario did not run")
			}
			history, err := h.f.History(repo, "main")
			must(t, err)
			var shas []string
			for _, commit := range history {
				shas = append(shas, commit.SHA)
			}
			want := append([]string{h.base}, tested...)
			if !slices.Equal(shas, want) {
				t.Fatalf("base history = %v, want exactly tested commits %v", shas, want)
			}
			for i, commit := range history[1:] {
				wantContent := []int{1, 2, 3}[:i+1]
				if !reflect.DeepEqual(commit.PRs, wantContent) {
					t.Fatalf("commit %d contains %v, want %v", i, commit.PRs, wantContent)
				}
			}
			if s.BaseSHA != h.read(t, "main") || s.Fence != h.read(t, fenceRef) {
				t.Fatal("queue state differs from actual base/fence")
			}
			for _, ref := range testRefs {
				if _, err := h.f.ReadRef(context.Background(), repo, ref); err == nil {
					t.Fatalf("test ref %s was not cleaned up", ref)
				}
			}
			for _, pr := range h.prs {
				if got := h.f.Statuses(repo, pr.Number)["openmerge"]; got.State != "merged" {
					t.Fatalf("PR %d did not receive merged status: %+v", pr.Number, got)
				}
			}
		})
	}
}
