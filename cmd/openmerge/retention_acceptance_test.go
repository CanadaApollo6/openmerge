package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
)

// Forge observations are fake; governing history and landings use private Git.
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

type retentionGit struct {
	*gitops.Repo
	f *fake.Forge
}

func (g retentionGit) BuildTestRef(ctx context.Context, r forge.RepoRef, b forge.BuildRequest, o forge.PushOptions) (string, error) {
	sha, err := g.Repo.BuildTestRef(ctx, r, b, o)
	if err == nil {
		g.f.SetCheckRuns(r, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success, HeadBranch: b.Ref}})
	}
	return sha, err
}

// The Python driver owns this database and leaves the journal for restore/ACL probes.
func TestRetentionLifecycle(t *testing.T) {
	dsn := os.Getenv("OPENMERGE_10B_DATABASE_URL")
	if dsn == "" {
		t.Skip("run scripts/check-adr0008-10b-1.py")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(dsn)
	must(err)
	if u.Scheme != "postgresql" || u.Host != "localhost:54329" || !regexp.MustCompile(`^/om_9c2c_[0-9a-f]{32}$`).MatchString(u.Path) || u.RawQuery != "sslmode=disable" {
		t.Fatal("unguarded scratch DSN")
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(e), "PG") {
			t.Fatal("PG routing override")
		}
	}
	out := filepath.Clean(os.Getenv("OPENMERGE_10B_OUT"))
	if !strings.HasPrefix(out, filepath.Join(os.Getenv("HOME"), ".cache")+"/") {
		t.Fatal("artifacts must be under ~/.cache")
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
	const source = "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues:\n- name: main\n  base: main\n  merge_method: merge\n  merge_when: [check-success = ci]\n  approvals: {default: {human: true, count: 1, receipt: true}}\n"
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
	// Establish genesis through normal GitHub-mode discovery before attesting adoption.
	bootstrapRepo := serve.Repository{Installation: "7", Name: r.Path, Branches: func(context.Context) ([]string, error) { return []string{"main"}, nil }, Target: func(base string) serve.Target {
		k := key
		k.BaseBranch = base
		return serve.Target{Key: k, Driver: d, Git: retentionGit{g, f}, AuthorityCompletionGit: completionGit, BotLogin: fake.Identity}
	}}
	bootstrap, err := serve.NewRepositories(t.Context(), db, []serve.Repository{bootstrapRepo}, serve.Options{PollInterval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	must(err)
	ctx, bootstrapCancel := context.WithCancel(t.Context())
	defer bootstrapCancel()
	bootstrapDone := make(chan error, 1)
	go func() { bootstrapDone <- bootstrap.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _, e := db.Load(t.Context(), id)
		must(e)
		if st.ConfigCommit == tip {
			break
		}
		if time.Now().After(deadline) {
			bootstrapCancel()
			must(<-bootstrapDone)
			t.Fatal("bootstrap configuration unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	bootstrapCancel()
	must(<-bootstrapDone)
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
	repo := serve.Repository{Installation: "7", Name: r.Path, Branches: func(context.Context) ([]string, error) { return []string{"main"}, nil }, Target: func(base string) serve.Target {
		k := key
		k.BaseBranch = base
		return serve.Target{Key: k, Driver: d, Git: retentionGit{g, f}, AuthorityCompletionGit: completionGit, BotLogin: fake.Identity}
	}}
	var s *serve.Server
	var cancel context.CancelFunc
	var done chan error
	start := func() {
		o := serveOptions{app: 5383059, authorityPlan: planPath, emissionReceipt: receiptPath, emissionReceiptMaxAge: time.Hour, poll: 10 * time.Millisecond}
		runtime, e := productionAuthorityRuntime(t.Context(), o, db)
		must(e)
		s, e = serve.NewRepositories(t.Context(), db, []serve.Repository{repo}, productionServeOptions(o, runtime, nil, slog.New(slog.NewTextHandler(os.Stderr, nil))))
		must(e)
		ctx, c := context.WithCancel(t.Context())
		cancel = c
		done = make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
	}
	stop := func() {
		if cancel != nil {
			cancel()
			must(<-done)
			cancel = nil
		}
	}
	defer stop()
	wait := func(label string, predicate func(queue.State) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			state, _, e := db.Load(t.Context(), id)
			must(e)
			if predicate(state) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		state, _, e := db.Load(t.Context(), id)
		must(e)
		var actions string
		must(admin.QueryRow(t.Context(), "SELECT coalesce(jsonb_agg(jsonb_build_object('type',type,'status',status,'error',error))::text,'[]') FROM actions").Scan(&actions))
		t.Fatalf("waiting for %s: config=%s entries=%d active=%v actions=%s", label, state.ConfigCommit, len(state.Entries), state.Active, actions)
	}
	inventory := func() string {
		var b string
		must(admin.QueryRow(t.Context(), `SELECT jsonb_build_object('queues',(SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM queues q),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e))::text`).Scan(&b))
		return b
	}
	var original map[string]json.RawMessage
	assertInventory := func() {
		var current map[string]json.RawMessage
		must(json.Unmarshal([]byte(inventory()), &current))
		for _, table := range []string{"queues", "events"} {
			var before, after []json.RawMessage
			must(json.Unmarshal(original[table], &before))
			must(json.Unmarshal(current[table], &after))
			if len(after) < len(before) {
				t.Fatal("lost rows", table)
			}
			for i := range before {
				if !bytes.Equal(before[i], after[i]) {
					t.Fatal("rewritten row", table, i)
				}
			}
		}
		original = current
	}
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
	// Read the production CLI brief, then submit rejected and accepted receipts.
	t.Setenv("OPENMERGE_API_TOKEN", "retention-read-token")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	approve := func(number int) {
		api := httptest.NewServer(status.Handler(s, []store.QueueKey{key}, "retention-read-token", nil))
		defer api.Close()
		changeID, e := key.ChangeID(number)
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
		native, e := f.NativeReviews(t.Context(), r, number)
		must(e)
		for i, digest := range []string{strings.Repeat("0", 64), view.Digest} {
			native.Comments = append(native.Comments, forge.NativeComment{ID: int64(400 + number*10 + i), Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, Body: "@openmerge approve " + digest, Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}})
		}
		f.SetNativeReviews(r, number, native)
		must(s.Notify(t.Context(), "7", r.Path, fmt.Sprintf("receipts-%d", number)))
	}
	start()
	wait("initial openmerge adoption", func(st queue.State) bool {
		return st.AuthorityPin() != nil && st.AuthorityPin().Authority == authority.OpenMerge
	})
	must(s.RefreshBranches(t.Context()))
	windowFrom = time.Now().UTC().Format(time.RFC3339Nano)
	f.AddComment(r, 1, "contributor", "@openmerge queue")
	f.SetNativeReviews(r, 1, forge.NativeReviews{Comments: []forge.NativeComment{{ID: 101, Kind: forge.IssueComment, Author: forge.UserIdentity{ID: 12, Login: "contributor", Kind: forge.UserAccount}, Body: "@openmerge queue", Created: forge.NativeTime{Time: time.Now().UTC(), Source: forge.CommentCreationTime, Precision: time.Second}}}, Reviews: []forge.NativeReview{{ID: 201, CommitID: head, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC(), Source: forge.ReviewSubmissionTime}}}})

	approve(1)
	wait("settled decision", func(_ queue.State) bool {
		var n int
		must(admin.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE type='landed'").Scan(&n))
		return n > 0
	})
	stop()
	must(json.Unmarshal([]byte(inventory()), &original))
	must(os.WriteFile(filepath.Join(out, "inventory-before.json"), []byte(inventory()), 0600))
	landedSHA := bare("rev-parse", "refs/heads/main")
	landings := map[string]string{landedSHA: tip + " " + head}
	before := export("before", "")
	selection, err := os.ReadFile(filepath.Join(before, "selection.jsonl"))
	must(err)
	if !bytes.Contains(selection, []byte(`"role":"dependency"`)) {
		t.Fatal("fixture must retain pre-window dependency closure")
	}

	manifest := filepath.Join(before, "manifest.json")
	for i, mode := range []string{"github", "openmerge"} {
		next := strings.Replace(source, "policy_authority: openmerge", "policy_authority: "+mode, 1)
		if mode == "github" {
			next = strings.Replace(next, "break_glass: {team: acme/emergency}\n", "", 1)
			next = strings.Replace(next, ", receipt: true", "", 1)
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
			// A second real landing records GitHub-governed historical semantics.
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
		db.Close()
		db, err = store.Open(t.Context(), dsn)
		must(err)
	}
	// Retain main, but remove its selector from valid default-branch config.
	tip = commit("version: 1\nqueues: [{name: other, base: release, merge_when: [check-success = ci]}]\n", tip, "changed")
	bare("update-ref", "refs/heads/main", tip)
	start()
	must(s.RefreshBranches(t.Context()))
	wait("retired", func(st queue.State) bool { return st.LifecycleState() == "retired" })
	stop()
	assertInventory()
	equal(before, export("retired-replay", manifest))
	db.Close()
	db, err = store.Open(t.Context(), dsn)
	must(err)
	start()
	wait("retired after reopen", func(st queue.State) bool { return st.LifecycleState() == "retired" })
	stop()
	assertInventory()
	equal(before, export("restart-replay", manifest))
	var invalidPairs int
	must(admin.QueryRow(t.Context(), `SELECT count(*) FROM events p LEFT JOIN events c ON c.queue_id=p.queue_id AND c.id=(SELECT max(id) FROM events WHERE queue_id=p.queue_id AND id<p.id) WHERE p.type='policy_authority_applied' AND (c.type IS DISTINCT FROM 'config_applied' OR c.payload->'data'->>'commit' IS DISTINCT FROM p.payload->'data'->>'governing_commit' OR c.payload->'data'->>'digest' IS DISTINCT FROM p.payload->'data'->>'effective_digest')`).Scan(&invalidPairs))
	if invalidPairs != 0 {
		t.Fatal("nonadjacent authority/config pair")
	}

	final := export("final", "")
	must(os.WriteFile(filepath.Join(out, "inventory-final.json"), []byte(inventory()), 0600))
	writeJSON(filepath.Join(out, "git-oracle.json"), map[string]any{"landings": landings, "parents": parents})
	t.Logf("retention inventory sha256=%x final=%s", sha256.Sum256([]byte(inventory())), final)
}
