package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/audit"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type retentionDriver struct {
	*fake.Forge
	git func(...string) string
	pr  forge.PullRequest
}

func (d retentionDriver) ReadFile(_ context.Context, _ forge.RepoRef, ref, path string) ([]byte, error) {
	if path != ".openmerge.yml" {
		return nil, forge.ErrFileNotFound
	}
	return []byte(d.git("show", ref+":"+path)), nil
}
func (d retentionDriver) PullRequest(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
	return d.pr, nil
}
func (d retentionDriver) PullRequestMetadata(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
	return d.pr, nil
}
func (d retentionDriver) ListOpenPullRequests(context.Context, forge.RepoRef, string) ([]forge.PullRequestSummary, error) {
	return []forge.PullRequestSummary{{Number: d.pr.Number, HeadSHA: d.pr.HeadSHA}}, nil
}

// Forge observations are fake; journal writes and retry dispatch callbacks use
// the production executor. The fake provider completes CI deterministically.
func (d retentionDriver) RerunFailedChecks(ctx context.Context, r forge.RepoRef, req forge.RerunChecksRequest) (forge.RerunChecksResult, error) {
	target := forge.RerunTarget{WorkflowRunID: 700, Attempt: 1}
	if err := req.Progress(target); err != nil {
		return forge.RerunChecksResult{}, err
	}
	result, err := d.Forge.RerunFailedChecks(ctx, r, req)
	if err != nil {
		return result, err
	}
	target.Accepted = true
	if err := req.Progress(target); err != nil {
		return result, err
	}
	runs, err := d.CheckRuns(ctx, r, req.TestedSHA)
	if err != nil {
		return result, err
	}
	for i := range runs {
		if runs[i].State == forge.Pending {
			runs[i].WorkflowRunID = 700
			runs[i].State = forge.Failure
			// The first batch exhausts its retry. The second retains a successful
			// retry as a pre-window dependency of an abandoned landing decision.
			if req.Runs[0].ID == 20 {
				runs[i].State = forge.Success
			}
		}
	}
	d.SetCheckRuns(r, req.TestedSHA, runs)
	return result, nil
}

type retentionGit struct {
	*gitops.Repo
	f        *fake.Forge
	builds   atomic.Int32
	rejected atomic.Bool
}

func (g *retentionGit) BuildTestRef(ctx context.Context, r forge.RepoRef, b forge.BuildRequest, o forge.PushOptions) (string, error) {
	sha, err := g.Repo.BuildTestRef(ctx, r, b, o)
	if err == nil {
		n := g.builds.Add(1)
		state := forge.Success
		if n <= 2 {
			state = forge.Failure
		}
		g.f.SetCheckRuns(r, sha, []forge.CheckRun{{ID: uint64(n) * 10, Attempt: 1, WorkflowRunID: 700, Source: "app:15368", Name: "ci", State: state, HeadBranch: b.Ref}})
	}
	return sha, err
}

// A rejected atomic push leaves the base untouched. The executor must settle
// the failed attempt through its real fence-abandonment recovery path.
func (g *retentionGit) Land(ctx context.Context, r forge.RepoRef, req forge.LandRequest, opts forge.PushOptions) (string, error) {
	if g.rejected.CompareAndSwap(false, true) {
		return "", forge.ErrLeaseRejected
	}
	return g.Repo.Land(ctx, r, req, opts)
}

// Completion is latched after the production cycle publishes health.
type retentionCycleLog struct {
	slog.Handler
	completed chan struct{}
}

func (h retentionCycleLog) Enabled(context.Context, slog.Level) bool { return true }
func (h retentionCycleLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "reconcile cycle finished" {
		select {
		case h.completed <- struct{}{}:
		default:
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h retentionCycleLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}
func (h retentionCycleLog) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}

func TestRetentionLifecycle(t *testing.T) {
	scratch := newRetentionScratch(t)
	dsn := scratch.database()
	must := func(err error) { t.Helper(); retentionMust(t, err) }
	out := os.Getenv("OPENMERGE_10B_OUT")
	if out == "" {
		out = t.TempDir()
	} else {
		// Caller supplies an unused directory; never reuse another trial's outputs.
		must(os.Mkdir(out, 0700))
	}
	admin, err := pgx.Connect(t.Context(), dsn)
	must(err)
	defer func() { must(admin.Close(context.Background())) }()
	db, err := store.Open(t.Context(), dsn)
	must(err)
	defer func() { db.Close() }()
	remote := filepath.Join(out, "remote.git")
	git := func(input string, args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Stdin = strings.NewReader(input)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Retention", "GIT_AUTHOR_EMAIL=retention@example.invalid", "GIT_COMMITTER_NAME=Retention", "GIT_COMMITTER_EMAIL=retention@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v: %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("", "init", "--bare", remote)
	bare := func(args ...string) string { return git("", append([]string{"--git-dir=" + remote}, args...)...) }
	object := func(input string, args ...string) string {
		return git(input, append([]string{"--git-dir=" + remote}, args...)...)
	}
	parents := map[string]string{}
	commit := func(source, parent, content string) string {
		cfg := object(source, "hash-object", "-w", "--stdin")
		owners := object("* @writer\n", "hash-object", "-w", "--stdin")
		readme := object(content, "hash-object", "-w", "--stdin")
		tree := object("100644 blob "+cfg+"\t.openmerge.yml\n100644 blob "+owners+"\tCODEOWNERS\n100644 blob "+readme+"\tREADME.md\n", "mktree")
		args := []string{"commit-tree", tree}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		sha := object("retention fixture\n", args...)
		parents[sha] = parent
		return sha
	}
	const source = "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues:\n- name: main\n  base: main\n  merge_method: merge\n  checks: {retry_failed: 1}\n  merge_when: [check-success = ci]\n  approvals: {default: {human: true, count: 1, receipt: true}}\n"
	githubSource := strings.Replace(strings.Replace(source, "policy_authority: openmerge", "policy_authority: github", 1), "break_glass: {team: acme/emergency}\n", "", 1)
	tip := commit(githubSource, "", "base")
	bare("update-ref", "refs/heads/main", tip)
	head := commit(source, tip, "changed")
	bare("update-ref", "refs/pull/1/head", head)
	r := forge.RepoRef{Path: "acme/retention"}
	key := store.QueueKey{Installation: "7", Repository: r.Path, BaseBranch: "main"}
	f := fake.New()
	f.Init(r, "main", "openmerge/fence/main")
	f.SetIdentity(forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount})
	f.SetIdentity(forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount})
	f.SetWriteGrant(r, 21, "write")
	f.SetCanWrite(r, "writer", true)
	f.SetCanWrite(r, "contributor", true)
	f.SetTeam(forge.TeamIdentity{ID: 200, OrganizationID: 100, Organization: "acme", Slug: "emergency"}, nil)
	fakeBase, err := f.ReadRef(t.Context(), r, "main")
	must(err)
	_, err = f.OpenPR(r, 1, fakeBase, true)
	must(err)
	d := retentionDriver{Forge: f, git: bare, pr: forge.PullRequest{Number: 1, HeadSHA: head, Base: "main", Author: "contributor", AuthorID: 12, Title: "retained change", HeadInBaseRepo: true, HeadBranch: "feature", FilesKnown: true, Files: []string{"README.md"}, LabelsKnown: true, ConflictKnown: true}}
	root := t.TempDir()
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(root, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
	must(err)
	// Push guards need an independent reader because the writer holds its cache lock.
	completionGit, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(root, "completion-cache"), HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }, Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }})
	must(err)
	id, err := db.EnsureQueue(t.Context(), key)
	must(err)
	wait := func(label string, predicate func(queue.State) bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			state, _, e := db.Load(t.Context(), id)
			must(e)
			if predicate(state) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("timed out", label)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	historicalGit := &retentionGit{Repo: g, f: f}
	repo := serve.Repository{Installation: "7", Name: r.Path, Branches: func(context.Context) ([]string, error) { return []string{"main"}, nil }, Target: func(base string) serve.Target {
		k := key
		k.BaseBranch = base
		return serve.Target{Key: k, Driver: d, Git: historicalGit, AuthorityCompletionGit: completionGit, BotLogin: fake.Identity}
	}}
	// Establish genesis through normal GitHub-mode discovery before adoption.
	bootstrap, err := serve.NewRepositories(t.Context(), db, []serve.Repository{repo}, serve.Options{PollInterval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	must(err)
	runServer := func(server *serve.Server) func() {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- server.Run(ctx) }()
		return func() {
			if cancel != nil {
				cancel()
				must(<-done)
				cancel = nil
			}
		}
	}
	bootstrapStop := runServer(bootstrap)
	defer bootstrapStop()
	wait("bootstrap configuration", func(st queue.State) bool { return st.ConfigCommit == tip })
	bootstrapStop()
	tip = commit(source, tip, "base")
	bare("update-ref", "refs/heads/main", tip)
	candidate, _, err := config.LoadAuthorityCandidate([]byte(source))
	must(err)
	selected, diags := candidate.Config.ResolveBranch("main", "main")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	pd, err := cp.PolicyDigest(cp.DigestInputs{Repository: candidate.Config.Policy, Queue: candidate.Config.Policy.Queues[0], Resolved: []cp.ResolvedIdentity{{Name: "writer", ID: "github.com:user:21", Kind: cp.HumanAccount}}, Owners: cp.GoverningContent{Path: "CODEOWNERS", Known: true, Present: true, Content: "* @writer\n"}, Attributes: []cp.GoverningContent{{Path: ".gitattributes", Known: true}}, ClassifierVersion: classification.Version, RequirePolicyOwners: true})
	must(err)
	legacy, err := cp.WithPolicyDigest(selected.Digest(false), pd)
	must(err)
	policy := authority.Policy{Mode: authority.OpenMerge, BreakGlass: authority.ActorBinding{Declared: *candidate.Authority.BreakGlass, Resolved: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: "200"}}}}
	digest, err := authority.EffectiveDigest(authority.DigestInputs{LegacyDigest: legacy, Policy: policy, DefaultBranch: "main", MergeMethod: authority.MergeMethod(selected.MergeMethod), Selectors: candidate.BaseSelectors()})
	must(err)
	coverage, err := authority.CoverageDigest("main", candidate.BaseSelectors())
	must(err)
	input := forge.WriteRestrictionInput{Repo: r, AppID: "5383059", BaseSelectors: candidate.BaseSelectors(), ExpectedRefs: []string{"refs/heads/main"}, Binding: authority.PolicyBinding{InstallationID: 7, RepositoryID: 123, Repository: r.Path, QueueKey: store.AuthorityQueueKey(key), EffectiveDigest: digest, CoverageDigest: coverage, DefaultBranch: "main", MergeMethod: "merge", DeclaredActor: policy.BreakGlass.Declared, ResolvedActor: policy.BreakGlass.Resolved}}
	raw, err := os.ReadFile("../../internal/queue/testdata/wire/v1/event_write_restriction_verified.json")
	must(err)
	event, err := queue.DecodeEvent(raw)
	must(err)
	draft := event.(queue.WriteRestrictionVerified)
	draft.Binding = input.Binding
	draft.Basis = 0
	draft.Nonce = "retention-adoption"
	draft.VerifiedAt = time.Now().UTC()
	draft.Evidence.Protections[0].Includes = []string{"refs/heads/main"}
	draft.Evidence, err = forge.InterpretWriteRestriction(input, draft.Evidence)
	must(err)
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	must(err)
	must(f.SetWriteRestrictionObservation(r, draft.Evidence, nil))
	if authority.RunningBinaryRevision() == "" {
		old := authority.BuildRevision
		authority.BuildRevision = "retention-test"
		defer func() { authority.BuildRevision = old }()
	}
	backup := filepath.Join(root, "receipt-backup")
	must(os.WriteFile(backup, []byte("scratch receipt backup"), 0600))
	identity, err := db.DatabaseIdentity(t.Context())
	must(err)
	now := time.Now().UTC()
	receipt := authority.EmissionReceipt{DatabaseIdentity: identity, BinaryRevision: authority.RunningBinaryRevision(), DrainedWorkers: []string{"scratch"}, DrainedAt: now.Add(-time.Minute), RestoreTestedAt: now.Add(-time.Second), BackupPath: backup, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("scratch receipt backup"))), CompatibleReadersOnly: true, Inventory: &authority.WorkerInventory{Complete: true, Workers: []authority.StoppedWorker{{ID: "scratch", BinaryRevision: "prior", StoppedAt: now.Add(-time.Minute)}}}}
	receipt.Reconciliation = &authority.RestoreReconciliation{SourceDatabaseIdentity: identity, RestoredDatabaseIdentity: "scratch-restored", BackupSHA256: receipt.BackupSHA256, BackupCapturedAt: receipt.DrainedAt, BackupWatermark: 0, RestoredWatermark: 0, ReconciledAt: receipt.RestoreTestedAt, EvidenceSHA256: receipt.BackupSHA256, AllRefsAndEvidenceReconciled: true}
	writeJSON := func(path string, v any) { b, e := json.Marshal(v); must(e); must(os.WriteFile(path, b, 0600)) }
	must(admin.QueryRow(t.Context(), "SELECT max(id) FROM events").Scan(&receipt.Reconciliation.BackupWatermark))
	receipt.Reconciliation.RestoredWatermark = receipt.Reconciliation.BackupWatermark
	receiptPath := filepath.Join(root, "receipt.json")
	writeJSON(receiptPath, receipt)
	records, err := db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: id, Event: draft}})
	must(err)
	planPath := filepath.Join(root, "plan.json")
	writePlan := func() {
		writeJSON(planPath, map[string]any{"targets": []serve.AuthorityTarget{{Key: key, Input: input, DraftID: records[0].EventID}}})
	}
	writePlan()
	var s *serve.Server
	serverStarts := 0
	var completed chan struct{}
	var stop func()
	start := func() {
		o := serveOptions{app: 5383059, authorityPlan: planPath, emissionReceipt: receiptPath, emissionReceiptMaxAge: time.Hour, poll: 250 * time.Millisecond}
		runtime, e := productionAuthorityRuntime(t.Context(), o, db)
		must(e)
		completed = make(chan struct{}, 1)
		s, e = serve.NewRepositories(t.Context(), db, []serve.Repository{repo}, productionServeOptions(o, runtime, nil, slog.New(retentionCycleLog{slog.NewTextHandler(os.Stderr, nil), completed})))
		must(e)
		serverStarts++
		stop = runServer(s)
	}
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	inventory := func() string { return retentionSQL(t, admin, retentionInventorySQL) }
	original := inventory()
	assertInventory := func() { current := inventory(); retentionPrefix(t, original, current); original = current }
	windowFrom := "2000-01-01T00:00:00Z"
	export := func(name, manifest string) string {
		reader, e := audit.Open(t.Context(), dsn, time.Minute)
		must(e)
		defer reader.Close()
		path := filepath.Join(out, name)
		if manifest == "" {
			_, e = reader.Export(t.Context(), audit.Request{Installation: "7", Repository: r.Path, From: windowFrom, Until: "2100-01-01T00:00:00Z"}, path)
		} else {
			_, e = reader.Replay(t.Context(), manifest, path)
		}
		must(e)
		return path
	}
	equal := func(a, b string) {
		for _, name := range []string{"manifest.json", "events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
			x, e := os.ReadFile(filepath.Join(a, name))
			must(e)
			y, e := os.ReadFile(filepath.Join(b, name))
			must(e)
			if !bytes.Equal(x, y) {
				t.Fatal("export differs", name)
			}
		}
	}
	t.Setenv("OPENMERGE_API_TOKEN", "retention-read-token")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	start()
	wait("initial openmerge adoption", func(st queue.State) bool {
		return st.AuthorityPin() != nil && st.AuthorityPin().Authority == authority.OpenMerge
	})
	assertInventory()
	must(s.RefreshBranches(t.Context()))
	f.AddComment(r, 1, "contributor", "@openmerge queue")
	f.SetNativeReviews(r, 1, forge.NativeReviews{Comments: []forge.NativeComment{{ID: 101, Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}, Body: "@openmerge queue", Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}}}, Reviews: []forge.NativeReview{{ID: 201, CommitID: head, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}}}})
	api := httptest.NewServer(status.Handler(s, []store.QueueKey{key}, "retention-read-token", nil))
	defer api.Close()
	changeID, e := key.ChangeID(1)
	must(e)
	var view brief.View
	wait("recorded brief", func(queue.State) bool {
		var stdout, stderr bytes.Buffer
		if runChange([]string{"brief", string(changeID), "--repo", r.Path, "--api", api.URL, "--json"}, &stdout, &stderr) != 0 {
			return false
		}
		must(json.Unmarshal(stdout.Bytes(), &view))
		for _, stance := range view.Stances {
			if stance.Channel == "native_review" && stance.Head == d.pr.HeadSHA && len(view.Digest) == 64 {
				return true
			}
		}
		return false
	})
	native, e := f.NativeReviews(t.Context(), r, 1)
	must(e)
	for i, digest := range []string{strings.Repeat("0", 64), view.Digest} {
		native.Comments = append(native.Comments, forge.NativeComment{ID: int64(410 + i), Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, Body: "@openmerge approve " + digest, Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}})
	}
	f.SetNativeReviews(r, 1, native)
	must(s.Notify(t.Context(), "7", r.Path, "receipts-1"))
	api.Close()
	// Wait for durable failure and dequeue before submitting another command;
	// no wall-clock sampling can race the planner's failed-batch cleanup.
	wait("failed retry dequeued", func(st queue.State) bool {
		var n int
		must(admin.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE type='checks_failure_confirmed'").Scan(&n))
		return n == 1 && len(st.Entries) == 0
	})
	native.Comments = append(native.Comments, forge.NativeComment{ID: 500, Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}, Body: "@openmerge queue", Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}})
	f.SetNativeReviews(r, 1, native)
	must(s.Notify(t.Context(), "7", r.Path, "requeue-after-failure"))
	wait("settled decision", func(_ queue.State) bool {
		var n int
		must(admin.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE type='landed'").Scan(&n))
		return n > 0
	})
	stop()
	original = inventory()
	must(os.WriteFile(filepath.Join(out, "inventory-before.json"), []byte(inventory()), 0600))
	landedSHA := bare("rev-parse", "refs/heads/main")
	landings := map[string]string{landedSHA: tip + " " + head}
	// Full history retains the exhausted batch, which has no landing decision.
	// The narrow decision export independently exercises pre-window retry closure.
	history := export("history", "")
	retentionHistorical(t, admin, history)
	var decisionAt time.Time
	must(admin.QueryRow(t.Context(), "SELECT recorded_at FROM events WHERE type='landing_decision_recorded' ORDER BY id LIMIT 1").Scan(&decisionAt))
	windowFrom = decisionAt.UTC().Format(time.RFC3339Nano)
	before := export("before", "")
	selection, err := os.ReadFile(filepath.Join(before, "selection.jsonl"))
	must(err)
	dependencies := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(selection), []byte("\n")) {
		var row audit.Selection
		must(json.Unmarshal(line, &row))
		if row.Role == "dependency" {
			recordedAt, e := time.Parse(time.RFC3339Nano, row.Source.RecordedAt)
			must(e)
			if recordedAt.Before(decisionAt) {
				dependencies[row.Source.Type] = true
			}
		}
	}
	for _, kind := range []string{"policy_authority_applied", "change_revision_observed", "change_accountability_observed", "change_classified", "approval_receipt_recorded", "batch_started", "test_ref_built", "checks_observed", "check_rerun_requested", "check_rerun_dispatch_progress", "check_rerun_dispatched"} {
		if !dependencies[kind] {
			t.Fatal("missing dependency type", kind)
		}
	}
	manifest := filepath.Join(before, "manifest.json")
	replayHistory := func(name string) {
		current := export(name+"-history", filepath.Join(history, "manifest.json"))
		retentionHistorical(t, admin, current)
		equal(history, current)
	}
	for i, mode := range []string{"github", "openmerge"} {
		next := strings.Replace(source, "policy_authority: openmerge", "policy_authority: "+mode, 1)
		if mode == "github" {
			next = strings.Replace(next, "break_glass: {team: acme/emergency}\n", "", 1)
			next = strings.Replace(next, ", receipt: true", "", 1)
			// Keep the legacy checks family alongside the new retry-aware history.
			next = strings.Replace(next, "  checks: {retry_failed: 1}\n", "", 1)
		}
		tip = commit(next, bare("rev-parse", "refs/heads/main"), "changed")
		bare("update-ref", "refs/heads/main", tip)
		if mode == "openmerge" {
			draft.VerifiedAt = time.Now().UTC()
			draft.Nonce = "retention-return"
			state, _, e := db.Load(t.Context(), id)
			must(e)
			draft.Basis = state.RulesSourceRevision(queue.WriteRestriction)
			records, err = db.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: id, Event: draft}})
			must(err)
			writePlan()
		}
		start()
		wait(mode+" pair", func(st queue.State) bool {
			return st.AuthorityPin() != nil && string(st.AuthorityPin().Authority) == mode && st.ConfigCommit == tip
		})
		stop()
		if mode == "github" {
			secondHead := commit(next, tip, "second change")
			bare("update-ref", "refs/pull/2/head", secondHead)
			d.pr.Number = 2
			d.pr.HeadSHA = secondHead
			_, err = f.OpenPR(r, 2, fakeBase, true)
			must(err)
			f.SetNativeReviews(r, 2, forge.NativeReviews{Comments: []forge.NativeComment{{ID: 301, Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}, Body: "@openmerge queue", Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}}}, Reviews: []forge.NativeReview{{ID: 302, CommitID: secondHead, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}}}})
			start()
			wait("github landing", func(queue.State) bool {
				var n int
				must(admin.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE type='landed'").Scan(&n))
				return n == 2
			})
			stop()
			landings[bare("rev-parse", "refs/heads/main")] = tip + " " + secondHead
		}
		assertInventory()
		equal(before, export(fmt.Sprintf("transition-%d", i), manifest))
		replayHistory(fmt.Sprintf("transition-%d", i))
		db.Close()
		db, err = store.Open(t.Context(), dsn)
		must(err)
	}
	tip = commit("version: 1\nqueues: [{name: other, base: release, merge_when: [check-success = ci]}]\n", tip, "changed")
	bare("update-ref", "refs/heads/main", tip)
	start()
	must(s.RefreshBranches(t.Context()))
	wait("retired", func(st queue.State) bool { return st.LifecycleState() == "retired" })
	stop()
	assertInventory()
	equal(before, export("retired-replay", manifest))
	replayHistory("retired")
	db.Close()
	db, err = store.Open(t.Context(), dsn)
	must(err)
	retiredInventory := inventory() + retentionSQL(t, admin, "SELECT count(*)::text FROM actions")
	oldServer := s
	start()
	restarted := s
	if restarted == oldServer {
		t.Fatal("store reopen reused old server")
	}
	wait("retired server discovery and cycle", func(queue.State) bool {
		select {
		case <-completed:
		default:
			return false
		}
		h, e := restarted.Health(t.Context())
		must(e)
		if len(h.Queues) != 1 || len(restarted.Keys()) != 1 || restarted.Keys()[0] != key {
			return false
		}
		q := h.Queues[0]
		return q.Lifecycle == "retired" && !q.Readiness.Ready && !q.Watched
	})
	must(restarted.RefreshBranches(t.Context()))
	wait("retired after reopen", func(st queue.State) bool { return st.LifecycleState() == "retired" })
	stop()
	if s != restarted {
		t.Fatal("restart server identity changed")
	}
	if inventory()+retentionSQL(t, admin, "SELECT count(*)::text FROM actions") != retiredInventory {
		t.Fatal("retired server processed new work")
	}
	assertInventory()
	equal(before, export("restart-replay", manifest))
	replayHistory("restart")
	if serverStarts != 6 {
		t.Error("missing lifecycle start", serverStarts)
	}
	var pins string
	must(admin.QueryRow(t.Context(), `SELECT string_agg(payload->'data'->>'authority',',' ORDER BY id) FROM events WHERE type='policy_authority_applied'`).Scan(&pins))
	if pins != "openmerge,github,openmerge" {
		t.Fatal("wrong ordered authority pins", pins)
	}
	for kind, want := range map[string]int{"landed": 2, "checks_completed": 1, "landing_decision_recorded": 3, "policy_authority_applied": 3, "write_restriction_verified": 2, "approval_receipt_recorded": 2, "lifecycle_changed": 1} {
		var n int
		must(admin.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE type=$1", kind).Scan(&n))
		if n != want {
			t.Fatal("wrong event count", kind, n, want)
		}
	}
	if retentionSQL(t, admin, `SELECT count(DISTINCT payload->'data'->>'accepted')::text FROM events WHERE type='approval_receipt_recorded'`) != "2" {
		t.Fatal("missing accepted/rejected receipts")
	}
	var invalidPairs int
	must(admin.QueryRow(t.Context(), `SELECT count(*) FROM events p LEFT JOIN events c ON c.queue_id=p.queue_id AND c.id=(SELECT max(id) FROM events WHERE queue_id=p.queue_id AND id<p.id) WHERE p.type='policy_authority_applied' AND (c.type IS DISTINCT FROM 'config_applied' OR c.payload->'data'->>'commit' IS DISTINCT FROM p.payload->'data'->>'governing_commit' OR c.payload->'data'->>'digest' IS DISTINCT FROM p.payload->'data'->>'effective_digest')`).Scan(&invalidPairs))
	if invalidPairs != 0 {
		t.Fatal("nonadjacent authority/config pair")
	}
	final := export("final", "")
	must(os.WriteFile(filepath.Join(out, "inventory-final.json"), []byte(inventory()), 0600))
	retentionProofs(t, scratch, dsn, out, admin, inventory, equal, landings, parents)
	t.Logf("retention inventory sha256=%x final=%s", sha256.Sum256([]byte(inventory())), final)
}

type retentionScratch struct {
	t                *testing.T
	base             *pgx.Conn
	route            url.URL
	databases, roles []string
}

func retentionMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func retentionName(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s%x", prefix, b)
}
func newRetentionScratch(t *testing.T) *retentionScratch {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	ci := os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("CI") == "true"
	if raw == "" {
		if ci {
			t.Fatal("CI retention acceptance requires Postgres")
		}
		t.Skip("set OPENMERGE_TEST_DATABASE_URL")
	}
	u, err := url.Parse(raw)
	retentionMust(t, err)
	for _, e := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(e), "PG") {
			t.Fatal("PG routing overrides forbidden")
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	retentionMust(t, err)
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() != "localhost" || (u.Port() != "54329" && (!ci || u.Port() != "5432")) || u.Fragment != "" || len(q) > 1 || (len(q) == 1 && (len(q["sslmode"]) != 1 || q.Get("sslmode") != "disable")) {
		t.Fatal("only guarded shared test Postgres is permitted")
	}
	u.Path = "/postgres"
	u.RawPath = ""
	u.RawQuery = "sslmode=disable"
	base, err := pgx.Connect(t.Context(), u.String())
	retentionMust(t, err)
	s := &retentionScratch{t: t, base: base, route: *u}
	t.Cleanup(func() {
		for group, names := range [][]string{s.databases, s.roles} {
			for i := len(names) - 1; i >= 0; i-- {
				_, e := base.Exec(context.Background(), "DROP "+[]string{"DATABASE", "ROLE"}[group]+" "+pgx.Identifier{names[i]}.Sanitize())
				if e != nil {
					t.Error(e)
				}
			}
		}
		var remaining int
		retentionMust(t, base.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM pg_database WHERE datname=ANY($1))+(SELECT count(*) FROM pg_roles WHERE rolname=ANY($2))`, s.databases, s.roles).Scan(&remaining))
		if remaining != 0 {
			t.Error("owned scratch resources remain", remaining)
		}
		retentionMust(t, base.Close(context.Background()))
	})
	return s
}
func (s *retentionScratch) database() string {
	name := retentionName("om_9c2c_")
	_, err := s.base.Exec(s.t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	retentionMust(s.t, err)
	s.databases = append(s.databases, name)
	u := s.route
	u.Path = "/" + name
	return u.String()
}
func retentionSQL(t *testing.T, c *pgx.Conn, sql string) string {
	t.Helper()
	var result string
	retentionMust(t, c.QueryRow(t.Context(), sql).Scan(&result))
	return result
}
func retentionManifest(t *testing.T, dir string) audit.Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	retentionMust(t, err)
	var m audit.Manifest
	retentionMust(t, json.Unmarshal(b, &m))
	return m
}

func retentionProofs(t *testing.T, s *retentionScratch, dsn, out string, owner *pgx.Conn, inventory func() string, equal func(string, string), landings, parents map[string]string) {
	t.Helper()
	m := retentionManifest(t, filepath.Join(out, "final"))
	digests := map[string]string{}
	var commands []map[string]any
	export := func(t *testing.T, url, name, manifest string, want int) string {
		t.Helper()
		t.Setenv("OPENMERGE_DATABASE_URL", url)
		target := filepath.Join(out, name)
		args := []string{"export", "--out", target}
		if manifest != "" {
			args = append(args, "--manifest", manifest)
		} else {
			args = append(args, "--repo", m.Request.Repository, "--installation", m.Request.Installation, "--from", m.Request.From, "--until", m.Request.Until)
		}
		var stdout, stderr bytes.Buffer
		code := runAudit(args, &stdout, &stderr)
		commands = append(commands, map[string]any{"args": args, "stdout": stdout.String(), "stderr": stderr.String(), "exit": code})
		if code != want {
			t.Fatalf("export %s exit=%d stderr=%s", name, code, stderr.String())
		}
		if want == 0 {
			saved := retentionManifest(t, target)
			if stderr.Len() != 0 || strings.Count(stdout.String(), "\n") != 2 || stdout.String() != target+"\nmanifest SHA-256: "+saved.ManifestSHA256+"\n" {
				t.Fatal("printed manifest digest differs", name)
			}
			digests[name] = saved.ManifestSHA256
		} else if stdout.Len() != 0 || stderr.String() != "audit: connection, source, integrity or publication failure\n" {
			t.Fatal("wrong negative diagnostic", name, stderr.String())
		}
		return target
	}
	before := filepath.Join(out, "before")
	equal(filepath.Join(out, "final"), export(t, dsn, "source-cli", "", 0))
	equal(before, export(t, dsn, "source-replay", filepath.Join(before, "manifest.json"), 0))
	role := retentionName("om_10b_")
	_, err := s.base.Exec(t.Context(), "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '"+role+"'")
	retentionMust(t, err)
	s.roles = append(s.roles, role)
	u, err := url.Parse(dsn)
	retentionMust(t, err)
	_, err = owner.Exec(t.Context(), fmt.Sprintf(`REVOKE CREATE,TEMP ON DATABASE %s FROM PUBLIC; REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT CONNECT ON DATABASE %s TO %s; GRANT USAGE ON SCHEMA public TO %s; GRANT SELECT ON events,queues TO %s`, pgx.Identifier{strings.TrimPrefix(u.Path, "/")}.Sanitize(), pgx.Identifier{strings.TrimPrefix(u.Path, "/")}.Sanitize(), role, role, role))
	retentionMust(t, err)
	u.User = url.UserPassword(role, role)
	readerDSN := u.String()
	reader, err := pgx.Connect(t.Context(), readerDSN)
	retentionMust(t, err)
	defer func() { retentionMust(t, reader.Close(context.Background())) }()
	// Effective privileges include PUBLIC and inherited grants, across every public relation.
	violations := retentionSQL(t, owner, fmt.Sprintf(`SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND ((c.relkind IN ('r','p','v','m','f') AND (has_table_privilege('%s',c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege('%s',c.oid,'INSERT,UPDATE,REFERENCES') OR has_table_privilege('%s',c.oid,'SELECT')<>(c.relname IN ('events','queues')))) OR (c.relkind='S' AND has_sequence_privilege('%s',c.oid,'USAGE,SELECT,UPDATE')))`, role, role, role, role))
	if violations != "0" || retentionSQL(t, reader, `SELECT (has_database_privilege(current_user,current_database(),'CREATE,TEMP') OR has_schema_privilege(current_user,'public','CREATE'))::text`) != "false" {
		t.Fatal("unexpected effective operator privileges", violations)
	}
	for _, sql := range []string{`INSERT INTO events(id,queue_id,type,payload) VALUES(-1,1,'base_moved','{}')`, `UPDATE events SET type=type`, `DELETE FROM events`, `CREATE TABLE public.forbidden(id int)`, `CREATE TEMP TABLE forbidden(id int)`} {
		_, err = reader.Exec(t.Context(), sql)
		pe, ok := err.(*pgconn.PgError)
		if !ok || pe.Code != "42501" {
			t.Fatalf("probe %s: wanted SQLSTATE 42501, got %v", sql, err)
		}
	}
	equal(filepath.Join(out, "final"), export(t, readerDSN, "reader", "", 0))
	equal(before, export(t, readerDSN, "reader-replay", filepath.Join(before, "manifest.json"), 0))
	for _, table := range []string{"events", "queues"} {
		_, err = owner.Exec(t.Context(), "REVOKE SELECT ON "+table+" FROM "+role)
		retentionMust(t, err)
		export(t, readerDSN, "missing-"+table, "", 1)
		_, err = owner.Exec(t.Context(), "GRANT SELECT ON "+table+" TO "+role)
		retentionMust(t, err)
	}
	// Remove an actual closure dependency in an isolated DB, never the source.
	selection, err := os.ReadFile(filepath.Join(before, "selection.jsonl"))
	retentionMust(t, err)
	var dependency audit.Source
	for _, line := range bytes.Split(bytes.TrimSpace(selection), []byte("\n")) {
		var row audit.Selection
		retentionMust(t, json.Unmarshal(line, &row))
		if row.Role == "dependency" && row.Source.Type == "policy_authority_applied" {
			dependency = row.Source
			break
		}
	}
	if dependency.ID == 0 {
		t.Fatal("missing authority dependency")
	}
	// A journal-only clone makes dependency deletion independent of client availability.
	cloneDSN := s.database()
	cloneStore, e := store.Open(t.Context(), cloneDSN)
	retentionMust(t, e)
	cloneStore.Close()
	clone, e := pgx.Connect(t.Context(), cloneDSN)
	retentionMust(t, e)
	defer func() { retentionMust(t, clone.Close(context.Background())) }()
	var rows map[string]json.RawMessage
	retentionMust(t, json.Unmarshal([]byte(inventory()), &rows))
	for _, table := range []string{"queues", "events"} {
		_, e = clone.Exec(t.Context(), "INSERT INTO "+table+" SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", rows[table])
		retentionMust(t, e)
	}
	_, e = clone.Exec(t.Context(), "DELETE FROM events WHERE queue_id=$1 AND id=$2", dependency.QueueID, dependency.ID)
	retentionMust(t, e)
	export(t, cloneDSN, "missing-dependency", filepath.Join(before, "manifest.json"), 1)
	t.Run("restore", func(t *testing.T) {
		version := retentionSQL(t, owner, "SHOW server_version_num")
		var server, dumpMajor int
		_, e := fmt.Sscanf(version, "%d", &server)
		retentionMust(t, e)
		for _, tool := range []string{"pg_dump", "pg_restore"} {
			b, e := exec.CommandContext(t.Context(), tool, "--version").CombinedOutput()
			var major int
			_, parseErr := fmt.Sscanf(string(b), tool+" (PostgreSQL) %d", &major)
			if e != nil || parseErr != nil || major < server/10000 || (tool == "pg_restore" && major < dumpMajor) {
				t.Skipf("restore leg only: %s missing or incompatible with server %s: %s", tool, version, b)
			}
			dumpMajor = major
		}
		run := func(tool string, args ...string) {
			t.Helper()
			b, e := exec.CommandContext(t.Context(), tool, args...).CombinedOutput()
			if e != nil {
				t.Fatalf("%s failed: %v: %s", tool, e, b)
			}
		}
		dump := filepath.Join(out, "journal.dump")
		run("pg_dump", "-Fc", "--file="+dump, dsn)
		restored := s.database()
		run("pg_restore", "--exit-on-error", "--dbname="+restored, dump)
		c, e := pgx.Connect(t.Context(), restored)
		retentionMust(t, e)
		defer func() { retentionMust(t, c.Close(context.Background())) }()
		if retentionSQL(t, c, retentionInventorySQL) != inventory() {
			t.Fatal("restore inventory differs")
		}
		equal(filepath.Join(out, "final"), export(t, restored, "restored", "", 0))
		equal(before, export(t, restored, "restored-before", filepath.Join(before, "manifest.json"), 0))
		restoredHistory := export(t, restored, "restored-history", filepath.Join(out, "history", "manifest.json"), 0)
		retentionHistorical(t, c, restoredHistory)
		equal(filepath.Join(out, "history"), restoredHistory)
	})
	summary, err := json.Marshal(map[string]any{"printed_manifest_sha256": digests, "commands": commands, "dependency_removed": dependency, "roles": s.roles, "databases": s.databases, "uid": os.Getuid(), "inventory_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(inventory())))})
	retentionMust(t, err)
	retentionMust(t, os.WriteFile(filepath.Join(out, "summary.json"), summary, 0600))
	retentionGitReadback(t, out, landings, parents)
}

const retentionInventorySQL = `SELECT jsonb_build_object('queues',(SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM queues q),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e))::text`

func retentionGitReadback(t *testing.T, out string, landings, parents map[string]string) {
	t.Helper()
	git := func(args ...string) string {
		b, e := exec.CommandContext(t.Context(), "git", append([]string{"--git-dir=" + filepath.Join(out, "remote.git")}, args...)...).CombinedOutput()
		retentionMust(t, e)
		return strings.TrimSpace(string(b))
	}
	for _, dir := range []string{"final", "restored"} {
		b, e := os.ReadFile(filepath.Join(out, dir, "events.jsonl"))
		if dir == "restored" && os.IsNotExist(e) {
			continue
		}
		retentionMust(t, e)
		for sha, expected := range landings {
			if git("show", "-s", "--format=%P", sha) != expected {
				t.Fatal("wrong landing Git parents", sha)
			}
			for _, field := range []string{"tested_sha", "landed_sha", "new_base_sha"} {
				if !bytes.Contains(b, []byte(`"`+field+`":"`+sha+`"`)) {
					t.Fatal("missing confirmed landing fact", dir, field, sha)
				}
			}
		}
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			var event struct {
				Payload struct {
					Data queue.OutsidePushRecorded
				}
			}
			retentionMust(t, json.Unmarshal(line, &event))
			d := event.Payload.Data
			if d.NewSHA != "" && (parents[d.NewSHA] != d.PreviousSHA || git("rev-list", d.NewSHA, "^"+d.PreviousSHA) != d.NewSHA) {
				t.Fatal("outside Git move differs")
			}
		}
	}
}
func retentionPrefix(t *testing.T, before, after string) {
	t.Helper()
	var old, next map[string][]json.RawMessage
	retentionMust(t, json.Unmarshal([]byte(before), &old))
	retentionMust(t, json.Unmarshal([]byte(after), &next))
	for _, table := range []string{"queues", "events"} {
		if len(next[table]) < len(old[table]) {
			t.Fatal("lost rows", table)
		}
		for i, row := range old[table] {
			if !bytes.Equal(row, next[table][i]) {
				t.Fatal("rewritten row", table, i)
			}
		}
	}
}

// The matrix is deliberately checked for completeness: deleting one family's
// assertion must not silently reduce the acceptance contract.
func retentionHistorical(t *testing.T, db *pgx.Conn, dir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "selection.jsonl"))
	retentionMust(t, err)
	selected := map[int64]audit.Selection{}
	var watermark int64
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var row audit.Selection
		retentionMust(t, json.Unmarshal(line, &row))
		selected[row.Source.ID] = row
		watermark = max(watermark, row.Source.ID)
	}
	families := []string{
		"landing_abandoned",
		"checks_observed",
		"checks_failure_confirmed",
		"check_rerun_requested",
		"check_rerun_dispatch_progress",
		"check_rerun_dispatched",
	}
	if len(families) != 6 {
		t.Fatal("historical retention assertion matrix incomplete")
	}
	for _, kind := range families {
		rows, e := db.Query(t.Context(), "SELECT id, payload FROM events WHERE type=$1 AND id<=$2 ORDER BY id", kind, watermark)
		retentionMust(t, e)
		count := 0
		for rows.Next() {
			var id int64
			var payload []byte
			retentionMust(t, rows.Scan(&id, &payload))
			row, ok := selected[id]
			if !ok || row.Source.Type != kind {
				rows.Close()
				t.Fatalf("historical family %s missing event %d", kind, id)
			}
			canonical, e := audit.Canonical(payload)
			retentionMust(t, e)
			if row.PayloadSHA256 != fmt.Sprintf("%x", sha256.Sum256(canonical)) {
				rows.Close()
				t.Fatalf("historical family %s payload changed: %d", kind, id)
			}
			count++
		}
		retentionMust(t, rows.Err())
		rows.Close()
		if count == 0 {
			t.Fatal("historical family not produced", kind)
		}
		want := map[string]int{"landing_abandoned": 1, "checks_failure_confirmed": 1, "check_rerun_requested": 2, "check_rerun_dispatch_progress": 4, "check_rerun_dispatched": 2}
		if n, exact := want[kind]; exact && count != n {
			t.Fatalf("historical family %s count=%d want=%d", kind, count, n)
		}
	}
	// Exhaustion and abandonment must be actual unsuccessful attempts, rather
	// than unrelated labels added to a happy-path journal.
	var exhausted, abandoned int
	retentionMust(t, db.QueryRow(t.Context(), `SELECT count(*) FROM events f JOIN events b ON b.queue_id=f.queue_id AND b.type='batch_started' AND b.payload->'data'->>'batch_id'=f.payload->'data'->>'batch_id' JOIN events d ON d.queue_id=f.queue_id AND d.type='dequeued' AND d.id>f.id WHERE f.type='checks_failure_confirmed' AND d.payload->'data'->>'reason'='checks-failed' AND EXISTS (SELECT 1 FROM jsonb_array_elements(b.payload->'data'->'prs') pr WHERE pr->>'pr'=d.payload->'data'->>'pr')`).Scan(&exhausted))
	retentionMust(t, db.QueryRow(t.Context(), `SELECT count(*) FROM events a JOIN events s ON s.queue_id=a.queue_id AND s.type='landing_started' AND s.payload->'data'->>'batch_id'=a.payload->'data'->>'batch_id' AND s.id<a.id WHERE a.type='landing_abandoned'`).Scan(&abandoned))
	if exhausted != 1 || abandoned != 1 {
		t.Fatalf("unsuccessful attempts: exhausted=%d abandoned=%d", exhausted, abandoned)
	}
	// Follow every retained reference, including references of dependencies.
	visited := map[int64]bool{}
	var visit func(int64)
	visit = func(id int64) {
		if visited[id] {
			return
		}
		row, ok := selected[id]
		if !ok {
			t.Fatalf("recursive closure missing source %d", id)
		}
		visited[id] = true
		for _, ref := range row.References {
			if ref.Source != nil {
				visit(ref.Source.ID)
			}
		}
	}
	for id, row := range selected {
		if row.Role == "scope" {
			visit(id)
		}
	}
	if len(visited) != len(selected) {
		t.Fatal("unreachable historical dependency")
	}
}

func TestRetentionCycleLogDerivation(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		completed := make(chan struct{}, 1)
		logger := slog.New(retentionCycleLog{slog.NewTextHandler(&bytes.Buffer{}, nil), completed}).With("queue", "retention")
		if grouped {
			logger = logger.WithGroup("worker").With("base", "main")
		}
		logger.Info("reconcile cycle finished")
		select {
		case <-completed:
		default:
			t.Fatal("derived handler lost cycle latch")
		}
	}
}
