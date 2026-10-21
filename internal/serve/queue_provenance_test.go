package serve

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestMergeWhenRequiresQueueCheckProvenance(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		for _, scenario := range []string{"foreign branch", "unknown branch", "commit status"} {
			t.Run(string(method)+"/"+scenario, func(t *testing.T) {
				testSynctest(t, func(t *testing.T) {
					s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: "+string(method)+"\n")
					base, err := f.ReadRef(t.Context(), repo, "main")
					must(t, err)
					pr, err := f.PullRequest(t.Context(), repo, 1)
					must(t, err)
					f.AddComment(repo, 1, "writer", "@openmerge queue")
					must(t, s.cycle(t.Context(), w))
					state, _, err := s.store.Load(t.Context(), w.id)
					must(t, err)
					if state.Active == nil || state.Active.Phase != queue.Testing {
						t.Fatal("batch did not start testing")
					}
					tested, ref := state.Active.TestedSHA, state.Active.TestRef
					if scenario != "commit status" {
						branch := "contributor/reposted-marker"
						if scenario == "unknown branch" {
							branch = ""
						}
						f.SetCheckRuns(repo, tested, []forge.CheckRun{
							{ID: 100, Name: "ci", Source: "app:15368", State: forge.Pending, HeadBranch: ref},
							{ID: 999, Name: "ci", Source: "app:15368", State: forge.Success, HeadBranch: branch},
						})
						time.Sleep(2 * time.Millisecond)
						must(t, s.cycle(t.Context(), w))
						state, _, err = s.store.Load(t.Context(), w.id)
						must(t, err)
						if state.BaseSHA != base || state.Active == nil || state.Active.Phase != queue.Testing {
							t.Fatal("foreign same-App pass satisfied merge_when while queue CI was pending")
						}
						f.SetCheckRuns(repo, tested, []forge.CheckRun{
							{ID: 101, Name: "ci", Source: "app:15368", State: forge.Success, HeadBranch: ref},
							{ID: 999, Name: "ci", Source: "app:222", State: forge.Failure, HeadBranch: branch},
						})
					} else {
						f.SetCheckRuns(repo, tested, []forge.CheckRun{{ID: 1, Name: "ci", Source: "status:external-ci", State: forge.Success}})
					}
					time.Sleep(2 * time.Millisecond)
					must(t, s.cycle(t.Context(), w))
					want := tested
					if method == queue.FastForward {
						want = pr.HeadSHA
					}
					state, _, err = s.store.Load(t.Context(), w.id)
					must(t, err)
					if state.BaseSHA != want || state.Active != nil || len(state.Entries) != 0 {
						t.Fatalf("accepted CI did not land: %+v", state)
					}
				})
			})
		}
	}
}
