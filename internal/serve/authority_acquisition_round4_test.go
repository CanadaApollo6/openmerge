package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestAuthorityAccountableIdentityRecoversAtSameTip(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		f.SetIdentity(forge.UserIdentity{ID: 30, Login: "robot", Kind: forge.BotAccount})
		f.SetIdentity(forge.UserIdentity{ID: 31, Login: "human", Kind: forge.BotAccount})
		raw := lifecycleConfig(repositoryConfig) + "agents: [{identity: robot, accountable: [human]}]\n"
		tip, err := f.PushBase(repo, "main", "accountable identity", map[string]string{".openmerge.yml": raw})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		var mutable *config.GoverningIdentityError
		if !errors.As(w.configError, &mutable) || w.configFailureRetryAt.IsZero() {
			t.Fatalf("live identity failure has no retry: %v", w.configError)
		}
		want := "Default-branch configuration: approvals: accountable identity \"human\" must resolve to a human"
		if got := lifecycleState(t, s, w).BlockSourceDetail(queue.ConfigSource); got != want {
			t.Fatalf("got %q; want %q", got, want)
		}
		f.SetIdentity(forge.UserIdentity{ID: 31, Login: "human", Kind: forge.UserAccount})
		calls := f.IdentityCalls("ResolveUser")
		must(t, s.configuration(t.Context(), w))
		if f.IdentityCalls("ResolveUser") != calls {
			t.Fatal("retry gate bypassed")
		}
		time.Sleep(2 * s.options.RulesInterval)
		must(t, s.configuration(t.Context(), w))
		if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("same-tip repair blocked", w.configError)
		}
		after, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		if after != tip {
			t.Fatal("tip moved")
		}
	})
}

func TestAuthorityMissingOwnerBoundsReadsAndRecovers(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"user", "team"} {
		t.Run(kind, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := authorityWorkerFixture(t)
				must(t, s.configuration(t.Context(), w))
				owner, method := "@missing", "ResolveUser"
				if kind == "team" {
					owner, method = "@owner/missing", "ResolveTeam"
				}
				_, err := f.PushBase(repo, "main", "missing owner", map[string]string{"CODEOWNERS": "* " + owner + "\n"})
				must(t, err)
				must(t, s.configuration(t.Context(), w))
				if !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) || w.configFailureRetryAt.IsZero() {
					t.Fatal("missing mutable owner block", w.configError)
				}
				files, calls := f.GoverningFilesCalls(), f.IdentityCalls(method)
				for range 3 {
					time.Sleep(s.options.RulesInterval / 4)
					must(t, s.configuration(t.Context(), w))
					if files != f.GoverningFilesCalls() || calls != f.IdentityCalls(method) {
						t.Fatal("missing owner repeatedly read inside retry gate")
					}
				}
				time.Sleep(s.options.RulesInterval / 4)
				must(t, s.configuration(t.Context(), w))
				if f.GoverningFilesCalls() != files+1 || f.IdentityCalls(method) != calls+1 {
					t.Fatal("deadline did not retry once")
				}
				user := forge.UserIdentity{ID: 32, Login: "missing", Kind: forge.UserAccount}
				f.SetIdentity(user)
				f.SetWriteGrant(repo, 32, "write")
				if kind == "team" {
					f.SetTeam(forge.TeamIdentity{ID: 33, OrganizationID: 34, Organization: "owner", Slug: "missing"}, []forge.UserIdentity{user})
				}
				time.Sleep(s.options.RulesInterval)
				must(t, s.configuration(t.Context(), w))
				if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("reappeared owner blocked", w.configError)
				}
			})
		})
	}
}

func TestAuthorityDiscoveryPreservesMutableFailureDeadline(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		tip, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		target := w.Target
		s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(string) Target { return target }}}
		failure := &config.GoverningIdentityError{Cause: config.Errors{{NoLine: true, Path: "approvals", Message: "mutable identity"}}}
		deadline := time.Now().Add(s.options.RulesInterval)
		w.configFailure, w.configFailureBranch, w.configFailureTip, w.configFailureRetryAt = failure, "main", tip, deadline
		bindings, _ := s.discoveryBindings(target)
		s.rememberDiscoveryAcquisition(target.Key.Installation+"/"+target.Key.Repository, discoveryAcquisition{branch: "main", tip: tip, bindings: bindings, err: failure})
		for range 3 {
			time.Sleep(s.options.RulesInterval / 4)
			if err := s.RefreshBranches(context.Background()); err == nil {
				t.Fatal("expected discovery failure")
			}
			if w.configFailureRetryAt != deadline {
				t.Fatal("discovery changed mutable deadline", w.configFailureRetryAt, deadline)
			}
		}
	})
}
