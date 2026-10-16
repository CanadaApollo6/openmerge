package gitops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestExecutorSerialQueueOverRealGit(t *testing.T) {
	for _, loseResult := range []bool{false, true} {
		name := "normal"
		if loseResult {
			name = "recover-lost-landing-result"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			fence, err := f.g.EnsureFence(ctx, testRepo, testFence)
			must(t, err)
			// Forge API effects (checks/statuses/ref cleanup requests) remain
			// in memory; every Git interface operation uses the bare remote.
			driver := fake.New()
			now := epoch
			x := executor.Executor{Driver: driver, Git: f.g, Repo: testRepo, Base: "main",
				DisableDeleteHeadBranch: true, RequiredChecks: []string{"ci"}, Now: func() time.Time { return now }}
			cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute, ResolveAfter: time.Minute}
			s := queue.State{BaseBranch: "main", BaseSHA: f.base, Fence: fence}
			for _, pr := range f.prs {
				s = queue.Apply(s, queue.PRObserved{PR: pr.Number, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
				s = queue.Apply(s, queue.EnqueueRequested{PR: pr.Number, At: now})
			}
			var tested, landed []string
			lost := false
			errLog := errors.New("lost durable landing result")
			for steps := 0; ; steps++ {
				if steps > 100 {
					t.Fatalf("queue did not converge: %+v", s)
				}
				actions := queue.Plan(cfg, s, now)
				if len(actions) == 0 {
					break
				}
				err := x.Execute(ctx, actions[0], func(e queue.Event) error {
					if result, ok := e.(queue.Landed); ok && loseResult && len(landed) == 1 && !lost {
						lost = true
						if f.read("main") != result.NewBaseSHA {
							t.Fatal("landing result was lost before the remote push")
						}
						return errLog
					}
					s = queue.Apply(s, e)
					switch e := e.(type) {
					case queue.TestRefBuilt:
						tested = append(tested, e.TestedSHA)
						driver.SetCheckRuns(testRepo, e.TestedSHA, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success, HeadBranch: s.Active.TestRef}})
					case queue.Landed:
						landed = append(landed, e.NewBaseSHA)
						if f.read("main") != e.NewBaseSHA || f.read(testFence) != e.NewFence {
							t.Fatal("recorded landing differs from real remote refs")
						}
					}
					return nil
				})
				if err != nil {
					if !errors.Is(err, errLog) || !lost || s.Active == nil || s.Active.Phase != queue.Landing {
						t.Fatalf("unexpected executor error: %v", err)
					}
					now = now.Add(2 * time.Minute)
				}
			}
			if len(s.Entries) != 0 || s.Active != nil || len(tested) != 3 || !slices.Equal(tested, landed) || lost != loseResult {
				t.Fatalf("queue lost PRs or landed untested commits: tested=%v landed=%v state=%+v", tested, landed, s)
			}
			history := strings.Fields(gitCommand(t, f.remote, "", nil, "rev-list", "--first-parent", "--reverse", "refs/heads/main"))
			if want := append([]string{f.base}, tested...); !slices.Equal(history, want) {
				t.Fatalf("remote first-parent history = %v, want exactly tested commits %v", history, want)
			}
			for i, sha := range tested {
				for _, pr := range f.prs[:i+1] {
					for path, want := range f.files[pr.Number] {
						if got := gitCommand(t, f.remote, "", nil, "show", sha+":"+path); got != strings.TrimSpace(want) {
							t.Fatalf("tested history lost PR %d content", pr.Number)
						}
					}
				}
			}
			for _, pr := range f.prs {
				if status := driver.Statuses(testRepo, pr.Number)[pr.HeadSHA]; status.State != forge.Merged {
					t.Fatalf("PR %d status = %+v", pr.Number, status)
				}
			}
		})
	}
}
