package serve

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// The server owns discovery/lifecycle/guards; only forge metadata is simulated.
// Every branch mutation and fence assertion below uses an owned bare Git remote.
func TestAuthorityAcceptanceDefaultBranchTransitions(t *testing.T) {
	t.Parallel()
	for _, authorityMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "github", true: "authority"}[authorityMode], func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				db := database(t)
				s, err := newServer(db, Options{})
				must(t, err)
				gf := newCompositionFixture(t, []int{1})
				contents := repositoryConfig
				if authorityMode {
					contents = lifecycleConfig(contents)
				}
				blob := gf.git(contents, "--git-dir="+gf.remote, "hash-object", "-w", "--stdin")
				owners := gf.git("* @writer\n", "--git-dir="+gf.remote, "hash-object", "-w", "--stdin")
				tree := gf.git("100644 blob "+blob+"\t.openmerge.yml\n100644 blob "+owners+"\tCODEOWNERS\n", "--git-dir="+gf.remote, "mktree")
				tip := gf.git("configuration\n", "--git-dir="+gf.remote, "commit-tree", tree, "-p", gf.base)
				gf.git("", "--git-dir="+gf.remote, "update-ref", "refs/heads/main", tip)
				f := fake.NewDeterministic()
				f.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
				f.SetWriteGrant(gf.w.repo(), 21, "write")
				f.SetTeam(forge.TeamIdentity{ID: 200, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
				defaultBranch := "main"
				driver := &acceptanceBranchDriver{Forge: f, defaultBranch: &defaultBranch, read: func(ref, path string) []byte { return []byte(gf.git("", "--git-dir="+gf.remote, "show", ref+":"+path)) }}
				target := Target{Key: gf.w.Key, Driver: driver, Git: gf.g, BotLogin: fake.Identity, AuthorityRequired: authorityMode}
				var draft queue.WriteRestrictionVerified
				var receipt authority.EmissionReceipt
				draftID := int64(0)
				if authorityMode {
					raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
					must(t, err)
					event, err := queue.DecodeEvent(raw)
					must(t, err)
					draft = event.(queue.WriteRestrictionVerified)
					target.resolveAuthorityLifecycle = func(_ context.Context, c config.AuthorityCandidate, _, _ string) (authorityLifecycleResolution, error) {
						return authorityLifecycleResolution{Policy: authority.Policy{Mode: authority.OpenMerge, BreakGlass: authority.ActorBinding{Declared: *c.Authority.BreakGlass, Resolved: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: "200"}}}}, RepositoryID: 123, Activation: authorityActivation{Receipt: receipt, ReceiptMaxAge: time.Hour, DraftID: draftID, Input: forge.WriteRestrictionInput{AppID: "5383059", ExpectedRefs: []string{"refs/heads/" + defaultBranch}}, Read: func(context.Context) (authority.Observation, error) { return draft.Evidence, nil }}}, nil
					}
				}
				w, err := s.register(t.Context(), target)
				must(t, err)
				_, err = db.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: tip}}, "")
				must(t, err)
				if authorityMode {
					receipt = activationReceipt(t, db)
				}
				must(t, s.loadConfiguration(t.Context(), w, "main", tip))
				if authorityMode {
					must(t, s.refreshStageAPolicy(t.Context(), w, tip))
					request, err := s.prepareAuthorityLifecycle(t.Context(), w, lifecycleState(t, s, w), "main", tip)
					must(t, err)
					draft.Binding, draft.VerifiedAt, draft.Basis, draft.Nonce = request.Input.Binding, time.Now().Add(-time.Minute), 0, "branch-transition"
					draft.Evidence, err = forge.InterpretWriteRestriction(request.Input, draft.Evidence)
					must(t, err)
					draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
					must(t, err)
					receipts, err := db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
					must(t, err)
					draftID = receipts[0].EventID
				}
				repository := Repository{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(base string) Target { result := target; result.Key.BaseBranch = base; return result }, Branches: func(context.Context) ([]string, error) {
					return strings.Fields(gf.git("", "--git-dir="+gf.remote, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads")), nil
				}}
				s.repositories = []Repository{repository}
				must(t, s.RefreshBranches(t.Context()))
				initial := lifecycleState(t, s, w)
				fence := gf.git("", "--git-dir="+gf.remote, "rev-parse", "refs/heads/openmerge/fence/main")
				request := store.AuthorityCompletionRequest{Installation: w.Key.Installation, Repository: w.Key.Repository, Digest: initial.ConfigDigest}
				if authorityMode {
					complete, err := s.AuthorityCompletion(t.Context(), request)
					must(t, err)
					must(t, complete.RequireComplete())
				}
				// Delete the adopted current default first, independently of rename.
				// A worker configuration read must invalidate its cached push preparation.
				gf.git("", "--git-dir="+gf.remote, "update-ref", "-d", "refs/heads/main")
				deletedRefs := gf.git("", "--git-dir="+gf.remote, "show-ref")
				if err := s.RefreshBranches(t.Context()); err == nil {
					t.Fatal("deleted default accepted by discovery")
				}
				if err := s.configuration(t.Context(), w); err == nil || w.configTip != "" {
					t.Fatal("deleted default retained usable worker configuration")
				}
				if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
					t.Fatal("deleted adopted default certified complete")
				}
				if authorityMode {
					options := s.authorityPushOptions(t.Context(), w, forge.PushEnsureFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/deleted-default-probe"}})
					if _, err := gf.g.EnsureFence(t.Context(), w.repo(), "openmerge/fence/deleted-default-probe", options); err == nil {
						t.Fatal("deleted default allowed generated fence push")
					}
				}
				if deletedRefs != gf.git("", "--git-dir="+gf.remote, "show-ref") {
					t.Fatal("deleted default changed unrelated real remote refs")
				}
				gf.git("", "--git-dir="+gf.remote, "update-ref", "refs/heads/main", tip)
				must(t, s.RefreshBranches(t.Context()))
				// Rename at the identical commit: default-name binding must still change.
				gf.git("", "--git-dir="+gf.remote, "update-ref", "refs/heads/trunk", tip)
				defaultBranch = "trunk"
				// The new partition has no independently verified proposal.
				draftID = 0
				must(t, s.RefreshBranches(t.Context()))
				if state := lifecycleState(t, s, w); state.LifecycleState() != "retired" {
					t.Fatalf("old default remains active: %s", state.LifecycleState())
				}
				var trunk *worker
				for _, candidate := range s.workers() {
					if candidate.Key.BaseBranch == "trunk" {
						trunk = candidate
					}
				}
				if trunk == nil {
					t.Fatal("renamed default queue not registered")
				}
				next := lifecycleState(t, s, trunk)
				if authorityMode {
					if next.AuthorityPin() == nil || next.AuthorityPin().DefaultBranch != "trunk" || queue.AuthorityDraftAdopted(*next.AuthorityPin()) {
						t.Fatal("rename reused prior default adoption")
					}
					if next.ConfigDigest == initial.ConfigDigest || next.AuthorityPin().GoverningCommit != tip {
						t.Fatal("same-SHA rename did not rebind effective policy")
					}
					request.Targets = map[string]string{"main": initial.ConfigDigest, "trunk": next.ConfigDigest}
					completion, err := s.AuthorityCompletion(t.Context(), request)
					must(t, err)
					if completion.RequireComplete() == nil {
						t.Fatal("partial rename propagation certified complete")
					}
					if len(completion.Queues) != 2 {
						t.Fatalf("rename omitted historical or new partition: %+v", completion.Queues)
					}
					for _, row := range completion.Queues {
						wantReason, wantDigest := "discovery_inputs_stale", initial.ConfigDigest
						if row.Base == "trunk" {
							wantReason, wantDigest = "adoption_missing", next.ConfigDigest
						} else if row.Base != "main" {
							t.Fatalf("unexpected partition: %+v", row)
						}
						if row.Reason != wantReason || row.Digest != wantDigest || row.ExpectedDigest != wantDigest || row.GoverningCommit != tip {
							t.Fatalf("wrong rename propagation evidence: %+v", row)
						}
					}
					before := gf.git("", "--git-dir="+gf.remote, "show-ref")
					options := s.authorityPushOptions(t.Context(), trunk, forge.PushEnsureFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/trunk"}})
					if _, err := gf.g.EnsureFence(t.Context(), trunk.repo(), "openmerge/fence/trunk", options); err == nil {
						t.Fatal("unadopted renamed queue pushed fence")
					}
					if before != gf.git("", "--git-dir="+gf.remote, "show-ref") {
						t.Fatal("rejected renamed fence changed remote refs")
					}
				} else if next.LifecycleState() != "active" || next.AuthorityPin() != nil {
					t.Fatal("legacy rename changed authority/lifecycle")
				}
				// Removing the former default retains its historical queue and real fence.
				gf.git("", "--git-dir="+gf.remote, "update-ref", "-d", "refs/heads/main")
				must(t, s.RefreshBranches(t.Context()))
				if gf.git("", "--git-dir="+gf.remote, "rev-parse", "refs/heads/openmerge/fence/main") != fence {
					t.Fatal("deleted old base lost fence history")
				}
				// A missing CURRENT default refuses fresh discovery/completion. Neither
				// path repairs the real remote by recreating the deleted default branch.
				gf.git("", "--git-dir="+gf.remote, "update-ref", "-d", "refs/heads/trunk")
				before := gf.git("", "--git-dir="+gf.remote, "show-ref")
				if err := s.RefreshBranches(t.Context()); err == nil {
					t.Fatal("missing default accepted")
				}
				if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
					t.Fatal("missing governing ref certified complete")
				}
				if before != gf.git("", "--git-dir="+gf.remote, "show-ref") {
					t.Fatal("missing default discovery changed remote refs")
				}
				if !authorityMode {
					for _, candidate := range s.workers() {
						events, err := db.EventsAfter(t.Context(), candidate.id, 0)
						must(t, err)
						for _, event := range events {
							switch event.Event.(type) {
							case queue.PolicyAuthorityApplied, queue.WriteRestrictionVerified, queue.WriteRestrictionObserved:
								t.Fatal("github branch transition emitted authority event")
							}
						}
					}
				}
			})
		})
	}
}

type acceptanceBranchDriver struct {
	*fake.Forge
	defaultBranch *string
	read          func(string, string) []byte
}

func (d *acceptanceBranchDriver) DefaultBranch(context.Context, forge.RepoRef) (string, error) {
	return *d.defaultBranch, nil
}
func (d *acceptanceBranchDriver) ReadFile(_ context.Context, _ forge.RepoRef, ref, path string) ([]byte, error) {
	if path != ".openmerge.yml" {
		return nil, forge.ErrFileNotFound
	}
	return d.read(ref, path), nil
}
