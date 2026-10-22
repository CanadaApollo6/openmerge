package serve

import (
	"context"
	"os"
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

type lifecycleFenceGit struct {
	*gitops.Repo
	before func(forge.PushOptions)
}

func (g *lifecycleFenceGit) EnsureFence(ctx context.Context, repo forge.RepoRef, ref string, options forge.PushOptions) (string, error) {
	g.before(options)
	return g.Repo.EnsureFence(ctx, repo, ref, options)
}

func TestAuthorityLifecycleFirstFenceEntrypoints(t *testing.T) {
	for _, path := range []string{"registration", "refreshWorker", "cycle", "unadopted"} {
		t.Run(path, func(t *testing.T) {
			db := database(t)
			s, err := newServer(db, Options{})
			must(t, err)
			real := newCompositionFixture(t, []int{1})
			repo := real.w.repo()
			f := fake.NewDeterministic()
			f.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
			f.SetWriteGrant(repo, 21, "write")
			f.SetTeam(forge.TeamIdentity{ID: 200, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
			contents := lifecycleConfig(repositoryConfig)
			blob := real.git(contents, "--git-dir="+real.remote, "hash-object", "-w", "--stdin")
			owners := real.git("* @writer\n", "--git-dir="+real.remote, "hash-object", "-w", "--stdin")
			tree := real.git("100644 blob "+blob+"\t.openmerge.yml\n100644 blob "+owners+"\tCODEOWNERS\n", "--git-dir="+real.remote, "mktree")
			tip := real.git("authority configuration\n", "--git-dir="+real.remote, "commit-tree", tree, "-p", real.base)
			real.git("", "--git-dir="+real.remote, "update-ref", "refs/heads/main", tip)
			driver := lifecycleFenceDriver{Forge: f, read: func(ref, path string) []byte {
				return []byte(real.git("", "--git-dir="+real.remote, "show", ref+":"+path))
			}}
			target := Target{Key: real.w.Key, Driver: driver, Git: real.g, BotLogin: fake.Identity, AuthorityRequired: true}
			w, err := s.register(t.Context(), target)
			must(t, err)
			_, err = db.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: real.base}}, "")
			must(t, err)
			raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
			must(t, err)
			event, err := queue.DecodeEvent(raw)
			must(t, err)
			draft := event.(queue.WriteRestrictionVerified)
			receipt := activationReceipt(t, db)
			draftID := int64(0)
			target.resolveAuthorityLifecycle = func(_ context.Context, c config.AuthorityCandidate, _, _ string) (authorityLifecycleResolution, error) {
				policy := authority.Policy{Mode: c.Authority.Mode.Normalized(), BreakGlass: authority.ActorBinding{Declared: *c.Authority.BreakGlass, Resolved: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: "200"}}}}
				return authorityLifecycleResolution{Policy: policy, RepositoryID: 123, Activation: authorityActivation{Receipt: receipt, ReceiptMaxAge: time.Hour, DraftID: draftID, Input: forge.WriteRestrictionInput{AppID: "5383059", ExpectedRefs: []string{"refs/heads/main"}}, Read: func(context.Context) (authority.Observation, error) { return draft.Evidence, nil }}}, nil
			}
			w.resolveAuthorityLifecycle = target.resolveAuthorityLifecycle
			must(t, s.loadConfiguration(t.Context(), w, "main", tip))
			must(t, s.refreshStageAPolicy(t.Context(), w, tip))
			request, err := s.prepareAuthorityLifecycle(t.Context(), w, lifecycleState(t, s, w), "main", tip)
			must(t, err)
			draft.Binding = request.Input.Binding
			draft.VerifiedAt = time.Now().Add(-time.Minute)
			draft.Basis = 0
			draft.Nonce = "first-fence-" + path
			draft.Evidence, err = forge.InterpretWriteRestriction(request.Input, draft.Evidence)
			must(t, err)
			draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
			must(t, err)
			receipts, err := db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
			must(t, err)
			draftID = receipts[0].EventID
			if path == "unadopted" {
				draftID = 0
			}
			called := false
			proxy := &lifecycleFenceGit{Repo: real.g, before: func(options forge.PushOptions) {
				called = true
				if path == "unadopted" {
					if !options.AuthorityRequired || options.Guard == nil || options.Guard(t.Context(), options.Context) == nil {
						t.Fatal("unadopted pin permitted fence creation")
					}
					return
				}
				state := lifecycleState(t, s, w)
				if state.AuthorityPin() == nil || !queue.AuthorityDraftAdopted(*state.AuthorityPin()) || state.WriteRestrictionStatus().LastObservedAt.IsZero() {
					t.Fatal("first fence preceded adopted pin or observation")
				}
				if !options.AuthorityRequired || options.Guard == nil || options.Context.Binding().GoverningCommit != tip {
					t.Fatal("direct EnsureFence lost lifecycle push options")
				}
				must(t, options.Guard(t.Context(), options.Context))
			}}
			w.Git, target.Git = proxy, proxy
			switch path {
			case "registration", "unadopted":
				var created *Server
				created, err = New(t.Context(), db, []Target{target}, Options{})
				if err == nil {
					s, w = created, created.queues[0]
				}
			case "refreshWorker":
				err = s.refreshWorker(t.Context(), w, w.configCandidate.Config, "main", tip, []string{"main"}, map[string]bool{})
			case "cycle":
				w.managed = false
				err = s.cycle(t.Context(), w)
			}
			must(t, err)
			state := lifecycleState(t, s, w)
			if path == "unadopted" {
				if !called || state.Fence != "" || state.AuthorityPin() == nil {
					t.Fatal("unadopted startup failed to defer")
				}
				if sha, readErr := real.g.ReadRef(t.Context(), repo, "openmerge/fence/main"); readErr == nil || sha != "" {
					t.Fatal("unadopted startup created remote fence")
				}
				return
			}
			remoteFence := real.git("", "--git-dir="+real.remote, "rev-parse", "refs/heads/openmerge/fence/main")
			if !called || state.Fence == "" || state.Fence != remoteFence {
				t.Fatal("entrypoint did not initialize/read back first real fence")
			}
			if target.PrepareAuthorityPush != nil || target.PinAuthorityPush != nil || target.ObserveAuthorityPush != nil {
				t.Fatal("test bypassed internal lifecycle adapter")
			}
			if path == "refreshWorker" {
				// Every generated-push family receives a real store-reloading
				// guard from the integrated lifecycle, including speculative builds.
				intents := map[forge.PushOperation][]forge.PushRefIntent{
					forge.PushBuild:        {{Ref: "refs/heads/openmerge/queue/main/probe", BuildBaseSHA: state.BaseSHA}},
					forge.PushLand:         {{Ref: "refs/heads/main", OldSHA: state.BaseSHA, NewSHA: state.BaseSHA}, {Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}},
					forge.PushEnsureFence:  {{Ref: "refs/heads/openmerge/fence/new"}},
					forge.PushAdvanceFence: {{Ref: "refs/heads/openmerge/fence/main", OldSHA: state.Fence}},
					forge.PushDeleteBranch: {{Ref: "refs/heads/feature", OldSHA: state.BaseSHA, Delete: true}},
				}
				frozen := make(map[forge.PushOperation]forge.PushOptions)
				for operation, refs := range intents {
					options := s.authorityPushOptions(t.Context(), w, operation, refs)
					if options.Context.Binding().GoverningCommit == "" {
						t.Fatalf("%s has no context: %v", operation, options.Guard(t.Context(), options.Context))
					}
					must(t, options.Guard(t.Context(), options.Context))
					frozen[operation] = options
				}
				_, err = db.Append(t.Context(), w.id, []queue.Event{queue.WriteRestrictionObserved{Binding: draft.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), ObservedAt: time.Now(), Evidence: draft.Evidence}}, "")
				must(t, err)
				for operation, options := range frozen {
					if options.Guard(t.Context(), options.Context) == nil {
						t.Fatalf("%s reused cached state after observation drift", operation)
					}
				}
				db.Close()
				for operation, options := range frozen {
					if err := options.Guard(t.Context(), options.Context); err == nil || err.Error() != "authority push durable state unavailable" {
						t.Fatalf("%s did not propagate a sanitized reload failure: %v", operation, err)
					}
				}
			}
		})
	}
}

var _ forge.Git = (*lifecycleFenceGit)(nil)

// The forge side supplies identities; governing files and generated objects are
// acquired by the real Git implementation against the local bare remote.
type lifecycleFenceDriver struct {
	*fake.Forge
	read func(string, string) []byte
}

func (d lifecycleFenceDriver) DefaultBranch(context.Context, forge.RepoRef) (string, error) {
	return "main", nil
}
func (d lifecycleFenceDriver) ReadFile(_ context.Context, _ forge.RepoRef, ref, path string) ([]byte, error) {
	if path != ".openmerge.yml" {
		return nil, forge.ErrFileNotFound
	}
	return d.read(ref, path), nil
}

func TestAuthorityStartupUnavailablePreparationPreservesRealFenceNoop(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "existing"}[existing], func(t *testing.T) {
			db := database(t)
			real := newCompositionFixture(t, []int{1})
			blob := real.git(repositoryConfig, "--git-dir="+real.remote, "hash-object", "-w", "--stdin")
			tree := real.git("100644 blob "+blob+"\t.openmerge.yml\n", "--git-dir="+real.remote, "mktree")
			tip := real.git("governing configuration\n", "--git-dir="+real.remote, "commit-tree", tree, "-p", real.base)
			real.git("", "--git-dir="+real.remote, "update-ref", "refs/heads/main", tip)
			token := ""
			var err error
			if existing {
				token, err = real.g.EnsureFence(t.Context(), real.w.repo(), "openmerge/fence/main", forge.PushOptions{})
				must(t, err)
			}
			before := real.git("", "--git-dir="+real.remote, "show-ref")
			called := false
			target := Target{Key: real.w.Key, Git: real.g, Driver: bootstrapDriver{Driver: fake.NewDeterministic(), read: func(input string, args ...string) string {
				return real.git(input, append([]string{"--git-dir=" + real.remote}, args...)...)
			}}, BotLogin: fake.Identity, AuthorityRequired: true,
				resolveAuthorityLifecycle: func(context.Context, config.AuthorityCandidate, string, string) (authorityLifecycleResolution, error) {
					called = true
					return authorityLifecycleResolution{}, forge.ErrRulesUnavailable
				},
			}
			s, err := New(t.Context(), db, []Target{target}, Options{})
			must(t, err)
			state := lifecycleState(t, s, s.queues[0])
			if !called || state.Fence != token || state.AuthorityPin() != nil {
				t.Fatal("unavailable preparation changed fence/pin", state.Fence, token)
			}
			after := real.git("", "--git-dir="+real.remote, "show-ref")
			if before != after {
				t.Fatal("unavailable startup changed real remote refs")
			}
		})
	}
}
