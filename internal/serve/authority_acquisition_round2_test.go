package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestAuthorityDiscoveryReusesUnchangedAcquisition(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, reads := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		tip, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		target := w.Target
		target.Key.Repository = strings.ToUpper(target.Key.Repository)
		if _, active := s.discoveryBindings(target); !active {
			t.Fatal("case-variant repository lost active binding")
		}
		_, err = s.repositoryConfig(t.Context(), w.Target, "main", tip, nil)
		must(t, err)
		files, protection, owners := f.GoverningFilesCalls(), *reads, f.IdentityCalls("WriteGrant")
		f.FailNext("DefaultBranch", context.DeadlineExceeded)
		_, err = s.repositoryConfig(t.Context(), target, "main", tip, nil)
		must(t, err)
		if _, err := f.DefaultBranch(t.Context(), repo); err != context.DeadlineExceeded {
			t.Fatal("discovery re-read known default branch", err)
		}
		if files != f.GoverningFilesCalls() || protection != *reads || owners != f.IdentityCalls("WriteGrant") {
			t.Fatal("unchanged discovery re-acquired evidence")
		}
		// Changing a binding at the same tip invalidates discovery's cached read.
		binding := *w.authorityDiscoveryPin.Load()
		binding.RepositoryID++
		w.authorityDiscoveryPin.Store(&binding)
		_, err = s.repositoryConfig(t.Context(), w.Target, "main", tip, nil)
		must(t, err)
		if *reads != protection+1 || f.GoverningFilesCalls() != files+1 {
			t.Fatal("changed binding reused acquisition")
		}
	})
}

func TestAuthorityDiscoveryHiddenBypassPreservesBaseDiscovery(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		original := w.resolveAuthorityLifecycle
		w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
			r, err := original(ctx, c, branch, tip)
			read := r.Activation.Read
			r.Activation.Read = func(ctx context.Context) (authority.Observation, error) {
				evidence, err := read(ctx)
				must(t, err)
				evidence = authority.CloneObservation(evidence)
				unrelated := evidence.Protections[0]
				unrelated.Source, unrelated.ID = "inherited_ruleset", "unrelated-org-policy"
				unrelated.Includes = []string{"refs/heads/unrelated"}
				evidence.Protections = append(evidence.Protections, unrelated)
				must(t, forge.ValidateAuthorityAcquisition(evidence))
				evidence.Protections[len(evidence.Protections)-1].BypassVisibility = authority.Hidden
				evidence.Protections[len(evidence.Protections)-1].BypassActors = []authority.BypassActor{}
				must(t, evidence.Validate())
				return evidence, nil
			}
			return r, err
		}
		target := w.Target
		s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Branches: func(ctx context.Context) ([]string, error) { return f.ListBranches(ctx, repo) }, Target: func(base string) Target { v := target; v.Key.BaseBranch = base; return v }}}
		tip, err := f.PushBase(repo, "main", "widen selectors", map[string]string{".openmerge.yml": lifecycleConfig(releaseConfig)})
		must(t, err)
		f.Init(repo, "release/v2", "openmerge/fence/release/v2")
		must(t, f.PushRef(repo, "release/v2", tip))
		if err := s.RefreshBranches(t.Context()); err == nil {
			t.Fatal("acquisition failure did not reach discovery backoff")
		}
		if s.refreshFailures == 0 || s.refreshRetryAt.IsZero() {
			t.Fatal("active acquisition failure lost retry backoff")
		}
		added := baseWorker(t, s, "release/v2")
		for _, worker := range []*worker{w, added} {
			state := lifecycleState(t, s, worker)
			if !state.SourceBlocked(queue.ConfigSource) || !strings.Contains(state.BlockSourceDetail(queue.ConfigSource), "protection") || worker.ready {
				t.Fatal("authority admission not blocked", worker.Key, state.BlockSourceDetail(queue.ConfigSource))
			}
			if _, err := s.readAuthorityAdmission(t.Context(), t.Context(), worker, state); err == nil {
				t.Fatal("authority admitted hidden protection")
			}
		}
		must(t, f.DeleteRef(t.Context(), repo, "release/v2"))
		if err := s.RefreshBranches(t.Context()); err == nil {
			t.Fatal("acquisition failure did not reach discovery backoff")
		}
		if s.refreshFailures == 0 || s.refreshRetryAt.IsZero() {
			t.Fatal("active acquisition failure lost retry backoff")
		}
		if lifecycleState(t, s, added).LifecycleState() != "dormant" {
			t.Fatal("base removal did not progress")
		}
	})
}

func TestAuthorityDefiniteConfigFailuresWaitForNewTip(t *testing.T) {
	for _, failure := range []string{"coverage", "multiple", "invalid", "transient"} {
		t.Run(failure, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := authorityWorkerFixture(t)
				must(t, s.configuration(t.Context(), w))
				validTip, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				changes := map[string]string{"CODEOWNERS": "/README.md @writer\n"}
				switch failure {
				case "multiple":
					changes = map[string]string{".github/openmerge.yml": lifecycleConfig(repositoryConfig)}
				case "invalid":
					changes = map[string]string{".openmerge.yml": "version: broken"}
				case "transient":
					changes = map[string]string{"README.md": "new tip"}
				}
				_, err = f.PushBase(repo, "main", failure, changes)
				must(t, err)
				if failure == "transient" {
					f.FailNext("GoverningFiles", context.DeadlineExceeded)
				}
				err = s.configuration(t.Context(), w)
				files := f.GoverningFilesCalls()
				if failure == "transient" {
					if err == nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
						t.Fatal("transient read became durable", err)
					}
					must(t, s.configuration(t.Context(), w))
					if f.GoverningFilesCalls() <= files || w.configError != nil {
						t.Fatal("transient failure did not retry")
					}
					return
				}
				must(t, err)
				if !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("missing durable configuration block")
				}
				if failure == "coverage" && !strings.Contains(w.configError.Error(), "owners") {
					t.Fatal("lost governing validation reason", w.configError)
				}
				if failure == "invalid" && (!strings.Contains(w.configError.Error(), "line 1") || !strings.Contains(w.configError.Error(), "version")) {
					t.Fatal("lost landed location", w.configError)
				}
				if failure == "multiple" && !strings.Contains(w.configError.Error(), "multiple OpenMerge") {
					t.Fatal("ambiguous config classified as unavailable")
				}
				// An attempted read at this same tip must remain queued, proving no
				// governing/owner/config read is needed to retain the durable block.
				f.FailNext("ReadFile", context.DeadlineExceeded)
				must(t, s.configuration(t.Context(), w))
				if f.GoverningFilesCalls() != files || !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("definite failure retried at same tip")
				}
				must(t, f.SetRef(repo, "main", validTip))
				_, err = f.PushBase(repo, "main", "repair", map[string]string{"CODEOWNERS": "* @writer\n", ".openmerge.yml": lifecycleConfig(repositoryConfig)})
				must(t, err)
				if err := s.configuration(t.Context(), w); err == nil {
					t.Fatal("queued read failure was consumed before tip changed")
				}
				must(t, s.configuration(t.Context(), w))
				if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("repaired tip did not clear durable block", w.configError)
				}

			})
		})
	}
}

func TestAuthorityMutableOwnersRecoverAtSameTip(t *testing.T) {
	for _, boundary := range []string{"worker", "discovery"} {
		t.Run(boundary, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := authorityWorkerFixture(t)
				must(t, s.configuration(t.Context(), w))
				target := w.Target
				s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Branches: func(ctx context.Context) ([]string, error) { return f.ListBranches(ctx, repo) }, Target: func(base string) Target { v := target; v.Key.BaseBranch = base; return v }}}
				refresh := func() {
					if boundary == "worker" {
						must(t, s.configuration(t.Context(), w))
					} else {
						_ = s.RefreshBranches(t.Context())
						must(t, s.configuration(t.Context(), w))
					}
				}
				f.SetWriteGrant(repo, 21, "read")
				// Force acquisition of live eligibility at a new policy tip, then repair the
				// grant without changing that tip.
				tip, err := f.PushBase(repo, "main", "recheck owners", map[string]string{"README.md": "grant test"})
				must(t, err)
				refresh()
				if !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("revoked grant did not block")
				}
				calls := f.IdentityCalls("WriteGrant")
				f.SetWriteGrant(repo, 21, "write")
				refresh()
				if f.IdentityCalls("WriteGrant") != calls || !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("mutable block bypassed retry cadence")
				}
				time.Sleep(s.options.RulesInterval)
				refresh()
				if w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
					t.Fatal("restored grant did not clear block", w.configError)
				}
				after, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				if after != tip {
					t.Fatal("tip changed")
				}
			})
		})
	}
}

func TestAuthorityDiscoveryContentBlockClearsOnRepairedTip(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		target := w.Target
		s.repositories = []Repository{{Installation: w.Key.Installation, Name: w.Key.Repository, Branches: func(ctx context.Context) ([]string, error) { return f.ListBranches(ctx, repo) }, Target: func(base string) Target { v := target; v.Key.BaseBranch = base; return v }}}
		_, err := f.PushBase(repo, "main", "coverage failure", map[string]string{"CODEOWNERS": "/README.md @writer\n"})
		must(t, err)
		must(t, s.RefreshBranches(t.Context()))
		must(t, s.configuration(t.Context(), w))
		if w.discoveryAuthorityError == nil || !lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("missing discovery block")
		}
		calls := f.GoverningFilesCalls()
		time.Sleep(2 * s.options.RulesInterval)
		must(t, s.RefreshBranches(t.Context()))
		if f.GoverningFilesCalls() != calls {
			t.Fatal("content failure retried at unchanged tip")
		}
		_, err = f.PushBase(repo, "main", "repair", map[string]string{"CODEOWNERS": "* @writer\n"})
		must(t, err)
		must(t, s.RefreshBranches(t.Context()))
		must(t, s.configuration(t.Context(), w))
		if w.discoveryAuthorityError != nil || w.configError != nil || lifecycleState(t, s, w).SourceBlocked(queue.ConfigSource) {
			t.Fatal("repaired discovery tip did not clear block", w.configError)
		}
	})
}

func TestAuthorityDiscoveryProtectionCacheExpires(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, reads := authorityWorkerFixture(t)
		must(t, s.configuration(t.Context(), w))
		tip, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		_, err = s.repositoryConfig(t.Context(), w.Target, "main", tip, nil)
		must(t, err)
		before := *reads
		original := w.resolveAuthorityLifecycle
		w.resolveAuthorityLifecycle = func(ctx context.Context, c config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
			r, err := original(ctx, c, branch, tip)
			read := r.Activation.Read
			r.Activation.Read = func(ctx context.Context) (authority.Observation, error) {
				e, err := read(ctx)
				if err != nil {
					return e, err
				}
				e = authority.CloneObservation(e)
				hidden := e.Protections[0]
				hidden.ID = "new-unrelated"
				hidden.Includes = []string{"refs/heads/unrelated"}
				hidden.BypassVisibility = authority.Hidden
				hidden.BypassActors = []authority.BypassActor{}
				e.Protections = append(e.Protections, hidden)
				return e, nil
			}
			return r, err
		}
		time.Sleep(s.options.RulesInterval - time.Nanosecond)
		_, err = s.repositoryConfig(t.Context(), w.Target, "main", tip, nil)
		must(t, err)
		if *reads != before {
			t.Fatal("warm TTL performed protection read")
		}
		time.Sleep(time.Nanosecond)
		_, err = s.repositoryConfig(t.Context(), w.Target, "main", tip, nil)
		if err == nil || *reads != before+1 {
			t.Fatal("expired cache missed hidden bypass", err)
		}
	})
}
