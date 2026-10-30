package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Shared approval fixture enters through the production configuration loader.
func shadowFixture(t *testing.T, depth int, base, installation string, receipt bool) (*Server, *worker, *fake.Forge) {
	return shadowFixtureForMethod(t, depth, base, installation, receipt, "")
}

func shadowFixtureForMethod(t *testing.T, depth int, base, installation string, receipt bool, method queue.MergeMethod) (*Server, *worker, *fake.Forge) {
	t.Helper()
	f := fake.New()
	f.Init(repo, base, "openmerge/fence/"+base)
	cfg := fmt.Sprintf("version: 1\nqueues:\n  - name: default\n    base: %s\n    speculation: {min_depth: %d, max_depth: %d}\n    merge_when: [check-success = ci]\n", base, depth, depth)
	if method != "" {
		cfg += fmt.Sprintf("    merge_method: %s\n", method)
	}
	if receipt {
		cfg += "    approvals: {default: {any_writer: true, count: 1, receipt: true}}\n"
	} else {
		cfg += "    approvals: {default: any_writer}\n"
	}
	tip, err := f.PushBase(repo, base, "config", map[string]string{".openmerge.yml": cfg})
	must(t, err)
	_, err = f.OpenPR(repo, 1, tip, true)
	must(t, err)
	writer := forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}
	f.SetIdentity(writer)
	f.SetWriteGrant(repo, 21, "write")
	f.SetCanWrite(repo, "writer", true)
	f.SetIdentity(forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount})
	f.SetNativeReviews(repo, 1, forge.NativeReviews{})
	s, err := New(t.Context(), database(t), []Target{{Key: store.QueueKey{Installation: installation, Repository: repo.Path, BaseBranch: base}, Driver: f, Git: f, BotLogin: fake.Identity}}, Options{PollInterval: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	w := s.queues[0]
	must(t, s.reconcile(t.Context(), w, true))
	return s, w, f
}

func shadowNative(_ *fake.Forge, _ *worker, comments ...forge.NativeComment) forge.NativeReviews {
	return forge.NativeReviews{Comments: comments}
}
func shadowComment(id int64, body string) forge.NativeComment {
	return forge.NativeComment{ID: id, Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, Body: body, Created: forge.NativeTime{Time: time.Now().UTC().Add(time.Hour), Source: forge.CommentCreationTime, Precision: time.Second}}
}
func shadowState(t *testing.T, s *Server, w *worker) queue.State {
	t.Helper()
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	return state
}
func shadowDigest(t *testing.T, s *Server, w *worker) string {
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	fact, err := state.RecordedBrief(w.stageA.policy, approvalBasis(r))
	must(t, err)
	identity, err := brief.Identity(fact)
	must(t, err)
	return identity.BriefDigest
}

type corruptTestedMessageGit struct {
	*fake.Forge
	sha, message string
}

func (g *corruptTestedMessageGit) ReadCommitMessage(ctx context.Context, repo forge.RepoRef, sha string) (string, error) {
	if sha == g.sha {
		return g.message, nil
	}
	return g.Forge.ReadCommitMessage(ctx, repo, sha)
}

func TestStageABareReceiptCommands(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	digest := shadowDigest(t, s, w)
	comments := []forge.NativeComment{shadowComment(100, "@openmerge approve "+digest), shadowComment(101, "@openmerge approve "+strings.Repeat("0", 64)), shadowComment(102, "@openmerge approve BAD"), shadowComment(103, "please @openmerge approve "+digest)}
	self := shadowComment(104, "@openmerge unapprove")
	self.Author.Login = fake.Identity
	comments = append(comments, self)
	f.SetNativeReviews(repo, 1, shadowNative(f, w, comments...))
	must(t, s.commands(t.Context(), w, 1))
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	records := state.ReceiptRecords(r.ChangeID)
	if len(records) != 3 || !records["100"].Accepted || records["101"].ReasonCode != "receipt_digest_mismatch" || records["102"].ReasonCode != "receipt_digest_malformed" {
		t.Fatalf("receipt outcomes %+v", records)
	}
	// Inclusive polling at an equal creation time catches a later ID. Edits of a
	// consumed comment are ignored even when its body becomes another command.
	comments[0].Body = "@openmerge unapprove"
	next := shadowComment(105, "@openmerge unapprove")
	next.Created = comments[len(comments)-1].Created
	comments = append(comments, next)
	f.SetNativeReviews(repo, 1, shadowNative(f, w, comments...))
	must(t, s.commands(t.Context(), w, 1))
	before := shadowState(t, s, w)
	must(t, s.commands(t.Context(), w, 1))
	after := shadowState(t, s, w)
	a, _ := queue.EncodeSnapshot(before)
	b, _ := queue.EncodeSnapshot(after)
	if string(a) != string(b) {
		t.Fatal("duplicate commands changed snapshot")
	}
	facts, _ := after.ApprovalFacts(r.ChangeID)
	if len(facts.Sources) != 2 || facts.Sources["command/105"].Revision != r.Revision { // use channel's actual wire spelling below
		found := false
		for _, source := range facts.Sources {
			if source.Event.Stance.Source.ID == "105" && source.Revision == r.Revision {
				found = true
			}
		}
		if !found || len(facts.Sources) != 2 {
			t.Fatalf("withdrawal binding %+v", facts)
		}
	}
}

func TestStageACommandWholeCommentGrammar(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"prefix @openmerge approve abc", "@openmerge approve abc\nplease", "@openmerge approve abc extra", "@openmerge unapprove now", "\u00a0@openmerge approve abc"} {
		if verb, _ := approvalCommand(body); verb != "" {
			t.Fatalf("accepted prose %q", body)
		}
	}
	for _, body := range []string{"@openmerge approve BAD", "@openmerge approve", "\r\n@openmerge\tapprove\tabc\r\n", "@openmerge unapprove"} {
		if verb, _ := approvalCommand(body); verb == "" {
			t.Fatalf("ignored command %q", body)
		}
	}
	if command("@openmerge approve "+strings.Repeat("a", 64)) != "" {
		t.Fatal("legacy parser changed")
	}
}

func TestStageAShadowGateDepthsAndIdentityIsolation(t *testing.T) {
	t.Parallel()
	for _, depth := range []int{1, 4} {
		for _, base := range []string{"main", "release/v1"} {
			for _, installation := range []string{"7", "8"} {
				t.Run(fmt.Sprintf("depth-%d/%s/install-%s", depth, base, installation), func(t *testing.T) {
					t.Parallel()
					s, w, f := shadowFixture(t, depth, base, installation, false)
					native := shadowNative(f, w, shadowComment(100, "@openmerge queue"))
					f.SetNativeReviews(repo, 1, native)
					batch := shadowBuildBatch(t, s, w, 1)
					state := shadowState(t, s, w)
					if batch == nil || batch.TestedSHA == "" {
						t.Fatalf("no tested composition %+v", state)
					}
					tested, ref := batch.TestedSHA, batch.TestRef
					shadowChecks(t, s, w, f, tested, []forge.CheckRun{{ID: 1, Name: "ci", Source: "github-actions", State: forge.Success, HeadBranch: ref}})
					// First landing has no approval. It must record a durable wait and no start.
					shadowRefreshChecks(t, s, w)
					shadowRejectLanding(t, s, w)
					state = shadowState(t, s, w)
					if len(state.ApprovalWaits()) != 1 || len(state.LandingDecisions()) != 0 {
						t.Fatalf("missing wait %+v", state.ApprovalWaits())
					}
					idlePolls := 3
					if testing.Short() {
						idlePolls = 2
					}
					for i := 0; i < idlePolls; i++ {
						if !testing.Short() {
							must(t, s.cycle(t.Context(), w))
							continue
						}
						// An ordinary acquisition and the real planner must keep an
						// unchanged approval wait idle; reporting is covered separately.
						must(t, s.reconcile(t.Context(), w, false))
						for _, action := range queue.Plan(w.plannerConfig(s.options.PollInterval), shadowState(t, s, w), time.Now()) {
							switch action.(type) {
							case queue.BuildTestRef, queue.BuildChainedTestRef:
								t.Fatal("unchanged wait planned a retest")
							}
						}
					}
					state = shadowState(t, s, w)
					if b := state.BatchForPR(1); b != nil && b.InvalidationReason == "" {
						t.Fatal("unchanged wait retested")
					}
					// No webhook: ordinary reconciliation discovers a fresh exact-head review.
					pr, err := f.PullRequest(t.Context(), repo, 1)
					must(t, err)
					native.Reviews = []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}
					f.SetNativeReviews(repo, 1, native)
					batch = shadowBuildBatch(t, s, w, 1)
					shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{ID: 2, Name: "ci", Source: "github-actions", State: forge.Success, HeadBranch: batch.TestRef}})
					shadowRefreshChecks(t, s, w)
					shadowLandBatch(t, s, w)
					events, err := s.store.EventsAfter(t.Context(), w.id, 0)
					must(t, err)
					decisions, starts, landed := 0, 0, 0
					for _, record := range events {
						switch record.Event.(type) {
						case queue.LandingDecisionRecorded:
							decisions++
						case queue.LandingStarted:
							starts++
						case queue.Landed:
							landed++
						}
					}
					if decisions != 1 || starts != 1 || landed != 1 {
						t.Fatalf("decision/start/landed %d/%d/%d", decisions, starts, landed)
					}
				})
			}
		}
	}
}

// Swapping a login between review acquisition and grant read cannot authorize
// its old author ID. Missing IDs and revoked grants follow the same boundary.
func TestStageAIdentitySwapRevocationAndNoiseAdmission(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{"read", "reassigned", "missing"} {
		t.Run(permission, func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			native := forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 1, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewChangesRequested, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}}}, Comments: []forge.NativeComment{shadowComment(100, "@openmerge approve BAD")}}
			if permission == "read" {
				f.SetWriteGrant(repo, 21, "read")
			} else {
				replacement := int64(22)
				if permission == "missing" {
					replacement = 0
				}
				f.SetIdentity(forge.UserIdentity{ID: replacement, Login: "writer", Kind: forge.UserAccount})
				f.SetWriteGrant(repo, replacement, "write")
			}
			f.SetNativeReviews(repo, 1, native)
			_, err = s.observe(t.Context(), w, 1)
			must(t, err)
			must(t, s.commands(t.Context(), w, 1))
			state := shadowState(t, s, w)
			r, _ := state.PRRevision(1)
			facts, _ := state.ApprovalFacts(r.ChangeID)
			if len(facts.Sources) != 0 || len(state.ReceiptRecords(r.ChangeID)) != 0 {
				t.Fatal("unbound/noise actor consumed approval capacity")
			}
			raw, err := s.store.Observation(t.Context(), w.id, "comment/100")
			must(t, err)
			if !strings.Contains(string(raw), "identity-bound") {
				t.Fatal("missing unauthorized outcome", string(raw))
			}
		})
	}
}

func TestStageAReceiptAcceptanceAtomicBoundary(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", true)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	b := approvalBasis(r)
	actor := cp.Identity{ID: "github.com:user:21", Login: "writer", Kind: cp.HumanAccount}
	receipt, err := state.PrepareBareReceipt(w.stageA.policy, b, actor, "100", time.Now().Add(time.Hour), shadowDigest(t, s, w))
	must(t, err)
	// Race a durable intent edit before the queue lock is acquired.
	edited := queue.Intent(b, "new intent", "")
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{edited}, "")
	must(t, err)
	data, _ := json.Marshal(true)
	err = s.store.RecordEvidence(t.Context(), w.id, "comment/100", data, true, func(current queue.State) error {
		candidate, err := current.PrepareBareReceipt(w.stageA.policy, b, actor, "100", receipt.CreatedAt, receipt.Supplied.BriefDigest)
		if err != nil {
			return err
		}
		if candidate != receipt {
			return errors.New("changed receipt basis")
		}
		return preflightStageA(current, []queue.Event{receipt})
	}, receipt)
	if err == nil {
		t.Fatal("stale receipt accepted")
	}
	raw, err := s.store.Observation(t.Context(), w.id, "comment/100")
	must(t, err)
	if len(raw) != 0 || len(shadowState(t, s, w).ReceiptRecords(r.ChangeID)) != 0 {
		t.Fatal("failed acceptance committed dedupe or evidence")
	}
}

func TestStageAReviewConditionWaitDoesNotTimeout(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	review, err := condition.Parse("approved-reviews-by = writer")
	must(t, err)
	// Install nested conditions in the isolated policy; public loader unchanged.
	w.cfg.MergeWhen = condition.All(w.cfg.MergeWhen, condition.Any(review, condition.Not(condition.Not(review))))
	f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue")))
	must(t, s.cycle(t.Context(), w))
	state := shadowState(t, s, w)
	b := state.BatchForPR(1)
	if b == nil {
		t.Fatal("no batch")
	}
	checks := []forge.CheckRun{{ID: 1, Name: "ci", Source: "actions", State: forge.Success}}
	terminal, success, err := s.stageACheckPolicy(t.Context(), w, b, checks)
	if !errors.Is(err, executor.ErrLandingApprovals) || terminal || success {
		t.Fatalf("wait misclassified %v/%v/%v", terminal, success, err)
	}
	if len(shadowState(t, s, w).ApprovalWaits()) != 1 {
		t.Fatal("wait missing")
	}
}

func TestStageAApprovalRejectionWorkerReplan(t *testing.T) {
	t.Parallel()
	t.Run("approval-rejection-worker-replan", func(t *testing.T) {
		t.Parallel()
		s, w, f := shadowFixture(t, 1, "main", "7", false)
		f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue")))
		must(t, s.cycle(t.Context(), w))
		state := shadowState(t, s, w)
		b := state.BatchForPR(1)
		if b == nil {
			t.Fatal("no batch")
		}
		shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
		_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: true}}, "")
		must(t, err)
		now := time.Now()
		cfg := w.plannerConfig(s.options.PollInterval)
		_, err = s.store.Plan(t.Context(), w.id, cfg, now)
		must(t, err)
		claims, err := s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, now)
		must(t, err)
		var land store.ClaimedAction
		for _, claim := range claims {
			if _, ok := claim.Action.(queue.Land); ok {
				land = claim
			}
		}
		if land.Key == "" {
			t.Fatal("missing Land")
		}
		err = s.execute(t.Context(), w, land)
		if !errors.Is(err, executor.ErrLandingApprovals) {
			t.Fatal("worker did not reject", err)
		}
		// Failed completion returns this same action to planned. Without another
		// Plan call, claim it again and prove BeginConsequential cancels it against
		// the durable rejection instead of entering executor or pushing the base.
		claims, err = s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, time.Now())
		must(t, err)
		var retry store.ClaimedAction
		for _, claim := range claims {
			if claim.Key == land.Key {
				retry = claim
			}
		}
		if retry.Key == "" {
			t.Fatal("Land completed Done instead of Failed")
		}
		proceed, err := s.store.BeginConsequential(t.Context(), retry, cfg, time.Now())
		must(t, err)
		if proceed {
			t.Fatal("rejected composition began again")
		}
		if err := s.store.Complete(t.Context(), retry, store.Done); !errors.Is(err, store.ErrClaimLost) {
			t.Fatal("stale claim was not cancelled", err)
		}
		state = shadowState(t, s, w)
		if len(state.LandingDecisions()) != 0 || len(state.ApprovalWaits()) != 1 {
			t.Fatal("unexpected replan evidence")
		}
	})
}

func TestStageAPinnedBaseRefreshAndPolicyRecovery(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	before := shadowDigest(t, s, w)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	initial, _ := state.Classification(r.ChangeID, r.Revision)
	base := state.BaseSHA
	// Ordinary forward base movement preserves the first pin and receipt digest.
	_, err := f.PushBase(repo, "main", "unrelated", map[string]string{"other.txt": "forward"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	if got := shadowDigest(t, s, w); got != before {
		t.Fatal("forward move invalidated receipt", got, before)
	}
	// A rewind refreshes the generation even with identical diff endpoints/paths.
	must(t, f.SetRef(repo, "main", base))
	must(t, s.reconcile(t.Context(), w, true))
	// The pinned base equals the rewind target here, so rewrite detection must use
	// the previously observed queue tip, independently of the original diff pin.
	state = shadowState(t, s, w)
	current, _ := state.Classification(r.ChangeID, r.Revision)
	if current.Evidence.Classification.Inputs.Generation <= initial.Evidence.Classification.Inputs.Generation {
		t.Fatal("rewind failed to refresh generation")
	}
	// A failed named identity pauses the config source; unchanged-commit recovery
	// clears exactly the recorded basis rather than falling back to old policy.
	w.stageA.repository.Groups = map[string][]string{"reviewers": {"not-found"}}
	must(t, s.configuration(t.Context(), w))
	state = shadowState(t, s, w)
	if !state.SourceBlocked(queue.ConfigSource) {
		t.Fatal("unresolved mapping did not pause")
	}
	f.SetIdentity(forge.UserIdentity{ID: 31, Login: "not-found", Kind: forge.UserAccount})
	must(t, s.configuration(t.Context(), w))
	state = shadowState(t, s, w)
	if state.SourceBlocked(queue.ConfigSource) {
		t.Fatal("basis-checked recovery failed")
	}
}

func TestStageABuildMetadataSurvivesRewriteThenForwardMove(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	initial := shadowState(t, s, w).BaseSHA
	// Build sibling B and forward C from A. Rewriting C to B refreshes the
	// classification pin at B; advancing B -> D must keep that pin usable.
	rewriteBase, err := f.PushBase(repo, "main", "branch B", map[string]string{"b.txt": "B"})
	must(t, err)
	must(t, f.SetRef(repo, "main", initial))
	_, err = f.PushBase(repo, "main", "branch C", map[string]string{"c.txt": "C"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	must(t, f.SetRef(repo, "main", rewriteBase))
	must(t, s.reconcile(t.Context(), w, true))
	_, err = f.PushBase(repo, "main", "forward D", map[string]string{"d.txt": "D"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	state := shadowState(t, s, w)
	revision, _ := state.PRRevision(1)
	classification, _ := state.Classification(revision.ChangeID, revision.Revision)
	if classification.Evidence.Classification.Inputs.Base != rewriteBase {
		t.Fatalf("refreshed base = %s, want rewrite target %s", classification.Evidence.Classification.Inputs.Base, rewriteBase)
	}

	f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue")))
	must(t, s.cycle(t.Context(), w))
	if batch := shadowState(t, s, w).BatchForPR(1); batch == nil || batch.TestedSHA == "" {
		t.Fatalf("build after rewrite then advance failed: batch=%+v", batch)
	}
}

func TestStageAUnReadableTeamAndDecisionTimeOwners(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	// Governing policy changes, never PR-owned CODEOWNERS content, define owners.
	_, err := f.PushBase(repo, "main", "owners", map[string]string{"CODEOWNERS": "* @org/reviewers"})
	must(t, err)
	f.SetTeam(forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}, []forge.UserIdentity{{ID: 21, Login: "writer", Kind: forge.UserAccount}})
	must(t, s.reconcile(t.Context(), w, true))
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	digest := shadowDigest(t, s, w)
	fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
	must(t, err)
	if len(fresh.OwnerPools["owned:team:org/reviewers"].IDs) != 1 {
		t.Fatal("fresh owners missing")
	}
	f.FailNext("TeamMembers", forge.ErrMembershipIncomplete)
	unknown, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
	must(t, err)
	if unknown.OwnerPools["owned:team:org/reviewers"].Resolved {
		t.Fatal("unreadable team became known")
	}
	f.SetTeam(forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}, []forge.UserIdentity{{ID: 22, Login: "replacement", Kind: forge.UserAccount}})
	next, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
	must(t, err)
	if next.OwnerPools["owned:team:org/reviewers"].IDs[0] != "github.com:user:22" {
		t.Fatal("reused stored owner pool")
	}
	if shadowDigest(t, s, w) != digest {
		t.Fatal("fresh membership entered recorded brief identity")
	}
}

func TestStageAReceiptGateAllMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []queue.MergeMethod{queue.Squash, queue.Merge, queue.Rebase, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", true)
			w.cfg.MergeMethod = method
			must(t, s.configuration(t.Context(), w))
			must(t, s.reconcile(t.Context(), w, true))
			digest := shadowDigest(t, s, w)
			f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue"), shadowComment(101, "@openmerge approve "+digest)))
			batch := shadowBuildBatch(t, s, w, 1)
			shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
			shadowRefreshChecks(t, s, w)
			shadowLandBatch(t, s, w)
			events, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			decisions, starts := 0, 0
			for _, record := range events {
				if d, ok := record.Event.(queue.LandingDecisionRecorded); ok {
					decisions++
					if len(d.Approval.Sources) != 1 || d.Approval.Identity.BriefDigest != digest {
						t.Fatal("receipt not frozen in decision")
					}
				}
				if _, ok := record.Event.(queue.LandingStarted); ok {
					starts++
				}
			}
			if decisions != 1 || starts != 1 {
				t.Fatalf("receipt decision/start %d/%d", decisions, starts)
			}
		})
	}
}

func TestStageAGateIgnoresContributorReservedTextForEveryMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []queue.MergeMethod{queue.Squash, queue.Merge, queue.Rebase, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", true)
			state := shadowState(t, s, w)
			w.cfg.MergeMethod = method
			w.cfg.Batch.MaxSize = 2
			forgedID := "chg1_" + strings.Repeat("0", 64)
			plainSource := "Document OpenMerge-Change trailer format"
			forgedSource := "Contributor forged trailer pair\n\n" +
				"OpenMerge-Change: " + forgedID + "@1\n" +
				"OpenMerge-Classification: " + forgedID + "@1 none"
			if _, err := forge.ParseChangeTrailers(forgedSource); err != nil {
				t.Fatalf("forged contributor pair must be syntactically valid: %v", err)
			}
			appendSources := func(pr int) {
				t.Helper()
				// Each member has its original commit plus these two source commits.
				// Rebase must count the segment, yet parse neither source message.
				_, err := f.PushPR(repo, pr, plainSource)
				must(t, err)
				_, err = f.PushPR(repo, pr, forgedSource)
				must(t, err)
			}
			assertThreeSourceCommits := func(pr int, base string) {
				t.Helper()
				current, err := f.PullRequest(t.Context(), repo, pr)
				must(t, err)
				commits, truncated, err := f.FirstParentCommits(t.Context(), repo, base, current.HeadSHA, 4)
				must(t, err)
				if truncated || len(commits) != 3 {
					t.Fatalf("PR %d source commits = %d, truncated=%v, want three", pr, len(commits), truncated)
				}
			}
			appendSources(1)
			first, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			assertThreeSourceCommits(1, state.BaseSHA)
			secondBase := state.BaseSHA
			if method == queue.FastForward {
				secondBase = first.HeadSHA
			}
			_, err = f.OpenPR(repo, 2, secondBase, true)
			must(t, err)
			must(t, f.SetPRMetadata(repo, 2, "main", false, "contributor"))
			f.SetNativeReviews(repo, 2, forge.NativeReviews{})
			appendSources(2)
			assertThreeSourceCommits(2, secondBase)
			must(t, s.reconcile(t.Context(), w, true))
			digest := func(pr int) string {
				current := shadowState(t, s, w)
				revision, _ := current.PRRevision(pr)
				fact, err := current.RecordedBrief(w.stageA.policy, approvalBasis(revision))
				must(t, err)
				identity, err := brief.Identity(fact)
				must(t, err)
				return identity.BriefDigest
			}
			f.SetNativeReviews(repo, 1, shadowNative(f, w,
				shadowComment(100, "@openmerge queue"),
				shadowComment(101, "@openmerge approve "+digest(1)),
			))
			f.SetNativeReviews(repo, 2, shadowNative(f, w,
				shadowComment(200, "@openmerge queue"),
				shadowComment(201, "@openmerge approve "+digest(2)),
			))
			batch := shadowBuildBatch(t, s, w, 1)
			builtMembers := len(batch.PRs)
			shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
			shadowRefreshChecks(t, s, w)
			shadowLandBatch(t, s, w)
			if builtMembers != 2 {
				t.Fatalf("tested members = %d, want two multi-commit members", builtMembers)
			}
			events, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			starts := 0
			for _, event := range events {
				if _, ok := event.Event.(queue.LandingStarted); ok {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("contributor trailer text affected %s gate: starts=%d", method, starts)
			}
		})
	}
}

func TestStageAGateRejectsMalformedGeneratedMemberMetadata(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	digest := shadowDigest(t, s, w)
	f.SetNativeReviews(repo, 1, shadowNative(f, w,
		shadowComment(100, "@openmerge queue"),
		shadowComment(101, "@openmerge approve "+digest),
	))
	must(t, s.cycle(t.Context(), w))
	batch := shadowState(t, s, w).BatchForPR(1)
	if batch == nil || batch.TestedSHA == "" {
		t.Fatal("missing tested composition")
	}
	shadowChecks(t, s, w, f, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
	w.Git = &corruptTestedMessageGit{Forge: f, sha: batch.TestedSHA, message: "OpenMerge generated\n\nOpenMerge-Change: malformed"}
	err := s.cycle(t.Context(), w)
	if err == nil || !strings.Contains(err.Error(), "invalid tested change trailers") {
		t.Fatalf("malformed generated metadata was accepted: %v", err)
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	for _, event := range events {
		if _, ok := event.Event.(queue.LandingStarted); ok {
			t.Fatal("malformed generated metadata started a landing")
		}
	}
}

func TestStageAMalformedReceiptCapAndOversizedRaw(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	comments := []forge.NativeComment{}
	first := int64(100)
	if testing.Short() {
		// Seed the ordinary rejected receipts in one transaction. The commands
		// below still cross the real 64-receipt boundary, truncate oversized
		// input, and consume all capacity outcomes through the inclusive cursor.
		state := shadowState(t, s, w)
		r, _ := state.PRRevision(1)
		actor, err := policyIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
		must(t, err)
		var seed []queue.Event
		for id := int64(100); id < 163; id++ {
			event, err := state.PrepareBareReceipt(w.stageA.policy, approvalBasis(r), actor, fmt.Sprint(id), shadowComment(id, "").Created.Time, "BAD")
			must(t, err)
			must(t, state.CheckReceiptAdmission(event))
			seed = append(seed, event)
			state = queue.Apply(state, event)
		}
		_, err = s.store.Append(t.Context(), w.id, seed, "")
		must(t, err)
		first = 163
	}
	for i := first; i < 170; i++ {
		comments = append(comments, shadowComment(i, "@openmerge approve "+strings.Repeat("x", 2048)))
	}
	f.SetNativeReviews(repo, 1, shadowNative(f, w, comments...))
	must(t, s.commands(t.Context(), w, 1))
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	records := state.ReceiptRecords(r.ChangeID)
	if len(records) != 64 {
		t.Fatalf("rejection cap %d", len(records))
	}
	for _, record := range records {
		if record.ReasonCode != "receipt_digest_malformed" || len(record.Supplied.BriefDigest) > 1024 {
			t.Fatal("unbounded or unrecorded malformed digest")
		}
	}
	raw, err := s.store.Observation(t.Context(), w.id, "comment/169")
	must(t, err)
	if !strings.Contains(string(raw), "limit") {
		t.Fatal("cap blocked inclusive cursor", string(raw))
	}
}

func TestStageAReceiptConcurrentDedupe(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", true)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	b := approvalBasis(r)
	receipt, err := state.PrepareBareReceipt(w.stageA.policy, b, cp.Identity{ID: "github.com:user:21", Login: "writer", Kind: cp.HumanAccount}, "100", time.Now().Add(time.Hour), shadowDigest(t, s, w))
	must(t, err)
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			value, _ := json.Marshal(fmt.Sprint(i))
			errorsCh <- s.store.RecordEvidence(t.Context(), w.id, "comment/100", value, true, func(current queue.State) error { return preflightStageA(current, []queue.Event{receipt}) }, receipt)
		}(i)
	}
	for i := 0; i < 2; i++ {
		must(t, <-errorsCh)
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	count := 0
	for _, event := range events {
		if _, ok := event.Event.(queue.ApprovalReceiptRecorded); ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent acceptance emitted %d receipt events", count)
	}
}

func TestStageAFreshRevokedGrantBlocksAcceptedReceipt(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	digest := shadowDigest(t, s, w)
	f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue"), shadowComment(101, "@openmerge approve "+digest)))
	must(t, s.cycle(t.Context(), w))
	state := shadowState(t, s, w)
	b := state.BatchForPR(1)
	if b == nil {
		t.Fatal("no composition")
	}
	f.SetWriteGrant(repo, 21, "read")
	shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
	err := s.cycle(t.Context(), w)
	if err != nil && !errors.Is(err, executor.ErrLandingApprovals) {
		t.Fatal(err)
	}
	state = shadowState(t, s, w)
	if len(state.ApprovalWaits()) != 1 {
		t.Fatal("revoked grant reused")
	}
	events, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	for _, event := range events {
		if _, ok := event.Event.(queue.LandingStarted); ok {
			t.Fatal("revoked receipt authorized a landing")
		}
	}
}

func TestStageANoneStillRequiresExactHeadForkTrust(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	w.stageA.candidate.Approvals.Rules = []cp.Rule{{When: cp.Match{Paths: []string{"**"}}, Require: cp.Requirement{Selector: cp.None}}}
	state := shadowState(t, s, w)
	_, err := f.OpenPR(repo, 2, state.BaseSHA, false)
	must(t, err)
	f.SetNativeReviews(repo, 2, forge.NativeReviews{})
	pr, err := s.observe(t.Context(), w, 2)
	must(t, err)
	if trusted(pr) {
		t.Fatal("none bypassed exact-head fork trust")
	}
}

func TestStageAStaleReceiptAfterIntentEdit(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", true)
	digest := shadowDigest(t, s, w)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	edited := queue.Intent(approvalBasis(r), "new recorded intent", "")
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{edited}, "")
	must(t, err)
	// The store boundary, as used by commands, recomputes from recorded evidence.
	receipt, err := shadowState(t, s, w).PrepareBareReceipt(w.stageA.policy, approvalBasis(r), cp.Identity{ID: "github.com:user:21", Login: "writer", Kind: cp.HumanAccount}, "101", time.Now().Add(time.Hour), digest)
	must(t, err)
	if receipt.Accepted || receipt.ReasonCode != "receipt_digest_mismatch" || receipt.Supplied.BriefDigest != digest {
		t.Fatal("stale digest rebound")
	}
}

func TestStageANativeNoiseFilteringRetainsAuthenticatedCommands(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	f.SetWriteGrant(repo, 12, "write")
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	native := forge.NativeReviews{Reviews: []forge.NativeReview{}}
	for i := int64(1); i < 20; i++ {
		native.Reviews = append(native.Reviews, forge.NativeReview{ID: i, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}, State: forge.ReviewChangesRequested, Submitted: forge.NativeTime{Time: time.Now(), Source: forge.ReviewSubmissionTime}})
	}
	c := shadowComment(100, "@openmerge unapprove")
	c.Author = forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}
	native.Comments = append(native.Comments, c)
	f.SetNativeReviews(repo, 1, native)
	_, err = s.observe(t.Context(), w, 1)
	must(t, err)
	must(t, s.commands(t.Context(), w, 1))
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	facts, _ := state.ApprovalFacts(r.ChangeID)
	if len(facts.Sources) != 1 {
		t.Fatal("native noise was retained, or the authenticated withdrawal was lost")
	}
	for _, source := range facts.Sources {
		if source.Event.Stance.Source.Channel != cp.CommentCommand || source.Event.Stance.Kind != cp.Withdraw {
			t.Fatal("expected only the authenticated withdrawal")
		}
	}

}

func TestStageARenamedReviewerFreshGrantAndMissingSource(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	native := forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime}}}}
	f.SetNativeReviews(repo, 1, native)
	_, err = s.observe(t.Context(), w, 1)
	must(t, err)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	f.SetIdentity(forge.UserIdentity{ID: 21, Login: "renamed", Kind: forge.UserAccount})
	native.Reviews[0].Author.Login = "renamed"
	f.SetNativeReviews(repo, 1, native)
	fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
	must(t, err)
	if fresh.Actors["github.com:user:21"].Grant.Permits("github.com:user:21") != cp.True {
		t.Fatal("renamed source's stable ID did not recover")
	}
	f.SetNativeReviews(repo, 1, forge.NativeReviews{})
	fresh, err = s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
	must(t, err)
	if fresh.Actors["github.com:user:21"].Grant.Permits("github.com:user:21") == cp.True {
		t.Fatal("missing native approval reused a stored source")
	}
}

func TestStageAStaticMappingSwapCannotBorrowPool(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"group", "owner"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			if kind == "group" {
				w.stageA.repository.Groups = map[string][]string{"reviewers": {"writer"}}
				w.stageA.candidate.Approvals.Default = cp.Requirement{Selector: cp.Group, Group: "reviewers", Count: 1}
			} else {
				_, err := f.PushBase(repo, "main", "owner", map[string]string{"CODEOWNERS": "* @writer"})
				must(t, err)
				w.stageA.candidate.Approvals.Rules = []cp.Rule{{When: cp.Match{Touches: []string{"**"}}, Require: cp.Requirement{Selector: cp.CodeOwners}}}
			}
			must(t, s.reconcile(t.Context(), w, true))
			old := w.stageA.policy.Digest
			// Reassignment after the governing read but before the grant read. ID B
			// cannot borrow the static pool pinned to A, even with its own writer grant.
			f.SetIdentity(forge.UserIdentity{ID: 22, Login: "writer", Kind: forge.UserAccount})
			f.SetWriteGrant(repo, 22, "write")
			state := shadowState(t, s, w)
			r, _ := state.PRRevision(1)
			fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
			must(t, err)
			pool := fresh.Groups["reviewers"]
			if kind == "owner" {
				pool = fresh.OwnerPools["owned:user:writer"]
			}
			if pool.Resolved || pool.Fresh {
				t.Fatal("reassigned identity borrowed pinned pool", pool)
			}
			must(t, s.reconcile(t.Context(), w, true))
			if w.stageA.policy.Digest == old {
				t.Fatal("unchanged-commit identity reassignment did not change semantic pin")
			}
		})
	}
}

func TestStageACompositionRejectsOnlyUnapprovedMember(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 4, "main", "7", false)
	state := shadowState(t, s, w)
	data, err := f.ReadFile(t.Context(), repo, state.ConfigCommit, ".openmerge.yml")
	must(t, err)
	configured := strings.Replace(string(data), "    merge_when:", "    batch: {max_size: 2}\n    merge_when:", 1)
	_, err = f.PushBase(repo, "main", "batch policy", map[string]string{".openmerge.yml": configured})
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	state = shadowState(t, s, w)
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	_, err = f.OpenPR(repo, 2, state.BaseSHA, true)
	must(t, err)
	approved := shadowNative(f, w, shadowComment(100, "@openmerge queue"))
	approved.Reviews = []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime}}}
	f.SetNativeReviews(repo, 1, approved)
	f.SetNativeReviews(repo, 2, shadowNative(f, w, shadowComment(101, "@openmerge queue")))
	if testing.Short() {
		b := shadowBuildBatch(t, s, w, 1)
		if len(b.PRs) != 2 {
			t.Fatal("expected two-member composition")
		}
		shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
		shadowRefreshChecks(t, s, w)
		shadowRejectLanding(t, s, w)
		b = shadowBuildBatch(t, s, w, 1)
		if len(b.PRs) != 1 || b.PRs[0].PR != 1 {
			t.Fatal("rebuild did not isolate the approved member")
		}
		shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
		shadowRefreshChecks(t, s, w)
		shadowLandBatch(t, s, w)
	} else {
		must(t, s.cycle(t.Context(), w))
		state = shadowState(t, s, w)
		b := state.BatchForPR(1)
		if b == nil || len(b.PRs) != 2 {
			t.Fatal("expected two-member composition")
		}
		shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
		err = s.cycle(t.Context(), w)
		if err != nil && !errors.Is(err, executor.ErrLandingApprovals) {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			must(t, s.cycle(t.Context(), w))
			state = shadowState(t, s, w)
			if b := state.BatchForPR(1); b != nil && b.TestedSHA != "" {
				shadowChecks(t, s, w, f, b.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: b.TestRef}})
			}
		}
	}
	state = shadowState(t, s, w)
	tip, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	if !f.ContainsPR(repo, tip, 1) || f.ContainsPR(repo, tip, 2) {
		t.Fatal("unapproved composition landed or approved member failed to progress")
	}
	if len(state.ApprovalWaits()) != 1 {
		t.Fatalf("unapproved member lost its wait: batch size=%d, digest=%s, entries=%+v", w.cfg.Batch.MaxSize, state.ConfigDigest, state.Entries)
	}
}

// Drive the actual planner, claims, consequential start and worker, without
// repeatedly polling or executing unrelated reporting actions to reach a phase.
func shadowBuildBatch(t *testing.T, s *Server, w *worker, pr int) *queue.Batch {
	t.Helper()
	must(t, s.reconcile(t.Context(), w, false))
	cfg := w.plannerConfig(s.options.PollInterval)
	// Rejected compositions must be discarded through the executor before a
	// replacement can be built. Do not bypass the durable invalidation step.
	planned, err := s.store.Plan(t.Context(), w.id, cfg, time.Now())
	must(t, err)
	for _, action := range planned {
		if _, ok := action.Action.(queue.InvalidateBatches); ok {
			claim := claimStageAAction(t, s, w, cfg, func(a queue.Action) bool {
				_, ok := a.(queue.InvalidateBatches)
				return ok
			})
			must(t, s.execute(t.Context(), w, claim))
			break
		}
	}
	claim := claimStageAAction(t, s, w, cfg, func(action queue.Action) bool {
		switch action.(type) {
		case queue.BuildTestRef, queue.BuildChainedTestRef:
			return true
		}
		return false
	})
	must(t, s.execute(t.Context(), w, claim))
	batch := shadowState(t, s, w).BatchForPR(pr)
	if batch == nil || batch.TestedSHA == "" {
		t.Fatal("missing tested composition")
	}
	return batch
}

func shadowRefreshChecks(t *testing.T, s *Server, w *worker) {
	t.Helper()
	cfg := w.plannerConfig(s.options.PollInterval)
	claim := claimStageAAction(t, s, w, cfg, func(action queue.Action) bool {
		_, ok := action.(queue.RefreshChecks)
		return ok
	})
	must(t, s.execute(t.Context(), w, claim))
}

func shadowRejectLanding(t *testing.T, s *Server, w *worker) {
	t.Helper()
	cfg := w.plannerConfig(s.options.PollInterval)
	claim := claimStageAAction(t, s, w, cfg, func(action queue.Action) bool {
		_, ok := action.(queue.Land)
		return ok
	})
	if err := s.execute(t.Context(), w, claim); !errors.Is(err, executor.ErrLandingApprovals) {
		t.Fatalf("landing error = %v, want approval wait", err)
	}
}

func shadowLandBatch(t *testing.T, s *Server, w *worker) {
	t.Helper()
	cfg := w.plannerConfig(s.options.PollInterval)
	claim := claimStageAAction(t, s, w, cfg, func(action queue.Action) bool {
		_, ok := action.(queue.Land)
		return ok
	})
	must(t, s.execute(t.Context(), w, claim))
	unsettled, err := s.settle(t.Context(), w)
	must(t, err)
	if unsettled {
		t.Fatal("fake landing did not settle")
	}
	if !testing.Short() {
		must(t, s.cycle(t.Context(), w))
		return
	}
	// Repeated settlement must be a journal no-op, including in short runs.
	before, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	unsettled, err = s.settle(t.Context(), w)
	must(t, err)
	after, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	if unsettled || before != after {
		t.Fatal("repeated settlement changed the journal")
	}
}

func shadowChecks(t *testing.T, s *Server, w *worker, f *fake.Forge, sha string, checks []forge.CheckRun) {
	t.Helper()
	f.SetCheckRuns(repo, sha, checks)
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.RefreshRequested{}}, "")
	must(t, err)
}

func TestStageAGoverningAttributesIndependentOfOpenChanges(t *testing.T) {
	t.Parallel()
	s, w, f := shadowFixture(t, 1, "main", "7", true)
	_, err := f.PushBase(repo, "main", "nested attributes", map[string]string{"docs/.gitattributes": "*.md linguist-generated"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	before := w.stageA.policy.Digest
	receipt := shadowDigest(t, s, w)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	c, _ := state.Classification(r.ChangeID, r.Revision)
	_, err = f.OpenPRWithFiles(repo, 2, state.BaseSHA, true, map[string]string{"docs/new.md": "new docs"})
	must(t, err)
	f.SetNativeReviews(repo, 2, forge.NativeReviews{})
	must(t, s.reconcile(t.Context(), w, true))
	if w.stageA.policy.Digest != before || shadowDigest(t, s, w) != receipt {
		t.Fatal("opening a change altered unchanged governing policy")
	}
	after, _ := shadowState(t, s, w).Classification(r.ChangeID, r.Revision)
	if after.Evidence.Classification.Inputs.Generation != c.Evidence.Classification.Inputs.Generation {
		t.Fatal("new change invalidated a sibling's classification")
	}
	_, err = f.PushBase(repo, "main", "attribute content", map[string]string{"docs/.gitattributes": "*.md -linguist-generated"})
	must(t, err)
	must(t, s.reconcile(t.Context(), w, true))
	if w.stageA.policy.Digest == before {
		t.Fatal("governing attribute content did not refresh semantic policy")
	}
}

func TestStageACommandsRetainBindingsAcrossMembershipChanges(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"unapprove", "approve"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			s, w, f := shadowFixture(t, 1, "main", "7", false)
			team := forge.TeamIdentity{ID: 50, OrganizationID: 5, Organization: "org", Slug: "reviewers"}
			writer := forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}
			f.SetTeam(team, []forge.UserIdentity{writer})
			w.stageA.repository.Groups = map[string][]string{"reviewers": {"org/reviewers"}}
			w.stageA.candidate.Approvals.Default = cp.Requirement{Selector: cp.Group, Group: "reviewers", Count: 1}
			must(t, s.reconcile(t.Context(), w, true))
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			native := forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: writer, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().Add(time.Minute), Source: forge.ReviewSubmissionTime}}}}
			f.SetNativeReviews(repo, 1, native)
			must(t, s.reconcile(t.Context(), w, true))
			digest := shadowDigest(t, s, w)
			f.SetTeam(team, []forge.UserIdentity{})
			body := "@openmerge unapprove"
			if verb == "approve" {
				body = "@openmerge approve " + digest
			}
			native.Comments = []forge.NativeComment{shadowComment(300, body)}
			f.SetNativeReviews(repo, 1, native)
			must(t, s.commands(t.Context(), w, 1))
			state := shadowState(t, s, w)
			r, _ := state.PRRevision(1)
			facts, _ := state.ApprovalFacts(r.ChangeID)
			found := false
			for _, source := range facts.Sources {
				if source.Event.Stance.Source.ID == "300" {
					found = true
				}
			}
			if !found {
				t.Fatal("authenticated command lost its current-revision binding while actor was outside the pool")
			}
			if verb == "approve" && !state.ReceiptRecords(r.ChangeID)["300"].Accepted {
				t.Fatal("membership entered bare-digest acceptance")
			}
			f.SetTeam(team, []forge.UserIdentity{writer})
			must(t, s.reconcile(t.Context(), w, true))
			state = shadowState(t, s, w)
			fresh, err := s.readStageAInputs(t.Context(), w, state, approvalBasis(r))
			must(t, err)
			fact, err := state.RecordedBrief(w.stageA.policy, fresh.Basis)
			must(t, err)
			identity, err := brief.Identity(fact)
			must(t, err)
			evaluation := state.EvaluateApprovals(w.stageA.policy, identity, fresh)
			if evaluation.Allowed != (verb == "approve") {
				t.Fatal("membership change revived the withdrawn older native approval, or discarded a valid receipt", evaluation)
			}
			if identity.BriefDigest != digest {
				t.Fatal("membership changed recorded brief identity")
			}
		})
	}
}
