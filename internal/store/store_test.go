package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// Every database test gets a schema, even when several processes share the URL.
func databaseURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("Postgres integration test skipped: OPENMERGE_TEST_DATABASE_URL is unset; run make db and export the printed URL")
	}
	u, err := url.Parse(raw)
	must(t, err)
	conn, err := pgx.Connect(t.Context(), raw)
	must(t, err)
	var token [12]byte
	_, err = rand.Read(token[:])
	must(t, err)
	schema := "om_test_" + hex.EncodeToString(token[:])
	_, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	must(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := conn.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		if err := conn.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	params := u.Query()
	params.Set("search_path", schema)
	params.Set("application_name", schema)
	u.RawQuery = params.Encode()
	return u.String()
}

func testLockScope(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	must(t, err)
	return u.Query().Get("search_path")
}

func openTestStore(t *testing.T, url string) *Store {
	t.Helper()
	s, err := Open(t.Context(), url, WithTestLockScope(testLockScope(t, url)))
	must(t, err)
	t.Cleanup(s.Close)
	return s
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t, databaseURL(t))
}

func ensureQueue(t *testing.T, s *Store, lane string) QueueID {
	t.Helper()
	id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main", Lane: lane})
	must(t, err)
	return id
}

func appendEvents(t *testing.T, s *Store, q QueueID, events ...queue.Event) int64 {
	t.Helper()
	watermark, err := s.Append(t.Context(), q, events, "")
	must(t, err)
	return watermark
}

func count(t *testing.T, s *Store, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, s.pool.QueryRow(t.Context(), sql, args...).Scan(&n))
	return n
}

func TestMigrationsConcurrentOpenAndReopen(t *testing.T) {
	url := databaseURL(t)
	scope := testLockScope(t, url)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			s, err := Open(t.Context(), url, WithTestLockScope(scope))
			if err == nil {
				s.Close()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	s := openTestStore(t, url)
	if n := count(t, s, `SELECT count(*) FROM schema_migrations`); n != 6 {
		t.Fatalf("migration count = %d, want 6", n)
	}
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	other := openTestStore(t, url)
	if got := ensureQueue(t, other, ""); got != q {
		t.Fatalf("queue id changed after reopen: %d / %d", q, got)
	}
	if n := count(t, other, `SELECT count(*) FROM events`); n != 3 {
		t.Fatalf("reopen changed event log: %d", n)
	}
	if other.pool.Config().ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatal("store requires session-scoped prepared statements")
	}
}

func TestEnsureQueueIdentityAndFenceBootstrap(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	if again := ensureQueue(t, s, ""); again != q {
		t.Fatal("queue identity changed")
	}
	if other := ensureQueue(t, s, "other"); other == q {
		t.Fatal("distinct lane shared an identity")
	}
	if _, err := s.EnsureQueue(t.Context(), QueueKey{}); err == nil {
		t.Fatal("accepted an empty identity")
	}
	key := QueueKey{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main"}
	var stored QueueKey
	must(t, s.pool.QueryRow(t.Context(), `SELECT installation, repository, base, lane FROM queues WHERE id = $1`, q).
		Scan(&stored.Installation, &stored.Repository, &stored.BaseBranch, &stored.Lane))
	if stored != key {
		t.Fatalf("identity columns = %+v, want %+v", stored, key)
	}
	for _, different := range []QueueKey{
		{Installation: "installation/2", Repository: key.Repository, BaseBranch: key.BaseBranch},
		{Installation: key.Installation, Repository: "another/repo", BaseBranch: key.BaseBranch},
		{Installation: key.Installation, Repository: key.Repository, BaseBranch: "release/1"},
	} {
		id, err := s.EnsureQueue(t.Context(), different)
		must(t, err)
		if id == q {
			t.Fatalf("different identity reused %d: %+v", q, different)
		}
		again, err := s.EnsureQueue(t.Context(), different)
		must(t, err)
		if again != id {
			t.Fatalf("unstable identity: %+v", different)
		}
	}
	must(t, s.InitializeFence(t.Context(), q, "initial"))
	appendEvents(t, s, q, seedEvents()...)
	must(t, s.InitializeFence(t.Context(), q, "initial"))
	if err := s.InitializeFence(t.Context(), q, "replacement"); err == nil {
		t.Fatal("allowed bootstrap rewrite")
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.BaseBranch != "main" || state.Fence != "initial" {
		t.Fatalf("bootstrap missing: %+v", state)
	}
	q2 := ensureQueue(t, s, "no-bootstrap")
	appendEvents(t, s, q2, queue.BaseMoved{SHA: "base"})
	if err := s.InitializeFence(t.Context(), q2, "late"); err == nil {
		t.Fatal("allowed a bootstrap after the log started")
	}
}

func TestAppendDeduplicatesWholeDeliveryConcurrently(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	type result struct {
		watermark int64
		err       error
	}
	const workers = 8
	results := make(chan result, workers)
	for range workers {
		go func() {
			watermark, err := s.Append(t.Context(), q, seedEvents(), "delivery-1")
			results <- result{watermark, err}
		}()
	}
	var want int64
	for range workers {
		result := <-results
		must(t, result.err)
		if want == 0 {
			want = result.watermark
		}
		if result.watermark != want {
			t.Fatalf("duplicate returned watermark %d, want %d", result.watermark, want)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q); n != len(seedEvents()) {
		t.Fatalf("deduplication left %d events", n)
	}
	var last int64
	must(t, s.pool.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE queue_id = $1`, q).Scan(&last))
	if want != last {
		t.Fatalf("delivery returned %d, want final watermark %d", want, last)
	}
	// An ID from another queue must not suppress this queue's delivery.
	q2 := ensureQueue(t, s, "other")
	_, err := s.Append(t.Context(), q2, seedEvents(), "delivery-1")
	must(t, err)
	// Empty keys disable dedupe, and a partially encoded delivery rolls back.
	appendEvents(t, s, q, queue.ResumeRequested{})
	appendEvents(t, s, q, queue.ResumeRequested{})
	if _, err := s.Append(t.Context(), q, []queue.Event{queue.ResumeRequested{}, nil}, "bad"); err == nil {
		t.Fatal("accepted an invalid event")
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q); n != 5 {
		t.Fatalf("failed append leaked part of a delivery: %d", n)
	}
}

func TestLoadEqualsInMemoryFold(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	must(t, s.InitializeFence(t.Context(), q, "fence"))
	events := append(seedEvents(), eventSamples()...)
	want := fold(queue.State{BaseBranch: "main", Fence: "fence"}, events)
	watermark := appendEvents(t, s, q, events...)
	got, loaded, err := s.Load(t.Context(), q)
	must(t, err)
	if !reflect.DeepEqual(got, want) || loaded != watermark {
		t.Fatalf("replay differs: got %+v at %d, want %+v at %d", got, loaded, want, watermark)
	}
	if _, _, err := s.Load(t.Context(), q+100); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing queue: %v", err)
	}
}

// Each contender signals from the lock boundary, before issuing the blocking
// query. The transaction holder controls release; no server statistics are polled.
func queueLockContenders(t *testing.T, s *Store, want int) func() {
	t.Helper()
	arrived := make(chan struct{}, want)
	s.beforeQueueLock = func() { arrived <- struct{}{} }
	return func() {
		t.Helper()
		for range want {
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("contender did not reach queue lock")
			}
		}
	}
}

func TestPlanSerializesAndIsIdempotent(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	// The trigger asserts the planning lock is still held when actions are
	// inserted, rather than merely protecting an earlier read of the log.
	_, err := s.pool.Exec(t.Context(), `CREATE FUNCTION require_planning_lock() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
	    IF NOT EXISTS (SELECT FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory' AND granted) THEN
	        RAISE EXCEPTION 'planning insert escaped its advisory lock';
	    END IF;
	    RETURN NEW;
	END $$;
	CREATE TRIGGER planning_lock BEFORE INSERT ON actions FOR EACH ROW EXECUTE FUNCTION require_planning_lock()`)
	must(t, err)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, q))
	type result struct {
		actions []PlannedAction
		err     error
	}
	results := make(chan result, 2)
	waitForContenders := queueLockContenders(t, s, 2)
	for range 2 {
		go func() {
			actions, err := s.Plan(t.Context(), q, config, epoch)
			results <- result{actions, err}
		}()
	}
	waitForContenders()
	if n := count(t, s, `SELECT count(*) FROM actions`); n != 0 {
		t.Fatal("a planner interleaved with the queue-lock holder")
	}
	name, payload, err := queue.EncodeEvent(queue.ResumeRequested{})
	must(t, err)
	var watermark int64
	must(t, tx.QueryRow(t.Context(), `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb) RETURNING id`, q, name, string(payload)).Scan(&watermark))
	must(t, tx.Commit(t.Context()))
	first, second := <-results, <-results
	s.beforeQueueLock = nil
	must(t, first.err)
	must(t, second.err)
	if !reflect.DeepEqual(first.actions, second.actions) || len(first.actions) != 1 || first.actions[0].Watermark != watermark {
		t.Fatalf("planners did not agree on committed state: %+v / %+v", first.actions, second.actions)
	}
	third, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if !reflect.DeepEqual(first.actions, third) || count(t, s, `SELECT count(*) FROM actions`) != 1 {
		t.Fatal("replanning duplicated or changed an action")
	}
}

func TestAppendCannotCommitBehindAPlannedWatermark(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	must(t, s.lockQueue(t.Context(), tx, q))
	name, payload, err := queue.EncodeEvent(queue.ResumeRequested{})
	must(t, err)
	var earlier int64
	must(t, tx.QueryRow(t.Context(), `INSERT INTO events(queue_id, type, payload) VALUES ($1, $2, $3::jsonb) RETURNING id`, q, name, string(payload)).Scan(&earlier))
	appended := make(chan error, 1)
	waitForContender := queueLockContenders(t, s, 1)
	go func() {
		_, err := s.Append(t.Context(), q, []queue.Event{queue.ResumeRequested{}}, "later")
		appended <- err
	}()
	waitForContender()
	if n := count(t, s, `SELECT count(*) FROM events`); n != 3 {
		t.Fatal("append committed while an earlier event was uncommitted")
	}
	must(t, tx.Commit(t.Context()))
	must(t, <-appended)
	s.beforeQueueLock = nil
	actions, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(actions) != 1 || actions[0].Watermark <= earlier {
		t.Fatalf("plan did not include both commits: %+v", actions)
	}
	if n := count(t, s, `SELECT count(*) FROM events`); n != 5 {
		t.Fatalf("event count = %d, want 5", n)
	}
}

func TestClaimSkipLockedAndLeaseRecovery(t *testing.T) {
	s := newStore(t)
	q1, q2 := ensureQueue(t, s, "first"), ensureQueue(t, s, "second")
	for _, q := range []QueueID{q1, q2} {
		appendEvents(t, s, q, seedEvents()...)
		_, err := s.Plan(t.Context(), q, config, epoch)
		must(t, err)
	}
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(t.Context(), `SELECT id FROM actions WHERE queue_id = $1 FOR UPDATE`, q1)
	must(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	claims, err := s.Claim(ctx, 2, time.Minute, epoch)
	must(t, err)
	if len(claims) != 1 || claims[0].QueueID != q2 {
		t.Fatalf("did not skip locked queue: %+v", claims)
	}
	old := claims[0]
	claims, err = s.Claim(ctx, 2, time.Minute, epoch.Add(30*time.Second))
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("claimed a locked row or unexpired lease")
	}
	must(t, tx.Commit(t.Context()))
	claims, err = s.Claim(t.Context(), 2, time.Minute, epoch.Add(30*time.Second))
	must(t, err)
	if len(claims) != 1 || claims[0].QueueID != q1 {
		t.Fatalf("claim after unlock: %+v", claims)
	}
	must(t, s.Complete(t.Context(), claims[0], Done))
	claims, err = s.Claim(t.Context(), 2, time.Minute, epoch.Add(time.Minute))
	must(t, err)
	if len(claims) != 1 || claims[0].QueueID != q2 || claims[0].AttemptID == old.AttemptID {
		t.Fatalf("expired lease was not replaced: %+v", claims)
	}
	if err := s.Complete(t.Context(), old, Done); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale completion took over the new claim: %v", err)
	}
	if _, err := s.BeginConsequential(t.Context(), old, config, epoch.Add(time.Minute)); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale worker began execution: %v", err)
	}
	proceed, err := s.BeginConsequential(t.Context(), claims[0], config, epoch.Add(time.Minute))
	must(t, err)
	if !proceed {
		t.Fatal("valid claim did not proceed")
	}
	claims, err = s.Claim(t.Context(), 1, time.Minute, epoch.Add(2*time.Minute))
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("in-flight lease was not recoverable")
	}
	proceed, err = s.BeginConsequential(t.Context(), claims[0], config, epoch.Add(2*time.Minute))
	must(t, err)
	if !proceed {
		t.Fatal("recovered in-flight action could not proceed")
	}
	must(t, s.Complete(t.Context(), claims[0], Failed))
	claims, err = s.Claim(t.Context(), 1, time.Minute, epoch.Add(2*time.Minute))
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("failed action was not retryable")
	}
	must(t, s.Complete(t.Context(), claims[0], Done))
	if n := count(t, s, `SELECT count(*) FROM actions WHERE status = 'done'`); n != 2 {
		t.Fatalf("done count = %d", n)
	}
	// Finished keys remain idempotent even if an observation has not advanced.
	_, err = s.Plan(t.Context(), q2, config, epoch)
	must(t, err)
	claims, err = s.Claim(t.Context(), 1, time.Minute, epoch.Add(3*time.Minute))
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("replanned a completed key")
	}
}

func readyLand(t *testing.T, s *Store) (QueueID, ClaimedAction) {
	t.Helper()
	q := ensureQueue(t, s, "")
	must(t, s.InitializeFence(t.Context(), q, "fence"))
	appendEvents(t, s, q, append(seedEvents(),
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Squash, At: epoch},
		queue.ChecksCompleted{TestedSHA: "tested", Success: true})...)
	_, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("land claims = %+v", claims)
	}
	if _, ok := claims[0].Action.(queue.Land); !ok {
		t.Fatalf("expected Land, got %T", claims[0].Action)
	}
	return q, claims[0]
}

func TestBeginConsequentialRevalidatesStaleLand(t *testing.T) {
	for _, change := range []string{"none", "unrelated", "dequeue"} {
		t.Run(change, func(t *testing.T) {
			s := newStore(t)
			q, claim := readyLand(t, s)
			switch change {
			case "unrelated":
				appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "other", Eligible: true, Trusted: true})
			case "dequeue":
				appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
			}
			// A caller cannot bypass staleness with a fabricated watermark.
			claim.Watermark = 1<<63 - 1
			proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
			must(t, err)
			if proceed != (change != "dequeue") {
				t.Fatalf("proceed = %v after %s", proceed, change)
			}
			want := "in_flight"
			if change == "dequeue" {
				want = "cancelled"
			}
			var status string
			must(t, s.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE queue_id = $1 AND key = $2`, q, claim.Key).Scan(&status))
			if status != want {
				t.Fatalf("durable status = %s, want %s", status, want)
			}
		})
	}
}

func TestLandingStartedStopsReexecutingLand(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("valid landing did not proceed")
	}
	emit := s.Emit(t.Context(), claim, config)
	must(t, emit(queue.LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "first", At: epoch}))
	if err := emit(queue.LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "late-copy", At: epoch}); !errors.Is(err, ErrStale) {
		t.Fatalf("a second executor could start another landing: %v", err)
	}
	if err := s.Complete(t.Context(), claim, Failed); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale landing retained its claim: %v", err)
	}
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("cancelled landing remained claimable")
	}
	actions, err := s.Plan(t.Context(), q, config, epoch.Add(2*time.Minute))
	must(t, err)
	if len(actions) != 1 {
		t.Fatalf("resolution plan = %+v", actions)
	}
	if _, ok := actions[0].Action.(queue.ResolveLanding); !ok {
		t.Fatalf("expected ResolveLanding, got %T", actions[0].Action)
	}
}

func TestClaimsPartitionConcurrentWorkers(t *testing.T) {
	s := newStore(t)
	const workers = 8
	for i := range workers {
		q := ensureQueue(t, s, fmt.Sprint(i))
		appendEvents(t, s, q, seedEvents()...)
		_, err := s.Plan(t.Context(), q, config, epoch)
		must(t, err)
	}
	type result struct {
		claims []ClaimedAction
		err    error
	}
	results := make(chan result, workers)
	for range workers {
		go func() {
			claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			results <- result{claims, err}
		}()
	}
	seen := make(map[QueueID]bool)
	for range workers {
		result := <-results
		must(t, result.err)
		if len(result.claims) != 1 || seen[result.claims[0].QueueID] {
			t.Fatalf("claim was lost or shared: %+v", result.claims)
		}
		seen[result.claims[0].QueueID] = true
	}
}

func TestInFlightLandDefersConflictingDequeue(t *testing.T) {
	s := newStore(t)
	q, land := readyLand(t, s)
	proceed, err := s.BeginConsequential(t.Context(), land, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("landing did not acquire its execution window")
	}
	appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
	_, err = s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 10, 2*time.Minute, epoch)
	must(t, err)
	var dequeue ClaimedAction
	for _, claim := range claims {
		if _, ok := claim.Action.(queue.Dequeue); ok {
			dequeue = claim
		}
	}
	if dequeue.Key == "" {
		t.Fatal("dequeue was not planned")
	}
	proceed, err = s.BeginConsequential(t.Context(), dequeue, config, epoch)
	if proceed || !errors.Is(err, ErrQueueBusy) {
		t.Fatalf("dequeue raced a live landing: proceed=%v err=%v", proceed, err)
	}
	// If the first worker never recorded LandingStarted, lease expiry permits
	// the dequeue. Its in-flight record now rejects the old worker's late emit.
	proceed, err = s.BeginConsequential(t.Context(), dequeue, config, epoch.Add(time.Minute))
	must(t, err)
	if !proceed {
		t.Fatal("dequeue could not recover an expired execution window")
	}
	err = s.Emit(t.Context(), land, config)(queue.LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "late", At: epoch.Add(time.Minute)})
	if !errors.Is(err, ErrClaimLost) {
		t.Fatalf("paused worker started a landing after losing its window: %v", err)
	}
}

func TestResolveLandingCanRecoverUncompletedInFlightLand(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("landing did not proceed")
	}
	must(t, s.Emit(t.Context(), claim, config)(queue.LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "attempt", At: epoch}))
	now := epoch.Add(2 * time.Minute)
	_, err = s.Plan(t.Context(), q, config, now)
	must(t, err)
	claims, err := s.Claim(t.Context(), 10, time.Minute, now)
	must(t, err)
	resolved := false
	for _, claim := range claims {
		proceed, err := s.BeginConsequential(t.Context(), claim, config, now)
		must(t, err)
		switch claim.Action.(type) {
		case queue.Land:
			if proceed {
				t.Fatal("recovered lease re-executed original Land")
			}
		case queue.ResolveLanding:
			if !proceed {
				t.Fatal("resolution could not proceed")
			}
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("missing resolution action")
	}
}
