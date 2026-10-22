package serve

import (
	"context"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestAuthorityGithubAccountableIdentityRetriesEveryCycle(t *testing.T) {
	for _, optedIn := range []bool{false, true} {
		name := "ordinary"
		if optedIn {
			name = "authority opted in"
		}
		t.Run(name, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				w.AuthorityRequired = optedIn
				w.resolveAuthorityLifecycle = func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
					return authorityLifecycleResolution{RepositoryID: 123}, nil
				}
				f.SetIdentity(forge.UserIdentity{ID: 30, Login: "robot", Kind: forge.BotAccount})
				f.SetIdentity(forge.UserIdentity{ID: 31, Login: "human", Kind: forge.BotAccount})
				tip, err := f.PushBase(repo, "main", "github accountable identity", map[string]string{".openmerge.yml": githubApprovalsConfig + "agents: [{identity: robot, accountable: [human]}]\n"})
				must(t, err)
				for range 3 {
					before := f.IdentityCalls("ResolveUser")
					must(t, s.configuration(t.Context(), w))
					want := "Default-branch configuration: line 0: approvals: accountable identity \"human\" must resolve to a human"
					if got := lifecycleState(t, s, w).BlockSourceDetail(queue.ConfigSource); got != want {
						t.Fatalf("got %q; want %q", got, want)
					}
					if got := f.IdentityCalls("ResolveUser") - before; got != 2 {
						t.Fatalf("failed cycle resolved %d users; want 2 (main cadence)", got)
					}
					if w.configFailure != nil || !w.configFailureRetryAt.IsZero() {
						t.Fatal("github failure memoized", w.configFailure)
					}
				}
				f.SetIdentity(forge.UserIdentity{ID: 31, Login: "human", Kind: forge.UserAccount})
				before := f.IdentityCalls("ResolveUser")
				must(t, s.configuration(t.Context(), w))
				if got := f.IdentityCalls("ResolveUser") - before; got != 2 {
					t.Fatalf("repaired cycle resolved %d users; want 2 (main cadence)", got)
				}
				if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) || w.configTip != tip {
					t.Fatal("same-tip github repair blocked", w.configError)
				}
			})
		})
	}
}

func TestAuthorityGithubRollbackClearsMemoizedFailure(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		_, err := f.PushBase(repo, "main", "invalid authority owners", map[string]string{"CODEOWNERS": "/README.md @writer\n"})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		if w.configFailure == nil || !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("authority failure not memoized")
		}
		tip, err := f.PushBase(repo, "main", "rollback to github", map[string]string{".openmerge.yml": githubApprovalsConfig})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		if w.configCandidate.Authority.Mode.Normalized() != authority.GitHub || w.configFailure != nil || !w.configFailureRetryAt.IsZero() || w.configError != nil || w.configTip != tip || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("rollback retained authority failure", w.configError, w.configFailure)
		}
	})
}
