package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type startGit struct {
	forge.Git
	calls int
}

func (g *startGit) BuildTestRef(ctx context.Context, repo forge.RepoRef, request forge.BuildRequest) (string, error) {
	g.calls++
	return g.Git.BuildTestRef(ctx, repo, request)
}

func (g *startGit) Land(ctx context.Context, repo forge.RepoRef, request forge.LandRequest) (string, error) {
	g.calls++
	return g.Git.Land(ctx, repo, request)
}

// Port of the reviewer's crash-after-BatchStarted/pre-build reproducer, with
// every member invalidation exercised both on initial execution and recovery.
func TestRetryBuildExecutesRevokedMember(t *testing.T) {
	for _, window := range []string{"initial", "reclaimed-after-start"} {
		for _, change := range []string{"trust", "eligibility", "head", "close"} {
			t.Run(window+"/"+change, func(t *testing.T) {
				s := newStore(t)
				q := ensureQueue(t, s, "")
				cfg := config
				cfg.BatchMaxSize = 2
				f := fake.New()
				repo := forge.RepoRef{Path: "owner/repo"}
				base, fence := f.Init(repo, "main", "openmerge/fence/main")
				must(t, s.InitializeFence(t.Context(), q, fence))
				appendEvents(t, s, q, queue.BaseMoved{SHA: base})
				for pr := 1; pr <= 2; pr++ {
					opened, err := f.OpenPR(repo, pr, base, pr == 1)
					must(t, err)
					if pr == 2 {
						must(t, f.Approve(repo, pr, forge.Approval{User: "writer", SHA: opened.HeadSHA, CanWrite: true}))
					}
					appendEvents(t, s, q, queue.PRObserved{PR: pr, HeadSHA: opened.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: pr, At: epoch})
				}
				plans, err := s.Plan(t.Context(), q, cfg, epoch)
				must(t, err)
				build := plans[0].Action.(queue.BuildTestRef)
				claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
				must(t, err)
				proceed, err := s.BeginConsequential(t.Context(), claims[0], cfg, epoch)
				must(t, err)
				if !proceed {
					t.Fatal("initial build rejected")
				}
				now := epoch
				if window == "reclaimed-after-start" {
					must(t, s.Emit(t.Context(), claims[0], cfg)(queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: now}))
					// Crash before forge I/O; replacement reclaims the expired lease.
					now = now.Add(2 * time.Minute)
					claims, err = s.Claim(t.Context(), 1, time.Minute, now)
					must(t, err)
					proceed, err = s.BeginConsequential(t.Context(), claims[0], cfg, now)
					must(t, err)
					if !proceed {
						t.Fatal("unchanged retry rejected")
					}
				}
				observation := queue.PRObserved{PR: 2, HeadSHA: build.PRs[1].HeadSHA, Eligible: true, Trusted: true}
				var event queue.Event
				switch change {
				case "trust":
					must(t, f.Approve(repo, 2, forge.Approval{User: "writer", SHA: observation.HeadSHA, CanWrite: false}))
					observation.Trusted = false
				case "eligibility":
					observation.Eligible = false
				case "head":
					updated, err := f.PushPR(repo, 2, "new head")
					must(t, err)
					observation.HeadSHA = updated.HeadSHA
				case "close":
					event = queue.PRClosed{PR: 2}
				}
				if event == nil {
					event = observation
				}
				// Invalidation commits after BeginConsequential and before emit.
				watermark := appendEvents(t, s, q, event)
				git := &startGit{Git: f}
				x := executor.Executor{Driver: f, Git: git, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return now }}
				err = x.Execute(t.Context(), build, s.Emit(t.Context(), claims[0], cfg))
				assertStaleStart(t, s, q, claims[0], watermark, git.calls, err)
			})
		}
	}
}

func TestLandingStartRevalidatesAfterBegin(t *testing.T) {
	for _, change := range []string{"dequeue-requested", "dequeued", "tested-sha", "fence"} {
		t.Run(change, func(t *testing.T) {
			s := newStore(t)
			q, claim := readyLand(t, s)
			proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("landing rejected")
			}
			var watermark int64
			switch change {
			case "dequeue-requested":
				watermark = appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
			case "dequeued":
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				watermark = appendEvents(t, s, q, queue.DequeueRequested{PR: 1}, queue.Dequeued{PR: 1, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested})
			case "tested-sha", "fence":
				// Keys alone do not capture Land's complete authorization payload.
				land := claim.Action.(queue.Land)
				if change == "tested-sha" {
					land.TestedSHA = "another-tested-sha"
				} else {
					land.FenceToken = "another-fence"
				}
				_, payload, err := queue.EncodeAction(land)
				must(t, err)
				_, err = s.pool.Exec(t.Context(), `UPDATE actions SET payload = $1::jsonb WHERE queue_id = $2 AND key = $3`, string(payload), q, claim.Key)
				must(t, err)
				claim.Action = land
				_, watermark, err = s.Load(t.Context(), q)
				must(t, err)
			}
			f := fake.New()
			git := &startGit{Git: f}
			x := executor.Executor{Driver: f, Git: git, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
			land := claim.Action.(queue.Land)
			f.SetCheckRuns(x.Repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: land.Ref}})
			err = x.Execute(t.Context(), claim.Action, s.Emit(t.Context(), claim, config))
			assertStaleStart(t, s, q, claim, watermark, git.calls, err)
		})
	}
}

func assertStaleStart(t *testing.T, s *Store, q QueueID, claim ClaimedAction, watermark int64, calls int, err error) {
	t.Helper()
	if !errors.Is(err, ErrStale) || calls != 0 {
		t.Fatalf("stale start: forge calls=%d, error=%v", calls, err)
	}
	_, after, err := s.Load(t.Context(), q)
	must(t, err)
	if after != watermark {
		t.Fatalf("rejected start appended an event: %d -> %d", watermark, after)
	}
	if count(t, s, `SELECT count(*) FROM actions WHERE queue_id=$1 AND key=$2 AND status='cancelled' AND attempt_id IS NULL AND lease_until IS NULL`, q, claim.Key) != 1 {
		t.Fatal("stale action was not cancelled")
	}
}
