package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type failedHealthGit struct {
	*fake.Forge
	w        *worker
	failDiff bool
	before   *time.Time
}

func (g *failedHealthGit) fail() error {
	g.before = g.w.health.Load().LastProgressAt
	time.Sleep(time.Second)
	return errors.New("fixture Git read failure")
}

func (g *failedHealthGit) RevisionDiff(ctx context.Context, repo forge.RepoRef, req forge.RevisionDiffRequest) (forge.RevisionDiff, error) {
	if g.failDiff {
		return forge.RevisionDiff{}, g.fail()
	}
	return g.Forge.RevisionDiff(ctx, repo, req)
}

func (g *failedHealthGit) GoverningFiles(context.Context, forge.RepoRef, forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	return forge.GoverningFiles{}, g.fail()
}

func TestHealthStageAFailedGitRead(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"policy-governing", "diff", "classification-governing"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				w.publishCycleStarted(time.Now())
				g := &failedHealthGit{Forge: f, w: w, failDiff: mode == "diff"}
				w.Git = g
				w.stageA.governing = newStageAGoverningCache()
				var err error
				if mode == "policy-governing" {
					err = s.refreshStageAPolicy(t.Context(), w, w.configTip)
				} else {
					state := shadowState(t, s, w)
					r, ok := state.PRRevision(1)
					if !ok {
						t.Fatal("missing revision")
					}
					_, err = s.prepareStageAClassification(t.Context(), w, queue.State{}, r, w.configTip, false)
				}
				if err == nil || g.before == nil {
					t.Fatalf("failed read not exercised: %v", err)
				}
				if !w.health.Load().LastProgressAt.Equal(*g.before) {
					t.Fatal("failed Git read published progress")
				}
			})
		})
	}
}
