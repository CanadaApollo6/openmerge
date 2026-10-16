package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestServeFastForwardExactHeadAndDivergence(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(map[bool]string{false: "land", true: "base-moved"}[diverged], func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: fast-forward\n")
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.Active == nil || state.Active.TestedSHA != pr.HeadSHA || state.Active.Method != queue.FastForward {
				t.Fatalf("not testing exact head: %+v", state.Active)
			}
			if diverged {
				_, err = f.PushBase(repo, "main", "concurrent update", map[string]string{"other": "change"})
				must(t, err)
			}
			f.SetCheckRuns(repo, pr.HeadSHA, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
			time.Sleep(2 * time.Millisecond)
			must(t, s.cycle(t.Context(), w))
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.Active != nil || len(state.Entries) != 0 {
				t.Fatalf("queue did not settle: %+v", state)
			}
			tip, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			if diverged {
				if tip == pr.HeadSHA {
					t.Fatal("diverged head landed")
				}
				status := f.Statuses(repo, 1)[pr.HeadSHA]
				if status.Reason != string(queue.FastForwardRequired) || !strings.Contains(status.Summary, "update the branch") {
					t.Fatalf("missing actionable reason: %+v", status)
				}
			} else {
				if tip != pr.HeadSHA {
					t.Fatal("landed a new commit")
				}
				_, id, tested, err := f.ReadFence(t.Context(), repo, "openmerge/fence/main")
				must(t, err)
				if !strings.HasPrefix(id, "land/") || tested != pr.HeadSHA {
					t.Fatal("missing landing fence")
				}
				if _, err := f.ReadRef(t.Context(), repo, pr.HeadBranch); err == nil {
					t.Fatal("head cleanup missing")
				}
			}
			// Replay through a new server using the same isolated test journal.
			restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			must(t, restarted.cycle(t.Context(), restarted.queues[0]))
			after, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if after.BaseSHA != state.BaseSHA || len(after.Entries) != 0 {
				t.Fatal("restart changed settled result")
			}
		})
	}
}

func TestServeFastForwardBatchSelection(t *testing.T) {
	for _, linear := range []bool{true, false} {
		t.Run(map[bool]string{true: "linear", false: "siblings"}[linear], func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: fast-forward\n    batch: {max_size: 2}\n")
			first, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			base, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			parent := base
			if linear {
				parent = first.HeadSHA
			}
			second, err := f.OpenPR(repo, 2, parent, true)
			must(t, err)
			must(t, f.SetPRMetadata(repo, 2, "main", false, "contributor"))
			var tested [][]int
			f.OnTestRef(func(sha string, prs []int) {
				tested = append(tested, prs)
				f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
			})
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			f.AddComment(repo, 2, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if len(state.Entries) != 0 || state.Active != nil || len(tested) != 1 {
				t.Fatalf("unsettled batch: %+v; CI=%v", state, tested)
			}
			if linear {
				if len(tested[0]) != 2 || state.BaseSHA != second.HeadSHA {
					t.Fatal("linear chain did not batch")
				}
			} else {
				if len(tested[0]) != 1 || state.BaseSHA != first.HeadSHA || f.Statuses(repo, 2)[second.HeadSHA].Reason != string(queue.FastForwardRequired) {
					t.Fatal("siblings were combined or not dequeued")
				}
			}
		})
	}
}
