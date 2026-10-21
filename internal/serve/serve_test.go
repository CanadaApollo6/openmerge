package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

const repositoryConfig = "version: 1\nqueues:\n  - name: default\n    merge_when:\n      - check-success = ci\n"

var repo = forge.RepoRef{Path: "owner/repo"}

type countingDriver struct {
	forge.Driver
	pulls, comments, checks, permissions int
}

func (d *countingDriver) LinearHistoryRequired(ctx context.Context, r forge.RepoRef, base string) (bool, error) {
	return d.Driver.(forge.LinearHistoryReader).LinearHistoryRequired(ctx, r, base)
}

func (d *countingDriver) PullRequest(ctx context.Context, r forge.RepoRef, n int) (forge.PullRequest, error) {
	d.pulls++
	return d.Driver.PullRequest(ctx, r, n)
}
func (d *countingDriver) ListComments(ctx context.Context, r forge.RepoRef, n int, since time.Time) ([]forge.Comment, error) {
	d.comments++
	return d.Driver.ListComments(ctx, r, n, since)
}
func (d *countingDriver) CheckRuns(ctx context.Context, r forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	d.checks++
	return d.Driver.CheckRuns(ctx, r, sha)
}
func (d *countingDriver) CanWrite(ctx context.Context, r forge.RepoRef, user string) (bool, error) {
	d.permissions++
	return d.Driver.CanWrite(ctx, r, user)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func database(t *testing.T) *store.Store {
	t.Helper()
	db, _ := databaseConnection(t)
	return db
}

func databaseConnection(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("run make db and set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	must(t, err)
	var token [12]byte
	_, err = rand.Read(token[:])
	must(t, err)
	schema := "serve_unit_" + hex.EncodeToString(token[:])
	_, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	must(t, err)
	t.Cleanup(func() {
		_, err := conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		must(t, err)
		must(t, conn.Close(context.Background()))
	})
	u, err := url.Parse(raw)
	must(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := store.Open(t.Context(), u.String(), store.WithTestLockScope(schema))
	must(t, err)
	t.Cleanup(db.Close)
	_, err = conn.Exec(t.Context(), "SET search_path TO "+pgx.Identifier{schema}.Sanitize())
	must(t, err)
	return db, conn
}
func fixture(t *testing.T, contents string) (*Server, *worker, *fake.Forge, *countingDriver) {
	t.Helper()
	db := database(t)
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	files := map[string]string{}
	if contents != "" {
		files[".openmerge.yml"] = contents
	}
	base, err := f.PushBase(repo, "main", "configuration", files)
	must(t, err)
	_, err = f.OpenPR(repo, 1, base, true)
	must(t, err)
	f.SetCanWrite(repo, "writer", true)
	d := &countingDriver{Driver: f}
	s, err := New(t.Context(), db, []Target{{Key: store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}, Driver: d, Git: f, BotLogin: fake.Identity}}, Options{PollInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	return s, s.queues[0], f, d
}

func TestUnchangedPollAndRestartDedupe(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	f.AddComment(repo, 1, "reader", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pulls, comments := d.pulls, d.comments
	must(t, s.cycle(t.Context(), w))
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if before != after || pulls != d.pulls || comments != d.comments {
		t.Fatalf("unchanged poll appended or fetched details: events %d -> %d, reads %d/%d -> %d/%d", before, after, pulls, comments, d.pulls, d.comments)
	}
	// Reconstructing the service forces a full observation, while persisted
	// receipts and inclusive cursors prevent another command or refusal.
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	must(t, restarted.cycle(t.Context(), restarted.queues[0]))
	if len(f.Comments(repo, 1)) != 1 {
		t.Fatal("unauthorized reply duplicated after restart")
	}
	if d.permissions != 1 {
		t.Fatal("already processed command reauthorized")
	}
}

func TestMissingInvalidAndReloadedConfig(t *testing.T) {
	for _, contents := range []string{"", "version: broken", "version: 1\nqueues:\n  - name: default\n    enter_when: [label = ready]\n"} {
		t.Run(contents, func(t *testing.T) {
			s, w, f, _ := fixture(t, contents)
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.PauseReason(queue.Config{}) != "invalid-config" || state.Active != nil {
				t.Fatal("invalid config did not pause")
			}
			for _, status := range f.Statuses(repo, 1) {
				if status.State != forge.Paused || status.Summary == "" {
					t.Fatalf("invisible configuration failure: %+v", status)
				}
			}
			_, err = f.PushBase(repo, "main", "fix config", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			must(t, s.cycle(t.Context(), w))
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.PauseReason(queue.Config{}) != "" {
				t.Fatal("valid base config did not resume")
			}
		})
	}
}

func TestChecksRefreshDespiteUnchangedPRAndAmbiguityRecovers(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing {
		t.Fatalf("not testing: %+v", state)
	}
	sha := state.Active.TestedSHA
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: state.Active.TestRef}, {ID: 2, Name: "ci", Source: "app:2", State: forge.Success, HeadBranch: state.Active.TestRef}})
	pulls, comments, checks := d.pulls, d.comments, d.checks
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	if d.pulls != pulls || d.comments != comments || d.checks <= checks {
		t.Fatal("unchanged PR skipped CI or reloaded details")
	}
	statuses := f.Statuses(repo, 1)
	for _, status := range statuses {
		if status.Reason != "ambiguous-check-sources" {
			t.Fatalf("ambiguity was hidden: %+v", status)
		}
	}
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 3, Name: "ci", Source: "app:1", State: forge.Success, HeadBranch: state.Active.TestRef}})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BaseSHA != sha || state.Active != nil {
		t.Fatal("corrected check producers did not land")
	}
}

func TestRevocationAtLandingCancelsStaleWork(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	_, err = f.OpenPR(repo, 2, base, false)
	must(t, err)
	pr, err := f.PullRequest(t.Context(), repo, 2)
	must(t, err)
	must(t, f.Approve(repo, 2, forge.Approval{User: "writer", SHA: pr.HeadSHA, CanWrite: true}))
	f.AddComment(repo, 2, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	sha := state.Active.TestedSHA
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	// Revoking write does not change the PR summary; only the landing gate reads it.
	f.SetCanWrite(repo, "writer", false)
	pulls := d.pulls
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BaseSHA == sha || len(state.Entries) != 0 || d.pulls <= pulls {
		t.Fatalf("revoked fork was not dequeued: %+v", state)
	}
}

type missingLanding struct{ forge.Git }

func (g missingLanding) Land(context.Context, forge.RepoRef, forge.LandRequest, forge.PushOptions) (string, error) {
	return "", &executor.ErrLandingNotOnBase{TestedSHA: "tested", BaseTip: "rolled-back", AttemptID: "attempt"}
}

func TestLandingNotOnBasePausesAndStillResolves(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	w.Git = missingLanding{w.Git}
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	err := s.cycle(t.Context(), w)
	var absent *executor.ErrLandingNotOnBase
	if !errors.As(err, &absent) {
		t.Fatalf("landing error=%v", err)
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "landing-not-on-base" || state.Active.Phase != queue.Landing {
		t.Fatalf("unsafe landing not paused: %+v", state)
	}
	// Advance the timer without waiting; resolution remains in the paused plan.
	actions, err := s.store.Plan(t.Context(), w.id, w.plannerConfig(time.Millisecond), time.Now().Add(2*time.Minute))
	must(t, err)
	resolving := false
	for _, action := range actions {
		if _, ok := action.Action.(queue.ResolveLanding); ok {
			resolving = true
		}
	}
	if !resolving {
		t.Fatal("pause suppressed landing resolution")
	}
}

type cancellableChecks struct {
	forge.Driver
	entered chan struct{}
}

func (d cancellableChecks) CheckRuns(ctx context.Context, _ forge.RepoRef, _ string) ([]forge.CheckRun, error) {
	close(d.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShutdownReleasesClaim(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	entered := make(chan struct{})
	w.Driver = cancellableChecks{Driver: w.Driver, entered: entered}
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reach CI read")
	}
	cancel()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not drain")
	}
	claims, err := s.store.ClaimQueue(t.Context(), w.id, 1, time.Minute, time.Now())
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("cancelled worker retained its claim")
	}
}

func TestQuotaBackoffStopsReconciliation(t *testing.T) {
	s, _, _, d := fixture(t, repositoryConfig)
	s.options.BackoffUntil = func() time.Time { return time.Now().Add(time.Minute) }
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	must(t, s.Run(ctx))
	if d.pulls != 0 || d.checks != 0 {
		t.Fatal("poll ignored quota cooldown")
	}
}

func TestWebhookTriggersEarlyCIReadAndIsDurable(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	s.options.PollInterval = time.Hour
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	checks := d.checks
	must(t, s.Notify(t.Context(), "7", repo.Path, "delivery-1"))
	must(t, s.cycle(t.Context(), w))
	if d.checks <= checks {
		t.Fatal("webhook did not refresh active SHA before polling deadline")
	}
	checks = d.checks
	must(t, s.Notify(t.Context(), "7", repo.Path, "delivery-1"))
	must(t, s.cycle(t.Context(), w))
	if d.checks != checks {
		t.Fatal("duplicate delivery refreshed CI")
	}
}

type rollbackBeforeClosure struct {
	*fake.Forge
	base   string
	landed bool
	reads  int
}

func (g *rollbackBeforeClosure) Land(ctx context.Context, r forge.RepoRef, request forge.LandRequest, opts forge.PushOptions) (string, error) {
	next, err := g.Forge.Land(ctx, r, request, opts)
	if err == nil {
		g.landed = true
	}
	return next, err
}
func (g *rollbackBeforeClosure) ReadRef(ctx context.Context, r forge.RepoRef, ref string) (string, error) {
	if g.landed && ref == "main" {
		g.reads++
		if g.reads == 2 {
			if err := g.SetRef(r, ref, g.base); err != nil {
				return "", err
			}
		}
	}
	return g.Forge.ReadRef(ctx, r, ref)
}
func TestRollbackBeforeMarkLandedRemainsVisible(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	w.Git = &rollbackBeforeClosure{Forge: f, base: base}
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	err = s.cycle(t.Context(), w)
	var absent *executor.ErrLandingNotOnBase
	if !errors.As(err, &absent) {
		t.Fatalf("closure error=%v", err)
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 0 {
		t.Fatal("test did not reach post-landing closure")
	}
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	if pr.Closed {
		t.Fatal("rolled-back PR was closed")
	}
	for _, status := range f.Statuses(repo, 1) {
		if status.State != forge.Paused || status.Reason != "landing-not-on-base" {
			t.Fatalf("post-landing pause hidden: %+v", status)
		}
	}
	landed := state.BaseSHA
	// The base observation still contains the pre-landing tip. Reconciliation
	// must nevertheless notice that the projected base was rolled back.
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.BaseSHA != base {
		t.Fatalf("rollback vanished: projected base=%s, actual=%s", state.BaseSHA, base)
	}
	// Repeat paused cycles to exercise the already durable MarkLanded claim.
	must(t, s.cycle(t.Context(), w))
	must(t, f.SetRef(repo, "main", landed))
	f.AddComment(repo, 1, "writer", "@openmerge refresh")
	must(t, s.cycle(t.Context(), w))
	pr, err = f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	if !pr.Closed {
		t.Fatalf("restored landing did not close/mark PR: %+v", pr)
	}
	marked := false
	for _, body := range f.Comments(repo, 1) {
		marked = marked || strings.Contains(body, "Landed in ["+landed+"]")
	}
	if !marked {
		t.Fatal("restored landing omitted landed marker")
	}
}

var _ forge.Git = (*missingLanding)(nil)

var _ forge.Git = (*rollbackBeforeClosure)(nil)
