package serve

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestManagedPolicyChangeReobservesUnchangedPRs(t *testing.T) {
	for _, refreshFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "awaiting-activation", true: "refresh-before-reconcile"}[refreshFirst], func(t *testing.T) {
			s, f := managedFixture(t, releaseConfig, Options{})
			w := baseWorker(t, s, "release/v1")
			tip, err := f.ReadRef(t.Context(), repo, w.Key.BaseBranch)
			must(t, err)
			for _, n := range []int{1, 2} {
				_, err := f.OpenPR(repo, n, tip, true)
				must(t, err)
				must(t, f.SetPRMetadata(repo, n, w.Key.BaseBranch, false, "contributor"))
			}
			must(t, s.reconcile(t.Context(), w, false))
			before, err := f.ListOpenPullRequests(t.Context(), repo, w.Key.BaseBranch)
			must(t, err)
			if len(before) != 2 {
				t.Fatal("expected two open PRs")
			}
			for _, summary := range before {
				data, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("pr/%d", summary.Number))
				must(t, err)
				var observation struct{ Decision queue.PRObserved }
				must(t, json.Unmarshal(data, &observation))
				if !observation.Decision.Eligible {
					t.Fatal("initial policy should allow both PRs")
				}
			}
			updated := strings.Replace(releaseConfig, "    base: release/*", "    base: release/*\n    enter_when: [label = ready]", 1)
			_, err = f.PushBase(repo, "main", "require ready label", map[string]string{".openmerge.yml": updated})
			must(t, err)
			if refreshFirst {
				must(t, s.RefreshBranches(t.Context()))
			} else {
				must(t, s.reconcile(t.Context(), w, false))
				if w.ready {
					t.Fatal("changed policy did not await activation")
				}
				must(t, s.RefreshBranches(t.Context()))
			}
			must(t, s.reconcile(t.Context(), w, false))
			after, err := f.ListOpenPullRequests(t.Context(), repo, w.Key.BaseBranch)
			must(t, err)
			if len(after) != len(before) {
				t.Fatal("open PRs changed during policy reload")
			}
			for i, summary := range after {
				if summary != before[i] {
					t.Fatal("regression requires unchanged PR summaries")
				}
				data, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("pr/%d", summary.Number))
				must(t, err)
				var observation struct{ Decision queue.PRObserved }
				must(t, json.Unmarshal(data, &observation))
				if observation.Decision.Eligible {
					t.Fatalf("PR #%d retained stale policy eligibility", summary.Number)
				}
			}
		})
	}
}
