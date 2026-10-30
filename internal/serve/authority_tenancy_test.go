package serve

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func assertPublicTenancy(t *testing.T, err error) {
	t.Helper()
	var tenancy *store.AuthorityTenancyError
	if !errors.As(err, &tenancy) || err.Error() != "authority mode requires a dedicated database per installation: this database serves 2 installations" {
		t.Fatalf("expected distinct sanitized tenancy refusal: %v", err)
	}
}

func addOtherInstallation(t *testing.T, p publicFixture) {
	t.Helper()
	key := p.w.Key
	key.Installation = "private-second"
	key.Repository = "other/private"
	key.BaseBranch = "excluded"
	_, err := p.s.store.EnsureQueue(t.Context(), key)
	must(t, err)
}

func TestPublicAuthorityTenancyLifecycle(t *testing.T) {
	t.Parallel()
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-boundary", true: "after-boundary"}[verified], func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				p := newPublicFixture(t, verified)
				addOtherInstallation(t, p)
				// Tenancy has priority even over a missing proposed verification/receipt.
				_, err := p.w.resolveAuthorityLifecycle(t.Context(), p.w.configCandidate, "main", "")
				assertPublicTenancy(t, err)
				err = p.s.configuration(t.Context(), p.w)
				assertPublicTenancy(t, err)
				if p.probe.reads != 0 || *p.receiptReads != 0 {
					t.Fatal("refused activation performed seeing/receipt I/O")
				}
			})
		})
	}
}

func TestPublicAuthorityTenancyPushAndRollback(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		must(t, p.s.configuration(t.Context(), p.w))
		request := p.s.publicAuthorityRequest(p.w)
		must(t, p.s.RequireAuthorityActivation(t.Context(), request))
		options := p.s.authorityPushOptions(t.Context(), p.w, forge.PushLand, []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: lifecycleState(t, p.s, p.w).BaseSHA, NewSHA: p.w.configTip}})
		if options.Guard == nil {
			t.Fatal("missing public guard")
		}
		addOtherInstallation(t, p)
		branches := *p.branchReads
		assertPublicTenancy(t, p.s.RequireAuthorityActivation(t.Context(), request))
		if *p.branchReads != branches {
			t.Fatal("tenancy preflight performed completion discovery")
		}
		// A previously frozen push must consult tenancy anew.
		assertPublicTenancy(t, options.Guard(t.Context(), options.Context))
		_, err := p.f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": repositoryConfig})
		must(t, err)
		must(t, p.s.configuration(t.Context(), p.w))
		if pin := lifecycleState(t, p.s, p.w).AuthorityPin(); pin == nil || pin.Authority != authority.GitHub {
			t.Fatal("tenancy blocked rollback")
		}
		options = p.s.authorityPushOptions(t.Context(), p.w, forge.PushLand, nil)
		if options.AuthorityRequired || options.Guard != nil {
			t.Fatal("github rollback retained authority guard")
		}
	})
}

func TestPublicAuthorityTenancyFinalInventory(t *testing.T) {
	t.Parallel()
	for _, sameRepository := range []bool{false, true} {
		t.Run(map[bool]string{false: "other-repository", true: "completion-race"}[sameRepository], func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				p := newPublicFixture(t, true)
				must(t, p.s.configuration(t.Context(), p.w))
				original := p.s.repositories[0].Branches
				p.s.repositories[0].Branches = func(ctx context.Context) ([]string, error) {
					key := p.w.Key
					key.Installation = "private-second"
					if !sameRepository {
						key.Repository = "other/private"
					}
					_, err := p.s.store.EnsureQueue(t.Context(), key)
					must(t, err)
					p.s.repositories[0].Branches = original
					return original(ctx)
				}
				assertPublicTenancy(t, p.s.RequireAuthorityActivation(t.Context(), p.s.publicAuthorityRequest(p.w)))
			})
		})
	}
}

func TestPublicAuthorityTenancyGithubUnaffected(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		addOtherInstallation(t, p)
		_, err := p.f.PushBase(repo, "main", "github", map[string]string{".openmerge.yml": repositoryConfig})
		must(t, err)
		must(t, p.s.configuration(t.Context(), p.w))
		if p.probe.reads != 0 || *p.receiptReads != 0 || lifecycleState(t, p.s, p.w).AuthorityPin() != nil {
			t.Fatal("github mode consulted authority")
		}
		options := p.s.authorityPushOptions(t.Context(), p.w, forge.PushLand, nil)
		if options.AuthorityRequired || options.Guard != nil {
			t.Fatal("github mode gained guard")
		}
	})
}
