package gitops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Consume the push-validation contract through the executor, not a synthetic
// PreparedPush. Every attempt acquires its objects into an empty Git cache.
func TestAuthorityAcceptanceRejectedObjectsRemoteRefs(t *testing.T) {
	f := newFixture(t)
	initial, err := f.g.EnsureFence(t.Context(), testRepo, testFence, forge.PushOptions{})
	must(t, err)
	tested := f.build(forge.Squash, f.prs[:1], "acceptance-fence")
	for _, kind := range []string{"intermediate", "marker", "fence"} {
		t.Run(kind, func(t *testing.T) {
			f.g = f.client(t, 0)
			entries, err := os.ReadDir(f.g.cfg.WorkDir)
			must(t, err)
			for _, entry := range entries {
				if entry.Name() != "askpass" {
					t.Fatalf("acceptance cache already contains %s", entry.Name())
				}
			}
			before := gitCommand(t, "", "", nil, "ls-remote", "--refs", f.remote)
			assert := spyPushes(t, f)
			guards := 0
			x := executor.Executor{AllowNoRequiredChecks: true, Driver: fake.NewDeterministic(), NewID: func() (string, error) { return "acceptance-reject", nil }, Git: f.g, Repo: testRepo, Base: "main", Now: func() time.Time { return epoch }, PreparePush: func(_ context.Context, op forge.PushOperation, refs []forge.PushRefIntent) (forge.PushOptions, error) {
				pattern := "OpenMerge-Batch: acceptance-reject"
				if kind == "marker" {
					pattern = "PR 1"
				}
				if kind == "fence" {
					pattern = "OpenMerge-Batch"
				}
				opts := validationOptions(t, op, refs, []authority.Rule{{Kind: "commit_message_pattern", Parameters: []authority.Parameter{{Name: "operator", Values: []string{`"contains"`}}, {Name: "pattern", Values: []string{`"` + pattern + `"`}}}}})
				opts.Guard = func(context.Context, forge.PushValidationContext) error { guards++; return nil }
				return opts, nil
			}}
			events := []queue.Event{}
			emit := func(e queue.Event) error { events = append(events, e); return nil }
			if kind == "fence" {
				err = x.Execute(t.Context(), queue.Land{BatchID: "acceptance-fence", ExpectedBaseSHA: f.base, TestedSHA: tested, FenceToken: initial, Method: queue.Squash, PRs: []queue.BatchPR{{PR: 1, HeadSHA: f.heads[1]}}}, emit)
			} else {
				method := queue.Merge
				prs := []queue.BatchPR{{PR: 1, HeadSHA: f.heads[1]}, {PR: 2, HeadSHA: f.heads[2]}}
				if kind == "marker" {
					method = queue.FastForward
					prs = prs[:1]
				}
				err = x.Execute(t.Context(), queue.BuildTestRef{BatchID: "acceptance-reject", Ref: "openmerge/queue/main/acceptance-reject", BaseSHA: f.base, PRs: prs, Method: method}, emit)
			}
			if !errors.Is(err, forge.ErrInvalidInput) || !strings.Contains(err.Error(), "commit message") {
				t.Fatalf("expected actual-object message rejection, got %v", err)
			}
			if guards != 1 {
				t.Fatalf("preparation/final guards = %d, want only pre-inspection guard", guards)
			}
			for _, e := range events {
				switch e.(type) {
				case queue.TestRefBuilt, queue.FastForwardRefBuilt, queue.Landed:
					t.Fatalf("rejected object recorded success: %+v", e)
				}
			}
			if kind != "fence" {
				wantEvents := 2
				if kind == "marker" {
					wantEvents = 3
				}
				if len(events) != wantEvents {
					t.Fatalf("build journal=%+v", events)
				}
				if _, ok := events[len(events)-1].(queue.TestRefBuildFailed); !ok {
					t.Fatalf("missing failed-build event: %+v", events)
				}
			}
			assert()
			after := gitCommand(t, "", "", nil, "ls-remote", "--refs", f.remote)
			if before != after {
				t.Fatalf("remote advertised refs changed\nbefore:\n%s\nafter:\n%s", before, after)
			}
			// These explicit reads additionally prove that the atomic land never
			// published its otherwise-valid tested base when its new fence was refused.
			if got := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main"); got != f.base {
				t.Fatal("rejected write moved base")
			}
			if got := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence); got != initial {
				t.Fatal("rejected write moved fence")
			}
		})
	}
}
