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
		for _, change := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/change-%t", path, change), func(t *testing.T) {
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
				pin.OpenMerge.Binding.QueueKey = "fixture/main"
				pin.OpenMerge.Binding.EffectiveDigest = digest
				pin.OpenMerge.Binding.DefaultBranch = "main"
				must(t, pin.Validate())
				evidence := authority.Observation{Inventory: authority.Visible, Protections: []authority.Protection{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Absent, ProtectionIDs: []string{}}}, Findings: []authority.Finding{}}
				policy := AuthorityPushPolicy{Mode: authority.OpenMerge, GoverningCommit: tip, EffectiveDigest: digest, AppID: "42"}
				order := []string{}
				observed := func(basis uint64) error {
					_, err := db.Append(ctx, id, []queue.Event{queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: basis, ObservedAt: time.Now().UTC(), Evidence: evidence}}, "")
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
						if change && calls == 2 {
							must(t, observed(2))
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
				must(t, loadErr)
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
