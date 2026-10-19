package serve

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStageAMixedRepositoryActivation(t *testing.T) {
	for _, depth := range []int{1, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			f := fake.New()
			f.Init(repo, "main", "openmerge/fence/main")
			source := fmt.Sprintf("version: 1\nclassification: {docs: ['**/*.md']}\nqueues:\n- name: opted\n  base: main\n  approvals: {default: any_writer}\n  speculation: {max_depth: %d, min_depth: %d}\n  merge_when: [check-success = ci]\n- name: legacy\n  base: release\n  speculation: {max_depth: %d, min_depth: %d}\n  merge_when: [check-success = ci]\n", depth, depth, depth, depth)
			tip, err := f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": source})
			must(t, err)
			must(t, f.PushRef(repo, "release", tip))
			f.Init(repo, "release", "openmerge/fence/release")
			for i, base := range []string{"main", "release"} {
				_, err := f.OpenPR(repo, i+1, tip, true)
				must(t, err)
				must(t, f.SetPRMetadata(repo, i+1, base, false, "contributor"))
				f.SetNativeReviews(repo, i+1, forge.NativeReviews{})
			}
			f.SetIdentity(forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount})
			f.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
			f.SetWriteGrant(repo, 21, "write")
			f.SetCanWrite(repo, "writer", true)
			targets := []Target{}
			for _, base := range []string{"main", "release"} {
				targets = append(targets, Target{Key: store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: base}, Driver: f, Git: f, BotLogin: fake.Identity})
			}
			s, err := New(t.Context(), database(t), targets, Options{PollInterval: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			must(t, err)
			opted, legacy := s.queues[0], s.queues[1]
			for _, w := range []*worker{opted, legacy} {
				must(t, s.reconcile(t.Context(), w, true))
			}
			if opted.stageA == nil || legacy.stageA != nil {
				t.Fatal("opt-in leaked across sibling queues")
			}
			// Compare the sibling against the original public loader's effective policy.
			cfg, _, err := config.Load([]byte(source))
			must(t, err)
			selected, diags := cfg.ResolveBranch("release", "main")
			if len(diags) != 0 {
				t.Fatal(diags)
			}
			before := shadowState(t, s, legacy).ConfigDigest
			if before != selected.Digest(true) {
				t.Fatal("legacy digest changed")
			}
			for _, w := range []*worker{opted, legacy} {
				pr := 1
				if w == legacy {
					pr = 2
				}
				if w == legacy {
					f.AddComment(repo, pr, "writer", "@openmerge queue")
				}
				f.SetNativeReviews(repo, pr, shadowNative(f, w, shadowComment(int64(100+pr), "@openmerge queue")))
				must(t, s.reconcile(t.Context(), w, true))
				batch := shadowBuildBatch(t, s, w, pr)
				shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
				shadowRefreshChecks(t, s, w)
				if w == opted {
					shadowRejectLanding(t, s, w)
				} else {
					shadowLandBatch(t, s, w)
				}
			}
			watermark, err := s.store.Watermark(t.Context(), legacy.id)
			must(t, err)
			optedDigest := opted.configDigest
			changedPolicy := strings.Replace(source, "default: any_writer", "default: {any_writer: true, count: 2}", 1)
			_, err = f.PushBase(repo, "main", "approval policy changed", map[string]string{".openmerge.yml": changedPolicy})
			must(t, err)
			must(t, s.configuration(t.Context(), opted))
			must(t, s.configuration(t.Context(), legacy))
			after, err := s.store.Watermark(t.Context(), legacy.id)
			must(t, err)
			if after != watermark || opted.configDigest == optedDigest || legacy.stageA != nil {
				t.Fatal("approval-policy refresh leaked into legacy sibling")
			}
			rows, err := s.store.EventsAfter(t.Context(), legacy.id, 0)
			must(t, err)
			for _, row := range rows {
				switch event := row.Event.(type) {
				case queue.ChangeRevisionObserved, queue.ChangeClassified, queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ApprovalEligibilityObserved, queue.ReviewStanceObserved, queue.ApprovalReceiptRecorded, queue.LandingDecisionRecorded, queue.ApprovalGateRejected, queue.ApprovalInputsChanged:
					t.Fatalf("legacy Stage A observation: %T", event)
				case queue.ConfigApplied:
					if event.Policy != nil {
						t.Fatal("legacy config gained policy pins")
					}
				}
			}
			if shadowState(t, s, legacy).ConfigDigest != before {
				t.Fatal("legacy digest refreshed by sibling")
			}
			landed, err := f.ReadRef(t.Context(), repo, "release")
			must(t, err)
			commit, err := f.Commit(repo, landed)
			must(t, err)
			if strings.Contains(commit.Message, "OpenMerge-Change:") || strings.Contains(commit.Message, "OpenMerge-Decision:") {
				t.Fatal("legacy landing gained trailers")
			}
		})
	}
}
