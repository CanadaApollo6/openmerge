package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// Only forge identities, reviews and checks are synthetic. Configuration,
// classification, generated objects and all ref leases use the local bare origin.
type acceptanceGitDriver struct {
	*fake.Forge
	fixture       compositionFixture
	pr            forge.PullRequest
	defaultBranch string
}

func (d *acceptanceGitDriver) DefaultBranch(context.Context, forge.RepoRef) (string, error) {
	return d.defaultBranch, nil
}
func (d *acceptanceGitDriver) ReadFile(_ context.Context, _ forge.RepoRef, ref, path string) ([]byte, error) {
	if path != ".openmerge.yml" {
		return nil, forge.ErrFileNotFound
	}
	return []byte(d.fixture.git("", "--git-dir="+d.fixture.remote, "show", ref+":"+path)), nil
}
func (d *acceptanceGitDriver) PullRequest(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
	return d.pr, nil
}
func (d *acceptanceGitDriver) PullRequestMetadata(ctx context.Context, r forge.RepoRef, n int) (forge.PullRequest, error) {
	return d.PullRequest(ctx, r, n)
}
func (d *acceptanceGitDriver) ListOpenPullRequests(context.Context, forge.RepoRef, string) ([]forge.PullRequestSummary, error) {
	return []forge.PullRequestSummary{{Number: d.pr.Number, HeadSHA: d.pr.HeadSHA}}, nil
}

type acceptanceGit struct {
	compositionFixture
	s      *Server
	driver *acceptanceGitDriver
	cache  string
}

// history is imported in one Git process; it is below the governing commit, so
// the authority creation inventory must exclude it rather than scan all history.
// This deliberately uses the same private opt-in as production's dormant seam.
func acceptanceGitFixture(t *testing.T, method queue.MergeMethod, history int, mutate ...func(*authority.Observation)) acceptanceGit {
	t.Helper()
	f := newCompositionFixture(t, []int{1})
	cfg := lifecycleConfig(repositoryConfig) + fmt.Sprintf("    merge_method: %s\n", method)
	blob := f.git(cfg, "--git-dir="+f.remote, "hash-object", "-w", "--stdin")
	owners := f.git("* @writer\n", "--git-dir="+f.remote, "hash-object", "-w", "--stdin")
	tree := f.git("100644 blob "+blob+"\t.openmerge.yml\n100644 blob "+owners+"\tCODEOWNERS\n", "--git-dir="+f.remote, "mktree")
	if history > 0 {
		var input strings.Builder
		for i := 1; i <= history; i++ {
			fmt.Fprintf(&input, "commit refs/heads/history\nmark :%d\ncommitter Fixture <fixture@example.test> 1700000000 +0000\ndata 2\nx\n", i)
			if i == 1 {
				fmt.Fprintf(&input, "from %s\n", f.base)
			} else {
				fmt.Fprintf(&input, "from :%d\n", i-1)
			}
			input.WriteByte('\n')
		}
		f.git(input.String(), "--git-dir="+f.remote, "fast-import", "--quiet")
		f.base = f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/history")
	}
	f.base = f.git("authority config\n", "--git-dir="+f.remote, "commit-tree", tree, "-p", f.base)
	f.git("", "--git-dir="+f.remote, "update-ref", "refs/heads/main", f.base)
	content := f.git("accepted change\n", "--git-dir="+f.remote, "hash-object", "-w", "--stdin")
	sourceTree := f.git("100644 blob "+blob+"\t.openmerge.yml\n100644 blob "+owners+"\tCODEOWNERS\n100644 blob "+content+"\tREADME.md\n", "--git-dir="+f.remote, "mktree")
	head := f.git("source first\n", "--git-dir="+f.remote, "commit-tree", sourceTree, "-p", f.base)
	head = f.git("source second\n", "--git-dir="+f.remote, "commit-tree", sourceTree, "-p", head)
	f.git("", "--git-dir="+f.remote, "update-ref", "refs/pull/1/head", head)
	f.prs[0].HeadSHA = head
	f.pins[0].HeadSHA = head
	cache := filepath.Join(filepath.Dir(f.remote), "acceptance-cache")
	makeGit := func() *gitops.Repo {
		g, err := gitops.New(t.Context(), gitops.Config{WorkDir: cache, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return f.remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
		must(t, err)
		return g
	}
	f.g = makeGit()
	if entries, err := os.ReadDir(cache); err != nil || len(entries) != 1 || entries[0].Name() != "askpass" {
		t.Fatalf("fixture cache not cold: %v, entries=%d", err, len(entries))
	}
	forgeFake := fake.NewDeterministic()
	forgeFake.Init(f.w.repo(), "main", "openmerge/fence/main")
	forgeFake.SetIdentity(forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount})
	forgeFake.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
	forgeFake.SetWriteGrant(f.w.repo(), 21, "write")
	forgeFake.SetTeam(forge.TeamIdentity{ID: 200, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
	forgeFake.SetNativeReviews(f.w.repo(), 1, forge.NativeReviews{})
	d := &acceptanceGitDriver{Forge: forgeFake, fixture: f, defaultBranch: "main", pr: forge.PullRequest{Number: 1, HeadSHA: head, Base: "main", Author: "contributor", AuthorID: 12, Title: "accepted change", HeadInBaseRepo: true, HeadBranch: "feature", FilesKnown: true, Files: []string{"README.md"}, LabelsKnown: true, ConflictKnown: true}}
	db := database(t)
	s, err := newServer(db, Options{})
	must(t, err)
	target := Target{Key: f.w.Key, Git: f.g, Driver: d, BotLogin: fake.Identity, AuthorityRequired: true}
	w, err := s.register(t.Context(), target)
	must(t, err)
	f.w = w
	w.managed = false
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(t, err)
	decoded, err := queue.DecodeEvent(raw)
	must(t, err)
	draft := decoded.(queue.WriteRestrictionVerified)
	for _, change := range mutate {
		change(&draft.Evidence)
	}
	_, err = db.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: f.base}}, "")
	must(t, err)
	receipt := activationReceipt(t, db)
	draftID := int64(0)
	w.resolveAuthorityLifecycle = func(_ context.Context, c config.AuthorityCandidate, _, _ string) (authorityLifecycleResolution, error) {
		policy := authority.Policy{Mode: c.Authority.Mode.Normalized(), BreakGlass: authority.ActorBinding{Declared: *c.Authority.BreakGlass, Resolved: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: "200"}}}}
		return authorityLifecycleResolution{Policy: policy, RepositoryID: 123, Activation: authorityActivation{Receipt: receipt, ReceiptMaxAge: time.Hour, DraftID: draftID, Input: forge.WriteRestrictionInput{AppID: "5383059", ExpectedRefs: []string{"refs/heads/main"}}, Read: func(context.Context) (authority.Observation, error) { return draft.Evidence, nil }}}, nil
	}
	must(t, s.loadConfiguration(t.Context(), w, "main", f.base))
	must(t, s.refreshStageAPolicy(t.Context(), w, f.base))
	request, err := s.prepareAuthorityLifecycle(t.Context(), w, lifecycleState(t, s, w), "main", f.base)
	must(t, err)
	draft.Binding = request.Input.Binding
	draft.VerifiedAt = time.Now().Add(-time.Minute)
	draft.Basis = 0
	draft.Nonce = "acceptance-" + string(method)
	draft.Evidence, err = forge.InterpretWriteRestriction(request.Input, draft.Evidence)
	must(t, err)
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	must(t, err)
	receipts, err := db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
	must(t, err)
	draftID = receipts[0].EventID
	must(t, s.configuration(t.Context(), w))
	options := s.authorityPushOptions(t.Context(), w, forge.PushEnsureFence, []forge.PushRefIntent{{Ref: "refs/heads/openmerge/fence/main"}})
	fence, err := f.g.EnsureFence(t.Context(), w.repo(), "openmerge/fence/main", options)
	must(t, err)
	_, err = db.Append(t.Context(), w.id, []queue.Event{queue.FenceInitialized{Token: fence}}, "")
	must(t, err)
	_, err = db.Append(t.Context(), w.id, []queue.Event{queue.BaseMoved{SHA: f.base}}, "")
	must(t, err)
	_, err = s.observe(t.Context(), w, 1)
	must(t, err)
	_, err = db.Append(t.Context(), w.id, []queue.Event{queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
	must(t, err)
	return acceptanceGit{compositionFixture: f, s: s, driver: d, cache: cache}
}

func TestAuthorityAcceptanceColdCacheAllMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []queue.MergeMethod{queue.FastForward, queue.Rebase, queue.Merge, queue.Squash} {
		t.Run(string(method), func(t *testing.T) {
			t.Parallel()
			testSynctest(t, func(t *testing.T) {
				history := 0
				if method == queue.Merge {
					history = forge.MaxCommitHistoryCommits + 1
				}
				f := acceptanceGitFixture(t, method, history)
				s, w := f.s, f.w
				must(t, os.RemoveAll(f.cache))
				cfg := w.plannerConfig(s.options.PollInterval)
				claim := claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
				must(t, s.execute(t.Context(), w, claim))
				batch := lifecycleState(t, s, w).BatchForPR(1)
				if batch == nil || batch.TestedSHA == "" {
					t.Fatal("authority build did not produce tested object")
				}
				tested, ref := batch.TestedSHA, batch.TestRef
				if got := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/"+ref); got != tested {
					t.Fatal("remote tested ref mismatch", got, tested)
				}
				// #84: the landing process starts without a single local Git object. The
				// tested marker/composition must be acquired from its advertised remote ref.
				must(t, os.RemoveAll(f.cache))
				cold, err := gitops.New(t.Context(), gitops.Config{WorkDir: f.cache, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return f.remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
				must(t, err)
				w.Git = cold
				f.driver.fixture.g = cold
				if entries, err := os.ReadDir(f.cache); err != nil || len(entries) != 1 || entries[0].Name() != "askpass" {
					t.Fatalf("restart cache not cold: %v, entries=%d", err, len(entries))
				}
				f.driver.SetCheckRuns(w.repo(), tested, []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 2, State: forge.Success, HeadBranch: ref}})
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: tested, Success: true}}, "")
				must(t, err)
				claim = claimLandingChecksAction(t, s, w, cfg, func(a queue.Action) bool { _, ok := a.(queue.Land); return ok })
				must(t, s.execute(t.Context(), w, claim))
				state := lifecycleState(t, s, w)
				landed := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/main")
				want := tested
				if method == queue.FastForward {
					want = f.pins[0].HeadSHA
				}
				if landed != want || state.BaseSHA != landed {
					t.Fatalf("remote=%s state=%s want=%s", landed, state.BaseSHA, want)
				}
				fence := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/openmerge/fence/main")
				if state.Fence != fence {
					t.Fatal("remote fence differs from settlement")
				}
				rows, err := s.store.EventsAfter(t.Context(), w.id, 0)
				must(t, err)
				metadata, err := forge.ParseChangeTrailers(f.git("", "--git-dir="+f.remote, "show", "-s", "--format=%B", tested))
				must(t, err)
				if len(metadata) != 1 {
					t.Fatal("remote tested object lost member trailers")
				}
				decisions, checks, starts, settlements := 0, 0, 0, 0
				journalReplay := queue.State{BaseBranch: w.Key.BaseBranch}
				decisionAttempt := ""
				for i, row := range rows {
					journalReplay = queue.Apply(journalReplay, row.Event)
					switch e := row.Event.(type) {
					case queue.LandingDecisionRecorded:
						decisions++
						decisionAttempt = e.AttemptID
						if e.TestedSHA != tested || e.LandedSHA != landed || e.Approval == nil || e.Approval.Basis.Head != f.pins[0].HeadSHA || e.Approval.Identity.ChangeID != metadata[0].ChangeID || e.Approval.Identity.Revision != metadata[0].Revision || e.Approval.Identity.PolicyDigest != w.stageA.policy.Digest || len(e.Approval.Requirements) != 0 || e.Approval.ClassificationDigest == "" || e.Approval.EligibilityReadID == "" {
							t.Fatalf("wrong recorded authority decision: %+v", e)
						}
						t.Logf("decision_event=%d attempt=%s classification=%s eligibility=%s", row.ID, e.AttemptID, e.Approval.ClassificationDigest, e.Approval.EligibilityReadID)
					case queue.LandingChecksRecorded:
						checks++
						if e.TestedSHA != tested || e.LandedSHA != landed || e.ConfigCommit != f.base || e.ConfigDigest != state.ConfigDigest || decisionAttempt == "" || e.AttemptID != decisionAttempt || e.PolicyDigest != w.stageA.policy.Digest || len(e.Checks) != 1 || e.Checks[0].RunID == nil || *e.Checks[0].RunID != 701 || e.Checks[0].Attempt == nil || *e.Checks[0].Attempt != 2 || e.Checks[0].Source != "app:15368" || e.Checks[0].Result != forge.Success {
							t.Fatalf("wrong persisted evidence: %+v", e)
						}
						start, ok := rows[i+1].Event.(queue.LandingStarted)
						if !ok || start.AttemptID != e.AttemptID {
							t.Fatal("checks/start lost atomic adjacency")
						}
						t.Logf("checks_event=%d start_event=%d tested=%s landed=%s fence=%s", row.ID, rows[i+1].ID, tested, landed, fence)
					case queue.LandingStarted:
						starts++
					case queue.Landed:
						settlements++
						if e.NewBaseSHA != landed || e.NewFence != fence {
							t.Fatal("settlement lost remote values")
						}
					}
				}
				if decisions != 1 || checks != 1 || starts != 1 || settlements != 1 {
					t.Fatal("decision/check/start/settlement counts", decisions, checks, starts, settlements)
				}
				if journalReplay.BaseSHA != landed || journalReplay.Fence != fence || !reflect.DeepEqual(journalReplay.LandingDecisions(), state.LandingDecisions()) {
					t.Fatal("full journal replay lost remote settlement/decisions")
				}
				snapshot, err := queue.EncodeSnapshot(state)
				must(t, err)
				replay, err := queue.DecodeSnapshot(snapshot)
				must(t, err)
				if replay.BaseSHA != landed || replay.Fence != fence || !reflect.DeepEqual(replay.LandingDecisions(), state.LandingDecisions()) {
					t.Fatal("recorded decisions lost on snapshot round trip")
				}
			})
		})
	}
}

// #82's inclusive production bound is exercised under -short without replaying
// 10,000 commits through the rebase builder. Only the over-limit attempt runs
// the executor: its durable composition refusal must precede any generated push.
func TestAuthorityAcceptanceHistoryBoundary(t *testing.T) {
	t.Parallel()
	if forge.MaxFirstParentCommits != 10_000 {
		t.Fatalf("production first-parent limit = %d, want 10000", forge.MaxFirstParentCommits)
	}
	testSynctest(t, func(t *testing.T) {
		f := acceptanceGitFixture(t, queue.Rebase, 0)
		var input strings.Builder
		for i := 1; i <= 10_001; i++ {
			fmt.Fprintf(&input, "commit refs/pull/1/head\nmark :%d\ncommitter Fixture <fixture@example.test> 1700000000 +0000\ndata 2\nx\n", i)
			if i == 1 {
				fmt.Fprintf(&input, "from %s\nM 100644 inline README.md\ndata 6\nchange\n", f.base)
			} else {
				fmt.Fprintf(&input, "from :%d\n", i-1)
			}
			input.WriteByte('\n')
			if i == 10_000 {
				fmt.Fprintf(&input, "reset refs/pull/2/head\nfrom :%d\n\n", i)
			}
		}
		f.git(input.String(), "--git-dir="+f.remote, "fast-import", "--quiet", "--force")
		atLimit := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/pull/2/head")
		overLimit := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/pull/1/head")
		must(t, os.RemoveAll(f.cache))
		for _, tc := range []struct {
			name, head string
			count      int
		}{{"exact-10000", atLimit, 10_000}, {"lookahead-10001", overLimit, 10_001}} {
			t.Log(tc.name)
			must(t, os.RemoveAll(f.cache))
			observed, err := readRebaseComposition(t.Context(), f.w, queue.BuildTestRef{BatchID: "boundary", BaseSHA: f.base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: tc.head}}}, forge.MaxFirstParentCommits)
			must(t, err)
			if len(observed.Counts) != 1 || observed.Counts[0] != tc.count || observed.Limit != 10_000 {
				t.Fatalf("wrong production boundary: %+v", observed)
			}
			selected, rejected, _ := observed.Select()
			if tc.count == 10_000 && (len(selected) != 1 || len(rejected) != 0) || tc.count > 10_000 && (len(selected) != 0 || len(rejected) != 1) {
				t.Fatalf("wrong boundary selection: %+v", observed)
			}
		}
		f.driver.pr.HeadSHA = overLimit
		_, err := f.s.observe(t.Context(), f.w, 1)
		must(t, err)
		cfg := f.w.plannerConfig(f.s.options.PollInterval)
		for _, action := range queue.Plan(cfg, lifecycleState(t, f.s, f.w), time.Now()) {
			if d, ok := action.(queue.Dequeue); ok && d.PR == 1 {
				_, err = f.s.store.Append(t.Context(), f.w.id, []queue.Event{queue.Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason}}, "")
				must(t, err)
			}
		}
		_, err = f.s.store.Append(t.Context(), f.w.id, []queue.Event{queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
		must(t, err)
		before := f.git("", "--git-dir="+f.remote, "show-ref")
		must(t, os.RemoveAll(f.cache))
		claim := claimLandingChecksAction(t, f.s, f.w, cfg, func(a queue.Action) bool { _, ok := a.(queue.BuildTestRef); return ok })
		must(t, f.s.execute(t.Context(), f.w, claim))
		rows, err := f.s.store.EventsAfter(t.Context(), f.w.id, 0)
		must(t, err)
		found := false
		for _, row := range rows {
			switch e := row.Event.(type) {
			case queue.RebaseCompositionObserved:
				found = true
				if len(e.Counts) != 1 || e.Counts[0] != 10_001 {
					t.Fatal("over-limit count not durable", e)
				}
			case queue.TestRefBuilt, queue.LandingStarted, queue.Landed:
				t.Fatalf("over-limit source reached generated build/land: %T", e)
			}
		}
		if !found {
			t.Fatal("executor omitted durable composition refusal")
		}
		state := lifecycleState(t, f.s, f.w)
		plannedDequeue := false
		for _, a := range queue.Plan(cfg, state, time.Now()) {
			if d, ok := a.(queue.Dequeue); ok && d.PR == 1 && d.Reason == queue.RebaseCompositionExceeded {
				plannedDequeue = true
			}
		}
		if !plannedDequeue {
			t.Fatal("over-limit source did not plan composition dequeue")
		}
		if after := f.git("", "--git-dir="+f.remote, "show-ref"); after != before {
			t.Fatal("composition rejection changed real remote refs")
		}
	})
}
