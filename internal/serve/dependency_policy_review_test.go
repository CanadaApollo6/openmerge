package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestDependencyActivationStillRefusesStageAAndProvenance(t *testing.T) {
	var token [32]byte
	token[0] = 6
	binding, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	pass := &dependencyPass{binding: binding,
		recover: func(context.Context, store.QueueID) error { return nil },
		landingStart: func(context.Context, queue.Land, queue.LandingStarted) (store.DependencyStart, error) {
			return store.DependencyStart{}, nil
		},
	}
	if err := pass.complete(&worker{}); err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]*worker{
		"Stage A":    {stageA: &stageAPolicy{}},
		"provenance": {provenance: &provenancePass{}},
	} {
		t.Run(name, func(t *testing.T) {
			if pass.complete(w) == nil {
				t.Fatal("unsupported dependency activation accepted")
			}
		})
	}
}

// The candidate remains unenforced public config. Reviewing the change to its
// policy file uses the existing governing owners, before adoption is possible.
func TestDependencySingleMaintainerOptInRequiresGoverningOwnerReview(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.reconcile(t.Context(), w, true))
		base := lifecycleState(t, s, w).BaseSHA
		candidate := "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues:\n  - name: default\n    dependencies: {mode: ordered, single_maintainer: true}\n    merge_when:\n      - check-success = ci\n"
		pr, err := f.OpenPRWithFiles(repo, 2, base, true, map[string]string{".openmerge.yml": candidate})
		must(t, err)
		f.SetNativeReviews(repo, 2, forge.NativeReviews{})
		must(t, s.reconcile(t.Context(), w, true))
		evaluate := func() cp.Evaluation {
			t.Helper()
			state := lifecycleState(t, s, w)
			r, _ := state.PRRevision(2)
			fact, err := state.RecordedBrief(w.stageA.policy, approvalBasis(r))
			must(t, err)
			identity, err := brief.Identity(fact)
			must(t, err)
			fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
			must(t, err)
			return state.EvaluateApprovals(w.stageA.policy, identity, fresh)
		}
		if got := evaluate(); got.Allowed || len(got.Requirements) == 0 {
			t.Fatalf("opt-in bypassed governing policy review: %+v", got)
		}
		f.SetNativeReviews(repo, 2, forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}})
		must(t, s.reconcile(t.Context(), w, false))
		if got := evaluate(); !got.Allowed {
			t.Fatalf("governing owner could not approve opt-in: %+v", got)
		}
		_, err = f.PushPR(repo, 2, "revise opt-in")
		must(t, err)
		must(t, s.reconcile(t.Context(), w, false))
		if got := evaluate(); got.Allowed {
			t.Fatal("old approval authorized a different opt-in revision")
		}
	})
}
