package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"path/filepath"
	"slices"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestProvenanceFreshGateFakeForgeRealGitAllMethods(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			dir, scratch, _, base, heads := provenanceGitFixture(t)
			expression, err := condition.Parse("check-success = ci")
			must(t, err)
			when, err := p.ParseCondition("#agent-commits > 0")
			must(t, err)
			required, err := condition.Parse("title = allowed")
			must(t, err)
			pass, err := newProvenancePass(config.Queue{MergeWhen: expression, MergeMethod: method}, cp.Policy{Digest: fmt.Sprintf("%064x", 1), RequirePolicyOwners: true}, []p.Rule{{When: when, Conditions: &required}}, nil, p.NewParserPolicy(provenanceGit(t, dir, "", "version")), p.DefaultInventoryLimits())
			must(t, err)
			state, members := provenanceState(t, pass, base, heads[0])
			f := fake.New()
			f.Init(repo, "main", "fixture-fence")
			provenanceGit(t, dir, "", "update-ref", "refs/heads/main", base)
			provenanceGit(t, dir, "", "update-ref", "refs/pull/1/head", heads[0])
			g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(t.TempDir(), "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return dir, gitops.Auth{}, nil }, HeadRef: func(int) string { return "refs/pull/1/head" }})
			must(t, err)
			classified, _ := state.Classification(members[0].Fresh.Basis.ChangeID, 1)
			result, err := g.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "fixture-tested", BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: heads[0], Metadata: &forge.ChangeMetadata{ChangeID: string(members[0].Fresh.Basis.ChangeID), Revision: 1, Labels: cp.ClassificationLabels(classified.Evidence.Classification)}}}, Method: forge.MergeMethod(method), BatchID: "b1"}, forge.PushOptions{})
			must(t, err)
			pairs := []p.CommitMapping{}
			if method == queue.Rebase {
				pairs = append(pairs, p.CommitMapping{Original: heads[0], Build: result})
			}
			s := &Server{store: database(t)}
			key := store.QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
			q, err := s.store.EnsureQueue(t.Context(), key)
			must(t, err)
			w := &worker{Target: Target{Key: key, Driver: f}, id: q, stageA: &stageAPolicy{policy: pass.policy, defaultBranch: "main"}, provenance: pass, prs: map[int]forge.PullRequest{1: {Number: 1, HeadSHA: heads[0], Title: "allowed"}}}
			land := queue.Land{BatchID: "b1", TestedSHA: result, ExpectedBaseSHA: base, PRs: []queue.BatchPR{members[0].Pin}, Method: method}
			localReads := 0
			pass.local = func(_ context.Context, read func(string, string) error) error {
				localReads++
				return read(dir, scratch)
			}
			pass.compositions = func(_ context.Context, _ queue.Land, attempt string) ([]provenanceComposition, error) {
				tested := p.BuildObservation{ID: attempt + "/0", Stage: "tested", Method: string(method), Base: base, Result: result, Replays: pairs}
				if method == queue.FastForward {
					tested.Marker = result
				}
				landed := tested
				landed.Stage = "landed"
				landed.ID += "/landed"
				if method == queue.FastForward {
					landed.Result = heads[0]
					landed.Marker = ""
				}
				return []provenanceComposition{{Tested: tested, Landed: landed, Excluded: []string{}}}, nil
			}
			f.SetCheckRuns(repo, result, []forge.CheckRun{{Name: "ci", Source: "app:1", ID: 1, State: forge.Success}})
			var prepared store.ProvenanceStart
			reads := 0
			gate := provenanceGate(&executor.LandingDecisionGate{Policy: pass.policy, ReadInputsWithChecks: func(context.Context, queue.Land, queue.State, []forge.CheckRun, time.Time, string) ([]queue.ApprovalEligibilityObserved, error) {
				reads++
				return []queue.ApprovalEligibilityObserved{members[0].Fresh}, nil
			}}, &prepared)
			// The real worker reloads immutable approval evidence; this test drives the
			// same private object/mapping preparation against the supplied state.
			checks, err := f.CheckRuns(t.Context(), repo, result)
			must(t, err)
			input, err := pass.prepareStart(t.Context(), s.store, w, state, land, "attempt", []queue.ApprovalEligibilityObserved{members[0].Fresh}, checks)
			must(t, err)
			prepared = input
			start := queue.LandingStarted{BatchID: land.BatchID, AttemptID: "attempt", At: members[0].Fresh.Basis.At}
			decisions, err := gate.Prepare(state, land, start, []queue.ApprovalEligibilityObserved{members[0].Fresh})
			must(t, err)
			legacy, err := state.PrepareLandingDecision(pass.policy, members[0].Fresh, start, land)
			must(t, err)
			if decisions[0].Approval.Identity.BriefDigest == legacy.Approval.Identity.BriefDigest {
				t.Fatal("declarations did not bind receipt identity")
			}
			if localReads != 1 || reads != 0 {
				t.Fatal("unexpected object read count")
			}
			// A fresh policy input or check result must defeat the earlier success.
			w.prs[1] = forge.PullRequest{Number: 1, HeadSHA: heads[0], Title: "changed"}
			changed, err := pass.prepareStart(t.Context(), s.store, w, state, land, "attempt-2", []queue.ApprovalEligibilityObserved{members[0].Fresh}, checks)
			must(t, err)
			if _, _, err := changed.Decisions(state, pass.policy, land, start, []queue.ApprovalEligibilityObserved{members[0].Fresh}); err == nil {
				t.Fatal("stale passing title reused")
			}
			w.prs[1] = forge.PullRequest{Number: 1, HeadSHA: heads[0], Title: "allowed"}
			f.SetCheckRuns(repo, result, []forge.CheckRun{{Name: "ci", Source: "app:1", ID: 2, State: forge.Failure}})
			checks, err = f.CheckRuns(t.Context(), repo, result)
			must(t, err)
			changed, err = pass.prepareStart(t.Context(), s.store, w, state, land, "attempt-3", []queue.ApprovalEligibilityObserved{members[0].Fresh}, checks)
			must(t, err)
			if _, _, err := changed.Decisions(state, pass.policy, land, start, []queue.ApprovalEligibilityObserved{members[0].Fresh}); err == nil {
				t.Fatal("stale successful checks reused")
			}
			if w.provenance == nil || gate.ReadInputs != nil || gate.ReadInputsWithChecks == nil {
				t.Fatal("gate fell back to legacy reads")
			}
			fence, err := g.EnsureFence(t.Context(), repo, "fixture-fence", forge.PushOptions{})
			must(t, err)
			actual := result
			fastHead := ""
			if method == queue.FastForward {
				actual = heads[0]
				fastHead = actual
			}
			// Drive the real activated Stage A read/prepare/transaction callback,
			// using recorded classification facts and the fake's fresh review API.
			w.Git = g
			w.cfg = config.Queue{MergeWhen: expression, MergeMethod: method}
			w.configDigest = pass.policy.Digest
			must(t, s.store.InitializeFence(t.Context(), q, fence))
			revision, _ := state.PRRevision(1)
			facts, _ := state.ApprovalFacts(revision.ChangeID)
			at := time.Now()
			events := []queue.Event{queue.BaseMoved{SHA: base}, queue.ConfigApplied{Commit: base, Digest: w.configDigest}, queue.ChangeRevisionObserved{PR: 1, ChangeID: revision.ChangeID, Revision: revision.Revision, HeadSHA: revision.HeadSHA, Basis: revision.Basis}, queue.ChangeClassified{Evidence: classified.Evidence}, *facts.Accountability, *facts.Intent, queue.PRObserved{PR: 1, HeadSHA: heads[0], Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: land.PRs, At: at}}
			if method == queue.FastForward {
				events = append(events, queue.FastForwardBatchSelected{BatchID: "b1", PRs: land.PRs}, queue.FastForwardRefBuilt{BatchID: "b1", TestedSHA: result, HeadSHA: heads[0], At: at})
			} else {
				events = append(events, queue.TestRefBuilt{BatchID: "b1", TestedSHA: result, Method: method, At: at})
			}
			events = append(events, queue.ChecksCompleted{TestedSHA: result, Success: true})
			_, err = s.store.Append(t.Context(), q, events, "")
			must(t, err)
			cfg := w.plannerConfig(time.Minute)
			_, err = s.store.Plan(t.Context(), q, cfg, at)
			must(t, err)
			claims, err := s.store.ClaimQueue(t.Context(), q, 1, time.Minute, at)
			must(t, err)
			if len(claims) != 1 {
				t.Fatal("missing activated landing", claims)
			}
			claim := claims[0]
			land = claim.Action.(queue.Land)
			proceed, err := s.store.BeginConsequential(t.Context(), claim, cfg, at)
			must(t, err)
			if !proceed {
				t.Fatal("activated landing declined")
			}
			current, _, err := s.store.Load(t.Context(), q)
			must(t, err)
			f.SetNativeReviews(w.repo(), 1, forge.NativeReviews{})
			checks = []forge.CheckRun{{Name: "ci", Source: "app:1", ID: 3, State: forge.Success}}
			live := s.stageAGate(t.Context(), w, claim, cfg, current)
			fresh, err := live.ReadInputsWithChecks(t.Context(), land, current, checks, time.Now(), "attempt")
			must(t, err)
			current, _, err = s.store.Load(t.Context(), q)
			must(t, err)
			start = queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: fence, At: time.Now()}
			decisions, err = live.Prepare(current, land, start, fresh)
			must(t, err)
			messages, err := live.ReadTestedMessages(t.Context(), land)
			must(t, err)
			metadata, err := forge.ParseChangeTrailers(messages[0])
			must(t, err)
			if len(metadata) != 1 || !slices.Equal(metadata[0].Labels, decisions[0].Approval.Labels) {
				t.Fatal("tested classification metadata mismatch")
			}
			must(t, live.Commit(t.Context(), fresh, decisions, start))
			next, err := g.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: result, FastForwardHeadSHA: fastHead, Fence: "fixture-fence", ExpectedFence: fence, NextFenceID: "attempt", PRs: []forge.BuildPR{{Number: 1, HeadSHA: heads[0]}}}, forge.PushOptions{})
			must(t, err)
			must(t, s.store.EmitWithPolicy(t.Context(), claim, cfg, pass.policy)(queue.Landed{BatchID: "b1", NewBaseSHA: actual, NewFence: next}))
			must(t, s.store.RecoverProvenance(t.Context(), q))
			journals, err := s.store.ReportingJournals(t.Context(), []store.QueueKey{key})
			must(t, err)
			companions := 0
			for _, event := range journals[0].Events {
				var envelope struct {
					Type string `json:"type"`
				}
				must(t, json.Unmarshal(event.Payload, &envelope))
				if envelope.Type == "landing_provenance_recorded" {
					companions++
				}
			}
			if companions != 2 {
				t.Fatal("activated runtime missed T/L", companions)
			}
			must(t, s.store.Complete(t.Context(), claim, store.Done))
			if got := provenanceGit(t, dir, "", "rev-parse", "refs/heads/main"); got != actual {
				t.Fatalf("actual L %s != assessed %s", got, actual)
			}
		})
	}
}

func TestProvenanceRequiresCompleteInternalActivation(t *testing.T) {
	// The private pass requires both a stable local object read and trusted
	// composer output; a baseline policy alone cannot activate a writer.
	s, w, _ := shadowFixture(t, 1, "main", "7", false)
	state := shadowState(t, s, w)
	pass := &provenancePass{policy: w.stageA.policy}
	w.provenance = pass
	// Do not enable a writer merely because a baseline approval policy exists.
	_, err := pass.prepareStart(t.Context(), s.store, w, state, queue.Land{}, "attempt", nil, nil)
	if err == nil {
		t.Fatal("incomplete internal activation admitted")
	}
}
