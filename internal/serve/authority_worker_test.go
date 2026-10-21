package serve

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// All fixtures live inside the caller's synctest bubble. Push guards remain
// 9c's responsibility; these tests drive the worker policy and projection seam.
func authorityWorkerFixture(t *testing.T) (*Server, *worker, *fake.Forge, *int) {
	t.Helper()
	s, w, f, _ := fixture(t, repositoryConfig)
	w.Driver = f
	f.SetIdentity(forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount})
	f.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
	f.SetWriteGrant(repo, 21, "write")
	f.SetNativeReviews(repo, 1, forge.NativeReviews{})
	must(t, s.configuration(t.Context(), w))
	w.AuthorityRequired = true
	team := "200"
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	evidence := event.(queue.WriteRestrictionVerified).Evidence
	reads := new(int)
	w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) { *reads++; return evidence, nil })
	_, err = f.PushBase(repo, "main", "authority with governing owners", map[string]string{".openmerge.yml": lifecycleConfig(repositoryConfig), "CODEOWNERS": "* @writer\n"})
	must(t, err)
	return s, w, f, reads
}

func TestAuthorityWorkerFirstCycleAndObservationCadence(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, reads := authorityWorkerFixture(t)
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		if *reads != 1 || state.WriteRestrictionStatus().LastObservedAt.IsZero() || w.stageA == nil || !w.stageA.policy.RequirePolicyOwners || w.stageA.policy.Approvals != nil {
			t.Fatalf("first cycle: reads=%d status=%+v policy=%+v", *reads, state.WriteRestrictionStatus(), w.stageA)
		}
		r, _ := state.PRRevision(1)
		fact, err := state.RecordedBrief(w.stageA.policy, approvalBasis(r))
		must(t, err)
		identity, err := brief.Identity(fact)
		must(t, err)
		fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
		must(t, err)
		if e := state.EvaluateApprovals(w.stageA.policy, identity, fresh); !e.Allowed || len(e.Requirements) != 0 {
			t.Fatalf("invented ordinary floor: %+v", e)
		}
		if gate := s.stageAGate(w, store.ClaimedAction{}, w.plannerConfig(s.options.PollInterval), state); gate.ReadInputsWithChecks == nil || gate.ValidateChecks == nil {
			t.Fatal("authority decision/check gate absent")
		}
		must(t, s.report(t.Context(), w))
		summary, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if !strings.Contains(summary, "This repository merges through OpenMerge") || !strings.Contains(summary, "@openmerge queue") || strings.Contains(summary, "authority unverified") {
			t.Fatal(summary)
		}
		journals, err := s.Journals(t.Context(), []store.QueueKey{w.Key})
		must(t, err)
		q, _, _ := status.Project(journals[0], 0, 0, nil)
		if q.Authority == nil || !strings.Contains(status.AuthoritySummary(q.Authority), "last observed:") {
			t.Fatal("status/CLI projection absent")
		}
		response := httptest.NewRecorder()
		status.Handler(s, []store.QueueKey{w.Key}, "", nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/queues", nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"authority"`) {
			t.Fatalf("API %d %s", response.Code, response.Body.String())
		}
		health, err := s.Health(t.Context())
		must(t, err)
		if len(health.Queues) != 1 || health.Queues[0].Authority == nil {
			t.Fatal("health projection absent")
		}
		// Config-only unchanged cycles and same-binding rebinds spend zero seeing
		// reads even for an unadopted pin. Recurring reconciliations spend one each.
		for range 3 {
			must(t, s.configuration(t.Context(), w))
		}
		if *reads != 1 {
			t.Fatal("unadopted pin polled during configuration", *reads)
		}
		_, err = f.PushBase(repo, "main", "unrelated", map[string]string{"README.md": "new"})
		must(t, err)
		saved := w.resolveAuthorityLifecycle
		team := "200"
		w.resolveAuthorityLifecycle = lifecycleResolver(t, s, &team, func(context.Context) (authority.Observation, error) {
			t.Fatal("draftless rebind read")
			return authority.Observation{}, errors.New("quota")
		})
		must(t, s.configuration(t.Context(), w))
		w.resolveAuthorityLifecycle = saved
		must(t, s.reconcile(t.Context(), w, false))
		if *reads != 2 {
			t.Fatal("observation cadence", *reads)
		}
		// Reconstruct from recorded local objects, without any forge access.
		rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		req := status.BriefPolicyRequest{Authority: lifecycleState(t, s, w).AuthorityPin()}
		for _, row := range rows {
			switch e := row.Event.(type) {
			case queue.ConfigApplied:
				req.Config = e
			case queue.ChangeClassified:
				req.Evidence = e.Evidence
			}
		}
		data, err := f.ReadFile(t.Context(), repo, req.Config.Commit, ".openmerge.yml")
		must(t, err)
		g, err := f.GoverningFiles(t.Context(), repo, forge.GoverningFilesRequest{CommitSHA: req.Config.Commit, AllAttributes: true, PathsComplete: true})
		must(t, err)
		oldG, err := f.GoverningFiles(t.Context(), repo, forge.GoverningFilesRequest{CommitSHA: state.ConfigCommit, AllAttributes: true, PathsComplete: true})
		must(t, err)
		w.Git = &localPolicyGit{data: data, governing: g, governingByCommit: map[string]forge.GoverningFiles{g.CommitSHA: g, oldG.CommitSHA: oldG}}
		w.Driver = nil
		policy, err := s.BriefPolicy(t.Context(), w.Key, req)
		must(t, err)
		if !policy.RequirePolicyOwners || policy.Digest != w.stageA.policy.Digest {
			t.Fatal("historical authority brief policy", policy)
		}
		response = httptest.NewRecorder()
		status.Handler(s, []store.QueueKey{w.Key}, "", nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/queues/owner/repo/main/changes/"+string(r.ChangeID)+"/brief?audience=agent", nil))
		if response.Code != 200 || strings.Contains(response.Body.String(), "policy_unavailable") || strings.Contains(response.Body.String(), "policy_digest_mismatch") {
			t.Fatalf("rebound HTTP brief: %d %s", response.Code, response.Body.String())
		}

	})
}

func TestAuthorityWorkerGoverningOwnersAndExactRevision(t *testing.T) {
	for _, path := range []string{"CODEOWNERS", ".openmerge.yml"} {
		t.Run(path, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := authorityWorkerFixture(t)
				must(t, s.reconcile(t.Context(), w, true))
				base := lifecycleState(t, s, w).BaseSHA
				proposed := "* @contributor\n"
				if path == ".openmerge.yml" {
					proposed = lifecycleConfig(repositoryConfig) + "# proposed replacement\n"
				}
				pr, err := f.OpenPRWithFiles(repo, 2, base, true, map[string]string{path: proposed})
				must(t, err)
				f.SetNativeReviews(repo, 2, forge.NativeReviews{})
				must(t, s.reconcile(t.Context(), w, true))
				evaluate := func() cp.Evaluation {
					t.Helper()
					state := lifecycleState(t, s, w)
					r, _ := state.PRRevision(2)
					fact, err := state.RecordedBrief(w.stageA.policy, approvalBasis(r))
					must(t, err)
					identity, err := brief.Identity(fact)
					must(t, err)
					fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
					must(t, err)
					return state.EvaluateApprovals(w.stageA.policy, identity, fresh)
				}
				if e := evaluate(); e.Allowed || len(e.Requirements) == 0 {
					t.Fatal("policy edit waived governing owners", e)
				}
				native := forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}}
				f.SetNativeReviews(repo, 2, native)
				must(t, s.reconcile(t.Context(), w, false))
				if e := evaluate(); !e.Allowed {
					t.Fatal("governing owner approval rejected", e)
				}
				_, err = f.PushPR(repo, 2, "new revision")
				must(t, err)
				must(t, s.reconcile(t.Context(), w, false))
				if e := evaluate(); e.Allowed {
					t.Fatal("old head approval reused", e)
				}
			})
		})
	}
}

func TestAuthorityWorkerRollbackRetainsDelayedWarnings(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, reads := authorityWorkerFixture(t)
		must(t, s.reconcile(t.Context(), w, true))
		activeMove, err := f.PushBase(repo, "main", "outside under authority", nil)
		must(t, err)
		must(t, s.reconcile(t.Context(), w, false))
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.RulesSourceBlocked{Source: queue.Permissions, Problems: []string{"unrelated permission block"}}, queue.BlockSourceBlocked{Source: queue.Operator, Detail: "operator hold"}}, "")
		must(t, err)
		rollbackMove, err := f.PushBase(repo, "main", "rollback", map[string]string{".openmerge.yml": repositoryConfig})
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		before := *reads
		_, err = f.PushBase(repo, "main", "outside after rollback", nil)
		must(t, err)
		must(t, s.reconcile(t.Context(), w, false))
		if *reads != before || w.stageA != nil || lifecycleState(t, s, w).AuthorityPin().Authority != authority.GitHub {
			t.Fatal("rollback retained observation/owner hook")
		}
		// All audit enrichment arrives after rollback, including the move just
		// before activation, which must remain ineligible for an authority warning.
		for range 3 {
			runOutsidePushAudit(t, s, w)
		}
		warnings := []store.OutsidePushWarning{}
		must(t, s.store.VisitOutsidePushWarnings(t.Context(), w.id, func(p store.OutsidePushWarning) error { warnings = append(warnings, p); return nil }))
		if len(warnings) != 2 {
			t.Fatalf("origin eligibility changed across rollback: %+v", warnings)
		}
		for _, p := range warnings {
			if p.NewSHA != activeMove && p.NewSHA != rollbackMove {
				t.Fatal("github-origin warning", p)
			}
		}
		state := lifecycleState(t, s, w)
		snapshot, err := queue.EncodeSnapshot(state)
		must(t, err)
		restored, err := queue.DecodeSnapshot(snapshot)
		must(t, err)
		if len(restored.RulesSourceProblems(queue.Permissions)) == 0 || !restored.SourceBlocked(queue.Operator) {
			t.Fatal("rollback removed unrelated protections")
		}
		must(t, s.refreshOutsideWarnings(t.Context(), w))
		for _, p := range warnings {
			_, err = s.store.AcknowledgeOutsidePush(t.Context(), w.id, p.EventID, p.NewSHA, "operator")
			must(t, err)
		}
		summary, err := s.store.ReadOutsidePushWarnings(t.Context(), w.id)
		must(t, err)
		if summary.Outstanding != 0 {
			t.Fatal(summary)
		}
		after := lifecycleState(t, s, w)
		if len(after.RulesSourceProblems(queue.Permissions)) == 0 || !after.SourceBlocked(queue.Operator) || after.BaseSHA != state.BaseSHA || len(after.RecentOutsidePushes()) != len(state.RecentOutsidePushes()) {
			t.Fatal("acknowledgement changed history/protection")
		}
		rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		acknowledgements := 0
		for _, row := range rows {
			if _, ok := row.Event.(queue.OutsidePushAcknowledged); ok {
				acknowledgements++
			}
		}
		if acknowledgements != 2 {
			t.Fatal("ack history missing")
		}
	})
}

func TestAuthorityWorkerAtomicDecisionAndCheckEvidence(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(100, "@openmerge queue")}})
		must(t, s.reconcile(t.Context(), w, true))
		cfg := w.plannerConfig(s.options.PollInterval)
		claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
		proceed, err := s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
		must(t, err)
		if !proceed {
			t.Fatal("build did not begin")
		}
		state := lifecycleState(t, s, w)
		// Exercise the executor's existing metadata/check/decision path directly.
		// Fake push authorization is deliberately excluded from this 2c proof.
		x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, BuildMetadata: executor.LoadBuildMetadata, BuildMetadataState: state, BuildMetadataKey: w.Key, BuildPolicyDigest: w.stageA.policy.Digest}
		x.BuildMetadata = func(_ queue.State, key executor.BuildChangeKey, batch queue.Batch, digest string) ([]*forge.ChangeMetadata, error) {
			current := lifecycleState(t, s, w)
			return executor.LoadBuildMetadataWithAncestry(current, key, *current.Batch(batch.ID), digest, func(a, b string) (bool, error) { return f.IsAncestor(t.Context(), repo, a, b) })
		}
		must(t, x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy)))
		must(t, s.store.Complete(t.Context(), claim, store.Done))
		batch := lifecycleState(t, s, w).BatchForPR(1)
		if batch == nil || batch.TestedSHA == "" {
			t.Fatal("no built batch")
		}
		f.SetCheckRuns(repo, batch.TestedSHA, []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 2, State: forge.Success, HeadBranch: batch.TestRef}})
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true}}, "")
		must(t, err)
		claim = claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
		proceed, err = s.store.BeginConsequential(t.Context(), claim, cfg, time.Now())
		must(t, err)
		if !proceed {
			t.Fatal("landing did not begin")
		}
		state = lifecycleState(t, s, w)
		// Direct emission must not bypass mandatory-owner decision storage.
		land := claim.Action.(queue.Land)
		if err := s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy)(queue.LandingStarted{BatchID: land.BatchID, AttemptID: "bypass", FenceToken: state.Fence, At: time.Now()}); err == nil {
			t.Fatal("direct start bypassed decision transaction")
		}
		x.LandingDecisions = s.stageAGate(w, claim, cfg, state)
		must(t, x.Execute(t.Context(), claim.Action, s.store.EmitWithPolicy(t.Context(), claim, cfg, w.stageA.policy)))
		rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		decisions, checks := 0, 0
		for i, row := range rows {
			switch e := row.Event.(type) {
			case queue.LandingDecisionRecorded:
				decisions++
			case queue.LandingChecksRecorded:
				checks++
				if *e.Checks[0].RunID != 701 || *e.Checks[0].Attempt != 2 {
					t.Fatal("wrong exact check evidence", e)
				}
				if start, ok := rows[i+1].Event.(queue.LandingStarted); !ok || start.AttemptID != e.AttemptID {
					t.Fatal("check/start not atomic neighbors")
				}
			}
		}
		if decisions != 1 || checks != 1 {
			t.Fatal("missing decision/check evidence", decisions, checks)
		}
	})
}

func TestAuthorityWorkerNoneCannotWaivePolicyOwners(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		config := lifecycleConfig(repositoryConfig) + "    approvals:\n      default: any_writer\n      rules:\n        - when: {paths: ['**']}\n          require: none\n"
		base, err := f.PushBase(repo, "main", "user none rule", map[string]string{".openmerge.yml": config})
		must(t, err)
		pr, err := f.OpenPRWithFiles(repo, 2, base, true, map[string]string{"CODEOWNERS": "* @contributor\n"})
		must(t, err)
		f.SetNativeReviews(repo, 2, forge.NativeReviews{})
		must(t, s.reconcile(t.Context(), w, true))
		evaluate := func() cp.Evaluation {
			t.Helper()
			state := lifecycleState(t, s, w)
			r, _ := state.PRRevision(2)
			fact, err := state.RecordedBrief(w.stageA.policy, approvalBasis(r))
			must(t, err)
			identity, err := brief.Identity(fact)
			must(t, err)
			fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
			must(t, err)
			return state.EvaluateApprovals(w.stageA.policy, identity, fresh)
		}
		if e := evaluate(); e.Allowed {
			t.Fatal("user none waived mandatory owner", e)
		}
		f.SetNativeReviews(repo, 2, forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 201, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}})
		must(t, s.reconcile(t.Context(), w, false))
		if e := evaluate(); !e.Allowed {
			t.Fatal("owner approval under none rejected", e)
		}
		f.SetWriteGrant(repo, 21, "read")
		if e := evaluate(); e.Allowed {
			t.Fatal("revoked owner grant remained eligible", e)
		}
	})
}
