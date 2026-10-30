package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type completionCounter struct {
	authorityCompletionStore
	calls int
}

func (s *completionCounter) RepositoryAuthorityCompletion(ctx context.Context, req store.AuthorityCompletionRequest, inventory store.AuthorityBranchInventory) (store.AuthorityCompletion, error) {
	s.calls++
	return s.authorityCompletionStore.RepositoryAuthorityCompletion(ctx, req, inventory)
}

type completionDriver struct {
	forge.Driver
	calls int
	err   error
	errAt int
}

func (d *completionDriver) DefaultBranch(ctx context.Context, repo forge.RepoRef) (string, error) {
	d.calls++
	if d.err != nil && (d.errAt == 0 || d.calls == d.errAt) {
		return "", d.err
	}
	return d.Driver.DefaultBranch(ctx, repo)
}

type completionGit struct {
	forge.Git
	calls int
	err   error
	errAt int
	after func()
}

func (g *completionGit) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	g.calls++
	if g.err != nil && (g.errAt == 0 || g.calls == g.errAt) {
		return "", g.err
	}
	sha, err := g.Git.ReadRef(ctx, repo, ref)
	if g.after != nil {
		g.after()
	}
	return sha, err
}

type completionCallCounts struct {
	installations, repositories, branches int
	driver                                *completionDriver
	git                                   *completionGit
	durable                               *completionCounter
}

func instrumentCompletion(s *Server) *completionCallCounts {
	c := &completionCallCounts{durable: &completionCounter{authorityCompletionStore: s.store}}
	s.authorityCompletionStore = c.durable
	repo := s.repositories[0]
	target := repo.Target("")
	c.driver = &completionDriver{Driver: target.Driver}
	reader := target.AuthorityCompletionGit
	if reader == nil {
		reader = target.Git
	}
	c.git = &completionGit{Git: reader}
	target.Driver = c.driver
	target.AuthorityCompletionGit = c.git
	repo.Target = func(string) Target { return target }
	branches := repo.Branches
	repo.Branches = func(ctx context.Context) ([]string, error) { c.branches++; return branches(ctx) }
	s.discovery = &Discovery{Installations: func(context.Context) ([]Installation, error) {
		c.installations++
		return []Installation{{ID: repo.Installation, Repositories: func(context.Context) ([]Repository, error) { c.repositories++; return []Repository{repo}, nil }}}, nil
	}}
	return c
}
func (c *completionCallCounts) assert(t *testing.T, rounds int) {
	t.Helper()
	if c.installations != rounds || c.repositories != rounds || c.branches != 2*rounds || c.driver.calls != 2*rounds || c.git.calls != 2*rounds || c.durable.calls != 2*rounds {
		t.Fatalf("completion calls installations=%d repositories=%d branches=%d default=%d refs=%d durable=%d; rounds=%d", c.installations, c.repositories, c.branches, c.driver.calls, c.git.calls, c.durable.calls, rounds)
	}
}

func TestPublicAuthorityEveryPushConsultsCompletion(t *testing.T) {
	t.Parallel()
	for _, operation := range []forge.PushOperation{forge.PushBuild, forge.PushLand, forge.PushEnsureFence, forge.PushAdvanceFence, forge.PushDeleteBranch} {
		t.Run(string(operation), func(t *testing.T) {
			t.Parallel()
			testSynctest(t, func(t *testing.T) {
				f := newPublicFixture(t, true)
				must(t, f.s.configuration(t.Context(), f.w))
				state := lifecycleState(t, f.s, f.w)
				intents := map[forge.PushOperation][]forge.PushRefIntent{
					forge.PushBuild:        {{Ref: "refs/heads/openmerge/queue/main/probe", BuildBaseSHA: state.BaseSHA}},
					forge.PushLand:         {{Ref: "refs/heads/main", OldSHA: state.BaseSHA, NewSHA: state.BaseSHA}, {Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}},
					forge.PushEnsureFence:  {{Ref: "refs/heads/openmerge/fence/new"}},
					forge.PushAdvanceFence: {{Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}},
					forge.PushDeleteBranch: {{Ref: "refs/heads/feature", OldSHA: state.BaseSHA, Delete: true}},
				}
				opts := f.s.authorityPushOptions(t.Context(), f.w, operation, intents[operation])
				c := instrumentCompletion(f.s)
				must(t, opts.CheckDispatch(t.Context(), f.w.repo(), operation, opts.Context.RefIntents()))
				c.assert(t, 0)
				must(t, opts.Guard(t.Context(), opts.Context))
				c.assert(t, 1)
				// A competing partition is absent from local workers but invalidates ALL
				// publication families, including ones which never compose a test ref.
				key := f.w.Key
				key.BaseBranch = "release/hidden"
				_, err := f.s.store.EnsureQueue(t.Context(), key)
				must(t, err)
				if err := opts.Guard(t.Context(), opts.Context); err == nil {
					t.Fatal("publication skipped repository completion")
				}
				c.assert(t, 2)
			})
		})
	}
}

func TestPublicAuthorityCompletionRealLandCallBudget(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		f := acceptanceGitFixture(t, queue.Merge, 0)
		enableAcceptancePublic(t, f)
		cfg := f.w.plannerConfig(f.s.options.PollInterval)
		build := claimLandingChecksAction(t, f.s, f.w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
		must(t, f.s.execute(t.Context(), f.w, build))
		state := lifecycleState(t, f.s, f.w)
		batch := state.BatchForPR(1)
		refs := []forge.PushRefIntent{{Ref: "refs/heads/main", OldSHA: state.BaseSHA, NewSHA: batch.TestedSHA}, {Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}}
		opts := f.s.authorityPushOptions(t.Context(), f.w, forge.PushLand, refs)
		c := instrumentCompletion(f.s)
		_, err := f.g.Land(t.Context(), f.w.repo(), forge.LandRequest{Base: "main", ExpectedBaseSHA: state.BaseSHA, TestedSHA: batch.TestedSHA, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/completion-budget", PRs: f.prs}, opts)
		must(t, err)
		c.assert(t, 1)
	})
}

func TestPublicAuthorityCompletionPartitionRace(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"register", "append"} {
		t.Run(change, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				f := newPublicFixture(t, true, releaseConfig)
				must(t, f.s.configuration(t.Context(), f.w))
				state := lifecycleState(t, f.s, f.w)
				// A second durable partition has its own valid folded authority verdict.
				siblingKey := f.w.Key
				siblingKey.BaseBranch = "release/sibling"
				sibling, err := f.s.store.EnsureQueue(t.Context(), siblingKey)
				must(t, err)

				mainPin := *state.AuthorityPin()
				evidence := mainPin.OpenMerge.Activation.Evidence
				evidence.Coverage = append(evidence.Coverage, authority.Coverage{Ref: "refs/heads/" + siblingKey.BaseBranch, Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/base-10"}})
				for _, key := range []store.QueueKey{f.w.Key, siblingKey} {
					input := f.s.options.Authority.Targets[0].Input
					input.Binding.QueueKey = store.AuthorityQueueKey(key)
					input.ExpectedRefs = []string{"refs/heads/main", "refs/heads/" + siblingKey.BaseBranch}
					fresh, err := forge.InterpretWriteRestriction(input, evidence)
					must(t, err)
					fingerprint, err := fresh.Fingerprint()
					must(t, err)
					now := time.Now().UTC()
					proposal := queue.WriteRestrictionVerified{Binding: input.Binding, Scope: "proposed", Nonce: "sibling-complete", Operator: "operator", VerifiedAt: now, Evidence: fresh, ProtectionFingerprint: fingerprint}
					id, err := f.s.store.EnsureQueue(t.Context(), key)
					must(t, err)
					proposalID, err := f.s.store.Append(t.Context(), id, []queue.Event{proposal}, "")
					must(t, err)
					pin := mainPin
					pin.OpenMerge = &queue.AuthorityPin{Binding: input.Binding}
					pin = queue.BindAuthorityDraft(pin, proposalID, proposal, now, now, input, fresh)
					if !queue.AuthorityDraftAdopted(pin) {
						t.Fatal("sibling draft not adopted")
					}
					_, err = f.s.store.Append(t.Context(), id, []queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin}, "")
					must(t, err)
					if key == f.w.Key {
						f.w.authorityObservation.Input = input
					}
					if key == siblingKey {
						f.s.options.Authority.Targets = append(f.s.options.Authority.Targets, AuthorityTarget{Key: key, Input: input})
					}
				}
				f.s.repositories[0].Branches = func(context.Context) ([]string, error) { return []string{"main", siblingKey.BaseBranch}, nil }

				opts := f.s.authorityPushOptions(t.Context(), f.w, forge.PushAdvanceFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}})
				completion, err := f.s.AuthorityCompletion(t.Context(), f.s.publicAuthorityRequest(f.w))
				must(t, err)
				if !completion.Complete {
					t.Fatalf("sibling fixture incomplete: %+v", completion)
				}
				c := instrumentCompletion(f.s)
				c.git.after = func() {
					if c.git.calls == 2 {
						if change == "register" {
							key := f.w.Key
							key.BaseBranch = "release/competing"
							_, err := f.s.store.EnsureQueue(t.Context(), key)
							must(t, err)
						} else {
							// Ordinary sibling journal progress leaves the safety verdict unchanged.
							_, err := f.s.store.Append(t.Context(), sibling, []queue.Event{queue.BaseMoved{SHA: state.BaseSHA}}, "")
							must(t, err)
						}
					}
				}
				err = opts.Guard(t.Context(), opts.Context)
				if change == "append" {
					must(t, err)
				} else if err == nil || !strings.Contains(err.Error(), "durable inventory changed") {
					t.Fatalf("partition changed in final outbound read accepted: %v", err)
				}
				c.assert(t, 1)
			})
		})
	}
}

type completionQuotaError struct{ retry time.Time }

func (*completionQuotaError) Error() string           { return "SECRET https://token.invalid" }
func (e *completionQuotaError) RetryAfter() time.Time { return e.retry }
func TestPublicAuthorityCompletionErrorClasses(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"installations", "repositories", "branches", "branches-final", "default", "default-final", "ref", "ref-final", "durable"} {
		for _, kind := range []string{"quota", "canceled", "deadline"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				t.Parallel()
				testSynctest(t, func(t *testing.T) {
					f := newPublicFixture(t, true)
					must(t, f.s.configuration(t.Context(), f.w))
					state := lifecycleState(t, f.s, f.w)
					opts := f.s.authorityPushOptions(t.Context(), f.w, forge.PushAdvanceFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}})
					var cause error = &completionQuotaError{retry: time.Now().Add(time.Hour)}
					if kind == "canceled" {
						cause = context.Canceled
					}
					if kind == "deadline" {
						cause = context.DeadlineExceeded
					}
					c := instrumentCompletion(f.s)
					switch path {
					case "installations":
						f.s.discovery.Installations = func(context.Context) ([]Installation, error) { return nil, cause }
					case "repositories":
						f.s.discovery.Installations = func(context.Context) ([]Installation, error) {
							return []Installation{{ID: f.w.Key.Installation, Repositories: func(context.Context) ([]Repository, error) { return nil, cause }}}, nil
						}
					case "branches":
						r := f.s.repositories[0]
						r.Branches = func(context.Context) ([]string, error) { return nil, cause }
						f.s.discovery = nil
						f.s.repositories = []Repository{r}
					case "branches-final":
						r := f.s.repositories[0]
						branches := r.Branches
						calls := 0
						r.Branches = func(ctx context.Context) ([]string, error) {
							calls++
							if calls == 2 {
								return nil, cause
							}
							return branches(ctx)
						}
						f.s.discovery = nil
						f.s.repositories = []Repository{r}
					case "default-final":
						c.driver.err = cause
						c.driver.errAt = 2
					case "ref-final":
						c.git.err = cause
						c.git.errAt = 2
					case "default":
						c.driver.err = cause
					case "ref":
						c.git.err = cause
					case "durable":
						f.s.authorityCompletionStore = completionFailure{cause}
					}
					err := opts.Guard(t.Context(), opts.Context)
					if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "token.invalid") || !errors.Is(err, cause) {
						t.Fatalf("lost sanitized cause: %v", err)
					}
					if kind == "quota" {
						var quota interface{ RetryAfter() time.Time }
						if !errors.As(err, &quota) || !quota.RetryAfter().Equal(cause.(*completionQuotaError).retry) {
							t.Fatal("lost quota retry", err)
						}
					}
				})
			})
		}
	}
}

type completionFailure struct{ err error }

func (f completionFailure) RepositoryAuthorityCompletion(context.Context, store.AuthorityCompletionRequest, store.AuthorityBranchInventory) (store.AuthorityCompletion, error) {
	return store.AuthorityCompletion{}, f.err
}

type reconcileDefaultProbe struct {
	*publicAuthorityProbe
	calls int
}

func (p *reconcileDefaultProbe) DefaultBranch(ctx context.Context, repo forge.RepoRef) (string, error) {
	p.calls++
	return p.publicAuthorityProbe.DefaultBranch(ctx, repo)
}

type reconcileGitProbe struct {
	*fake.Forge
	calls int
}

func (p *reconcileGitProbe) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	p.calls++
	return p.Forge.ReadRef(ctx, repo, ref)
}

func TestPublicAuthorityReconcileInventoryBudget(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		p := newPublicFixture(t, true)
		must(t, p.s.configuration(t.Context(), p.w))
		c := instrumentCompletion(p.s)
		defaults := &reconcileDefaultProbe{publicAuthorityProbe: p.probe}
		p.w.Driver = defaults
		refs := &reconcileGitProbe{Forge: p.f}
		p.w.Git = refs
		teamReads := p.f.IdentityCalls("ResolveTeam")
		protectionReads := p.probe.reads
		// Unchanged governing policy still needs fresh branch membership to notice
		// selector matches introduced without a change to the default branch tip.
		for n := 1; n <= 2; n++ {
			must(t, p.s.configuration(t.Context(), p.w))
			if defaults.calls != n || refs.calls != n || p.f.IdentityCalls("ResolveTeam")-teamReads != n || p.probe.reads != protectionReads {
				t.Fatalf("reconcile %d default=%d ref=%d team=%d protection=%d", n, defaults.calls, refs.calls, p.f.IdentityCalls("ResolveTeam")-teamReads, p.probe.reads-protectionReads)
			}
			if c.installations != n || c.repositories != n || c.branches != n || c.driver.calls != 0 || c.git.calls != 0 || c.durable.calls != 0 {
				t.Fatalf("reconcile %d: installations=%d repositories=%d branches=%d default=%d refs=%d durable=%d", n, c.installations, c.repositories, c.branches, c.driver.calls, c.git.calls, c.durable.calls)
			}
		}
	})
}
