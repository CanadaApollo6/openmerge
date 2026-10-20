package serve

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type bootstrapDriver struct {
	forge.Driver
	read func(string, ...string) string
}

func (d bootstrapDriver) DefaultBranch(context.Context, forge.RepoRef) (string, error) {
	return "main", nil
}
func (d bootstrapDriver) ReadFile(_ context.Context, _ forge.RepoRef, ref, path string) ([]byte, error) {
	if path != ".openmerge.yml" {
		return nil, forge.ErrFileNotFound
	}
	return []byte(d.read("", "show", ref+":"+path)), nil
}

type bootstrapGit struct {
	forge.Git
	before func(forge.PushOptions) forge.PushOptions
	calls  int
}

func (g *bootstrapGit) EnsureFence(ctx context.Context, r forge.RepoRef, ref string, opts forge.PushOptions) (string, error) {
	g.calls++
	return g.Git.EnsureFence(ctx, r, ref, g.before(opts))
}

func TestAuthorityBootstrapPinsBeforeFirstRealFence(t *testing.T) {
	for _, path := range []string{"registration", "refreshWorker", "cycle"} {
		for _, mode := range []string{"none", "basis", "policy", "reload", "missing-seeing", "seeing-drift", "runtime-hidden"} {
			change := mode != "none" && mode != "runtime-hidden"
			t.Run(fmt.Sprintf("%s/change-%s", path, mode), func(t *testing.T) {
				db := database(t)
				f := newCompositionFixture(t, []int{1})
				ctx := t.Context()
				blob := f.git(repositoryConfig, "--git-dir="+f.remote, "hash-object", "-w", "--stdin")
				tree := f.git("100644 blob "+blob+"\t.openmerge.yml\n", "--git-dir="+f.remote, "mktree")
				tip := f.git("governing config\n", "--git-dir="+f.remote, "commit-tree", tree, "-p", f.base)
				f.git("", "--git-dir="+f.remote, "update-ref", "refs/heads/main", tip)
				realGit, err := exec.LookPath("git")
				must(t, err)
				dir := t.TempDir()
				pushLog := filepath.Join(dir, "pushes")
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
				script := "#!/bin/sh\nfor a do\nif [ \"$a\" = push ]; then\nprintf push >> " + quote(pushLog) + "\nfi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
				must(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700))
				t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
				g, err := gitops.New(ctx, gitops.Config{WorkDir: filepath.Join(dir, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return f.remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
				must(t, err)
				key := f.w.Key
				id, _, err := db.CanonicalQueue(ctx, key)
				must(t, err)
				cfg, _, err := config.Load([]byte(repositoryConfig))
				must(t, err)
				digest := cfg.Queues[0].Digest(true)
				raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
				must(t, err)
				event, err := queue.DecodeEvent(raw)
				must(t, err)
				pin := event.(queue.PolicyAuthorityApplied)
				pin.GoverningCommit = tip
				pin.EffectiveDigest = digest
				pin.DefaultBranch = "main"
				pin.OpenMerge.Binding.Repository = key.Repository
				pin.OpenMerge.Binding.QueueKey = key.Installation + "/" + key.Repository + "/" + key.BaseBranch
				pin.OpenMerge.Binding.InstallationID = 7
				pin.OpenMerge.Binding.EffectiveDigest = digest
				pin.OpenMerge.Binding.DefaultBranch = "main"
				must(t, pin.Validate())
				evidence := authority.Observation{Inventory: authority.Visible, Protections: []authority.Protection{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Absent, ProtectionIDs: []string{}}}, Findings: []authority.Finding{}}
				if mode == "runtime-hidden" {
					evidence.Protections = []authority.Protection{{ID: "pinned", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible, Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: []authority.Rule{}, BypassActors: []authority.BypassActor{}}}
				}
				fp, err := evidence.Fingerprint()
				must(t, err)
				seeing := time.Now().UTC()
				pin.OpenMerge.Activation = &authority.Activation{Proposed: authority.ProposedVerification{EventID: 1, Scope: "proposed", Binding: pin.OpenMerge.Binding, ProtectionFingerprint: fp, SeeingReadAt: seeing}, ProtectionFingerprint: fp, SeeingReadAt: seeing, Evidence: evidence}
				if mode == "missing-seeing" {
					pin.OpenMerge.Activation = nil
				}
				must(t, pin.Validate())
				policy := AuthorityPushPolicy{Mode: authority.OpenMerge, GoverningCommit: tip, EffectiveDigest: digest, AppID: "42"}
				order := []string{}
				observed := func(basis uint64) error {
					readEvidence := authority.CloneObservation(evidence)
					if mode == "runtime-hidden" {
						readEvidence.Protections[0].BypassVisibility = authority.Hidden
					}
					if mode == "seeing-drift" {
						readEvidence.Protections = []authority.Protection{{ID: "new", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible, Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: []authority.Rule{}, BypassActors: []authority.BypassActor{}}}
					}
					_, err := db.Append(ctx, id, []queue.Event{queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: basis, ObservedAt: time.Now().UTC(), Evidence: readEvidence}}, "")
					return err
				}
				proxy := &bootstrapGit{Git: g}
				proxy.before = func(opts forge.PushOptions) forge.PushOptions {
					order = append(order, "ensure")
					if !opts.AuthorityRequired || opts.Guard == nil {
						t.Fatal("bootstrap lost required options")
					}
					state, _, err := db.Load(ctx, id)
					must(t, err)
					_, _, ok := state.AuthorityProtection()
					if state.AuthorityPin() == nil || !ok {
						t.Fatal("first fence preceded pin or observation")
					}
					original := opts.Guard
					calls := 0
					opts.Guard = func(ctx context.Context, c forge.PushValidationContext) error {
						calls++
						if calls == 2 {
							switch mode {
							case "basis":
								must(t, observed(2))
							case "policy":
								_, err := db.Append(ctx, id, []queue.Event{queue.ConfigApplied{Commit: tip, Digest: strings.Repeat("d", 64)}}, "")
								must(t, err)
							case "reload":
								db.Close()
							}
						}
						return original(ctx, c)
					}
					return opts
				}
				target := Target{Key: key, Git: proxy, Driver: bootstrapDriver{Driver: fake.New(), read: func(input string, args ...string) string {
					return f.git(input, append([]string{"--git-dir=" + f.remote}, args...)...)
				}}, BotLogin: fake.Identity,
					PrepareAuthorityPush: func(context.Context) (AuthorityPushPolicy, error) {
						order = append(order, "resolve")
						return policy, nil
					},
					PinAuthorityPush: func(context.Context, AuthorityPushPolicy) error {
						order = append(order, "pin")
						state, _, err := db.Load(ctx, id)
						if err != nil {
							return err
						}
						if state.AuthorityPin() != nil {
							return nil
						}
						_, err = db.Append(ctx, id, []queue.Event{queue.ConfigApplied{Commit: tip, Digest: digest}, pin}, "")
						return err
					},
					ObserveAuthorityPush: func(context.Context, AuthorityPushPolicy) error {
						order = append(order, "observe")
						state, _, err := db.Load(ctx, id)
						if err != nil {
							return err
						}
						if state.AuthorityPin() == nil {
							t.Fatal("observation before pin")
						}
						return observed(1)
					},
				}
				var s *Server
				if path == "registration" {
					s, err = New(ctx, db, []Target{target}, Options{})
				} else {
					s, err = newServer(db, Options{})
					must(t, err)
					w, registerErr := s.register(ctx, target)
					must(t, registerErr)
					if proxy.calls != 0 {
						t.Fatal("registration unexpectedly created fence")
					}
					if path == "refreshWorker" {
						err = s.refreshWorker(ctx, w, cfg, "main", tip, []string{"main"}, map[string]bool{})
					} else {
						w.managed = false
						err = s.cycle(ctx, w)
					}
				}
				if change {
					if err == nil {
						t.Fatal("basis change during preparation accepted")
					}
					raw, _ := os.ReadFile(pushLog)
					if len(raw) != 0 {
						t.Fatalf("stale bootstrap invoked push: %s", raw)
					}
				} else {
					must(t, err)
					if s == nil {
						t.Fatal("missing server")
					}
				}
				if len(order) < 4 || strings.Join(order[:4], ",") != "resolve,pin,observe,ensure" {
					t.Fatalf("ordering %v", order)
				}
				state, _, loadErr := db.Load(ctx, id)
				if mode == "reload" {
					if loadErr == nil {
						t.Fatal("closed store unexpectedly loaded")
					}
				} else {
					must(t, loadErr)
				}
				cmd := exec.Command(realGit, "--git-dir="+f.remote, "show-ref", "--verify", "--hash", "refs/heads/openmerge/fence/main")
				out, readErr := cmd.Output()
				if change {
					if readErr == nil || state.Fence != "" {
						t.Fatal("stale bootstrap left a fence")
					}
				} else {
					if readErr != nil || strings.TrimSpace(string(out)) != state.Fence {
						t.Fatalf("remote fence %s state %s err %v", out, state.Fence, readErr)
					}
				}
			})
		}
	}
}

func TestAuthorityBootstrapNeverFallsBackWithoutRequiredPin(t *testing.T) {
	db := database(t)
	s, err := newServer(db, Options{})
	must(t, err)
	f := fake.NewDeterministic()
	key := store.QueueKey{Installation: "7", Repository: "local/required", BaseBranch: "main"}
	w, err := s.register(t.Context(), Target{Key: key, Driver: f, Git: f, BotLogin: fake.Identity, AuthorityRequired: true})
	must(t, err)
	if err := s.initializeFence(t.Context(), w); err == nil {
		t.Fatal("required missing resolver/pin selected zero options")
	}
	if sha, err := f.ReadRef(t.Context(), w.repo(), "openmerge/fence/main"); err == nil || sha != "" {
		t.Fatal("created fence without authority pin")
	}
	// A pin callback that succeeds without recording a pin cannot trigger observation.
	w.PrepareAuthorityPush = func(context.Context) (AuthorityPushPolicy, error) {
		return AuthorityPushPolicy{Mode: authority.OpenMerge}, nil
	}
	w.PinAuthorityPush = func(context.Context, AuthorityPushPolicy) error { return nil }
	observed := false
	w.ObserveAuthorityPush = func(context.Context, AuthorityPushPolicy) error { observed = true; return nil }
	if err := s.initializeFence(t.Context(), w); err == nil || observed {
		t.Fatal("observed protection before durable pin")
	}
	// The existing-ref no-op is still allowed with required, unavailable options.
	initial, err := f.EnsureFence(t.Context(), w.repo(), "openmerge/fence/main", forge.PushOptions{})
	must(t, err)
	must(t, s.initializeFence(t.Context(), w))
	state, _, err := db.Load(t.Context(), w.id)
	must(t, err)
	if state.Fence != initial {
		t.Fatal("existing fence was not recorded")
	}
}

var _ forge.Git = (*bootstrapGit)(nil)

func TestCachedGitHubPinCannotSkipRequiredValidation(t *testing.T) {
	for _, scenario := range []string{"required", "durable-config-changed", "consistent-legacy", "startup", "startup-existing"} {
		t.Run(scenario, func(t *testing.T) {
			db := database(t)
			ctx := t.Context()
			f := fake.NewDeterministic()
			repo := forge.RepoRef{Path: "local/cached-pin"}
			tip, _ := f.Init(repo, "main", "existing")
			// Init creates main, but no openmerge/fence/main ref.
			key := store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}
			id, _, err := db.CanonicalQueue(ctx, key)
			must(t, err)
			digest := strings.Repeat("a", 64)
			pin := queue.PolicyAuthorityApplied{GoverningCommit: tip, EffectiveDigest: digest, Authority: authority.GitHub, DefaultBranch: "main", CoverageDigest: strings.Repeat("b", 64)}
			_, err = db.Append(ctx, id, []queue.Event{queue.ConfigApplied{Commit: tip, Digest: digest}, pin}, "")
			must(t, err)
			target := Target{Key: key, Driver: f, Git: f, BotLogin: fake.Identity, AuthorityRequired: scenario == "required"}
			if scenario == "startup-existing" {
				existing, err := f.EnsureFence(ctx, repo, "openmerge/fence/main", forge.PushOptions{})
				must(t, err)
				_, err = New(ctx, db, []Target{target}, Options{})
				must(t, err)
				state, _, err := db.Load(ctx, id)
				must(t, err)
				if state.Fence != existing {
					t.Fatal("startup lost existing-ref no-op")
				}
				return
			}
			if scenario == "startup" {
				s, err := New(ctx, db, []Target{target}, Options{})
				must(t, err)
				if len(s.queues) != 1 {
					t.Fatal("startup lost queue")
				}
				if _, err := f.ReadRef(ctx, repo, "openmerge/fence/main"); err == nil {
					t.Fatal("startup created unresolved fence")
				}
				w := s.queues[0]
				w.configTip = tip
				w.configDigest = digest
				must(t, s.initializeFence(ctx, w))
				if _, err := f.ReadRef(ctx, repo, "openmerge/fence/main"); err != nil {
					t.Fatal("resolved bootstrap did not resume", err)
				}
				return
			}
			s, err := newServer(db, Options{})
			must(t, err)
			w, err := s.register(ctx, target)
			must(t, err)
			w.configTip = tip
			w.configDigest = digest
			if scenario == "durable-config-changed" {
				_, err = db.Append(ctx, id, []queue.Event{queue.ConfigApplied{Commit: tip, Digest: strings.Repeat("c", 64)}}, "")
				must(t, err)
			}
			err = s.initializeFence(ctx, w)
			if scenario == "consistent-legacy" {
				must(t, err)
				return
			}
			if err == nil {
				t.Fatal("cached pin bypassed validation")
			}
			if _, err := f.ReadRef(ctx, repo, "openmerge/fence/main"); err == nil {
				t.Fatal("created absent fence through fallback")
			}
			// Required or inconsistent options must still allow the existing-ref no-op.
			existing, err := f.EnsureFence(ctx, repo, "openmerge/fence/main", forge.PushOptions{})
			must(t, err)
			must(t, s.initializeFence(ctx, w))
			state, _, err := db.Load(ctx, id)
			must(t, err)
			if state.Fence != existing {
				t.Fatal("lost existing fence no-op")
			}
		})
	}
}
