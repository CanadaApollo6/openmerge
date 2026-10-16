package executor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type afterLandGit struct {
	forge.Git
	after func()
}

func (g *afterLandGit) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	fence, err := g.Git.Land(ctx, repo, req)
	if err == nil {
		g.after()
	}
	return fence, err
}

type closingDriver struct {
	forge.Driver
	calls int
}

func (d *closingDriver) MarkLanded(ctx context.Context, repo forge.RepoRef, pr int, sha string) error {
	d.calls++
	return d.Driver.MarkLanded(ctx, repo, pr, sha)
}

func TestLandingVerification(t *testing.T) {
	for _, stage := range []string{"land", "resolve", "mark-landed"} {
		for _, outcome := range []string{"tip", "descendant", "rollback", "rewrite", "read-failure", "ancestry-failure"} {
			t.Run(stage+"/"+outcome, func(t *testing.T) {
				h := setup(t)
				ctx := context.Background()
				tested, err := h.f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "test", BaseSHA: h.base, PRs: h.prs[:1], Method: forge.Squash, BatchID: "batch"})
				must(t, err)
				driver := &closingDriver{Driver: h.f}
				h.x.Driver = driver
				mutate := func() {
					switch outcome {
					case "descendant", "ancestry-failure":
						pr, err := h.f.OpenPR(repo, 99, tested, true)
						must(t, err)
						must(t, h.f.SetRef(repo, "main", pr.HeadSHA))
						if outcome == "ancestry-failure" {
							h.f.FailNext("IsAncestor", errIO)
						}
					case "rollback":
						must(t, h.f.SetRef(repo, "main", h.base))
					case "rewrite":
						must(t, h.f.SetRef(repo, "main", h.prs[1].HeadSHA))
					case "read-failure":
						h.f.FailNext("ReadRef", errIO)
					}
				}
				var action queue.Action
				if stage == "land" {
					h.x.Git = &afterLandGit{Git: h.f, after: mutate}
					action = queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: tested, FenceToken: h.fence, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}
				} else {
					_, err := h.f.Land(ctx, repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: h.base, TestedSHA: tested, Fence: fenceRef, ExpectedFence: h.fence, NextFenceID: "land/attempt", PRs: h.prs[:1]})
					must(t, err)
					mutate()
					action = queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: tested, FenceToken: h.fence}
					if stage == "mark-landed" {
						action = queue.MarkLanded{PR: 1, HeadSHA: h.prs[0].HeadSHA, LandedSHA: tested}
					}
				}
				var events []queue.Event
				err = h.x.Execute(ctx, action, record(&events))
				success := outcome == "tip" || outcome == "descendant"
				switch outcome {
				case "tip", "descendant":
					must(t, err)
				case "rollback", "rewrite":
					var absent *executor.ErrLandingNotOnBase
					attempt := "attempt"
					if stage == "mark-landed" {
						attempt = ""
					}
					if !errors.As(err, &absent) || absent.TestedSHA != tested || absent.BaseTip != h.read(t, "main") || absent.AttemptID != attempt {
						t.Fatalf("missing verification diagnostics: %v", err)
					}
				default:
					if !errors.Is(err, errIO) {
						t.Fatalf("verification lost infrastructure error: %v", err)
					}
				}
				results := events
				if stage == "land" {
					if len(events) == 0 {
						t.Fatal("missing LandingStarted")
					}
					if _, ok := events[0].(queue.LandingStarted); !ok {
						t.Fatalf("first event is %T", events[0])
					}
					results = events[1:]
				}
				wantResults, wantCalls := 0, 0
				if success {
					wantResults = 1
					if stage == "mark-landed" {
						wantCalls = 1
					}
				}
				if len(results) != wantResults || driver.calls != wantCalls {
					t.Fatalf("verification emitted events=%#v or called MarkLanded=%d", results, driver.calls)
				}
				pr, err := h.f.PullRequest(ctx, repo, 1)
				must(t, err)
				if pr.Closed != (wantCalls == 1) {
					t.Fatal("PR closure did not match verified MarkLanded")
				}
			})
		}
	}
}
