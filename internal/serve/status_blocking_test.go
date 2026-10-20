package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

type slowStatusGit struct {
	*fake.Forge
	phase            string
	entered, release chan struct{}
	once             sync.Once
}

func (g *slowStatusGit) wait(ctx context.Context) error {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *slowStatusGit) ReadCommitMessage(ctx context.Context, repo forge.RepoRef, sha string) (string, error) {
	if g.phase == "gate" {
		if err := g.wait(ctx); err != nil {
			return "", err
		}
	}
	return g.Forge.ReadCommitMessage(ctx, repo, sha)
}

func (g *slowStatusGit) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	if g.phase == "landing" {
		if err := g.wait(ctx); err != nil {
			return "", err
		}
	}
	return g.Forge.Land(ctx, repo, req)
}

func TestApprovalStatusDuringGateAndLanding(t *testing.T) {
	for _, phase := range []string{"gate", "landing"} {
		t.Run(phase, func(t *testing.T) {
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			s.discoveryStatus.Repositories = []status.Repository{{Installation: "7", Name: repo.Path, State: "served"}}
			id, err := w.Key.ChangeID(1)
			must(t, err)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			f.SetNativeReviews(repo, 1, forge.NativeReviews{
				Reviews:  []forge.NativeReview{{ID: 100, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}},
				Comments: []forge.NativeComment{shadowComment(1000, "@openmerge queue")},
			})
			batch := shadowBuildBatch(t, s, w, 1)
			shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
			must(t, err)
			g := &slowStatusGit{Forge: f, phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
			w.Git = g
			done := make(chan error, 1)
			go func() { done <- s.cycle(t.Context(), w) }()
			defer func() {
				close(g.release)
				must(t, <-done)
				state := shadowState(t, s, w)
				if len(state.Entries) != 0 || state.Active != nil {
					t.Fatalf("queue did not land after releasing slow %s: %+v", phase, state)
				}
			}()
			select {
			case <-g.entered:
			case err := <-done:
				// Keep deferred cleanup from waiting for a consumed result.
				done <- err
				t.Fatalf("cycle stopped before slow %s: %v", phase, err)
			case <-time.After(5 * time.Second):
				t.Fatal("cycle did not reach slow step")
			}
			handler := status.DynamicHandler(s, s.Keys, "", nil, s.DiscoveryStatus)
			for _, path := range []string{"/v1/queues/owner/repo/main/prs/1", "/v1/queues/owner/repo/main", "/v1/queues", "/v1/discovery", "/v1/queues/owner/repo/main/changes/" + string(id), "/v1/queues/owner/repo/main/changes/" + string(id) + "/brief?audience=reviewer"} {
				response := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					rr := httptest.NewRecorder()
					handler.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
					response <- rr
				}()
				select {
				case rr := <-response:
					if rr.Code != 200 {
						t.Fatalf("%s: HTTP %d: %s", path, rr.Code, rr.Body)
					}
					if !strings.Contains(path, "/brief?") {
						var view status.Response
						must(t, json.Unmarshal(rr.Body.Bytes(), &view))
						if view.PR != nil && (view.PR.HeadSHA != pr.HeadSHA || view.PR.Change == nil) {
							t.Fatalf("committed PR evidence lost: %s", rr.Body)
						}
						if view.Queue != nil && (view.Queue.Readiness == nil || view.Queue.Readiness.Reason != "policy_busy" || view.Queue.Window != 1) {
							t.Fatalf("busy response lost policy or reason: %s", rr.Body)
						}
					}
					if strings.Contains(path, "/prs/") || strings.Contains(path, "/changes/") {
						if !strings.Contains(rr.Body.String(), "policy_busy") {
							t.Fatalf("missing busy diagnostic: %s", rr.Body)
						}
					}
				case <-time.After(200 * time.Millisecond):
					t.Fatalf("%s blocked behind slow %s while worker mutex held", path, phase)
				}
			}
		})
	}
}
