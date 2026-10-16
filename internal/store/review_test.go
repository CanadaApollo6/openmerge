package store

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestStoredActionDoesNotDependOnHistoricalPlanner(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	// This wording and private key scope need not occur in today's planner.
	payload, err := os.ReadFile("../queue/testdata/wire/v1/action_report_status.json")
	must(t, err)
	expected, err := queue.DecodeAction(payload)
	must(t, err)
	// Keep the original key column authoritative, independently of Key changes.
	const storedKey = "historical/report/key"
	_, err = s.pool.Exec(t.Context(), `INSERT INTO actions(queue_id, key, type, payload, watermark)
        VALUES ($1, $2, 'report_status', $3::jsonb, 99)`, q, storedKey, string(payload))
	must(t, err)
	planned, err := s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	if len(planned) != 1 || planned[0].Key != storedKey || planned[0].Action != expected {
		t.Fatalf("historical payload changed: %+v", planned)
	}
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(claims) != 1 || claims[0].Key != storedKey || claims[0].Action != expected {
		t.Fatalf("claim replayed the planner: %+v", claims)
	}
}

func corruptBuildAction(t *testing.T, s *Store, q QueueID) {
	t.Helper()
	_, err := s.pool.Exec(t.Context(), `UPDATE actions
        SET payload = jsonb_set(payload, '{data,prs}', '"corrupt"'::jsonb)
        WHERE queue_id = $1 AND type = 'build_test_ref'`, q)
	must(t, err)
}

func TestPoisonIsolationAndOperatorListing(t *testing.T) {
	for _, detectAt := range []string{"claim", "plan", "begin"} {
		t.Run(detectAt, func(t *testing.T) {
			s := newStore(t)
			a, b := ensureQueue(t, s, "a"), ensureQueue(t, s, "b")
			appendEvents(t, s, a, seedEvents()...)
			_, err := s.Plan(t.Context(), a, config, epoch)
			must(t, err)
			var corruptClaim ClaimedAction
			if detectAt == "begin" {
				claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
				must(t, err)
				if len(claims) != 1 {
					t.Fatal("missing claim to corrupt")
				}
				corruptClaim = claims[0]
			}
			corruptBuildAction(t, s, a)
			// Queue B's later ID must still fill a limit-one claim in this call.
			appendEvents(t, s, b, seedEvents()...)
			_, err = s.Plan(t.Context(), b, config, epoch)
			must(t, err)
			switch detectAt {
			case "plan":
				planned, err := s.Plan(t.Context(), a, config, epoch)
				must(t, err)
				if len(planned) != 0 {
					t.Fatalf("returned poison: %+v", planned)
				}
			case "begin":
				proceed, err := s.BeginConsequential(t.Context(), corruptClaim, config, epoch)
				if proceed || err == nil {
					t.Fatal("corrupt claimed action proceeded")
				}
			}
			claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if len(claims) != 1 || claims[0].QueueID != b {
				t.Fatalf("poison blocked queue B: %+v", claims)
			}
			poison, err := s.Poisoned(t.Context())
			must(t, err)
			if len(poison) != 1 || poison[0].QueueID != a || poison[0].Key == "" || poison[0].ID == 0 ||
				poison[0].Watermark == 0 || !strings.Contains(poison[0].Error, "cannot unmarshal") || len(poison[0].Payload) == 0 {
				t.Fatalf("missing poison evidence: %+v", poison)
			}
			// Replanning repairs the poisoned action and preserves unrelated work.
			appendEvents(t, s, a, queue.PRObserved{PR: 2, HeadSHA: "denied"}, queue.EnqueueRequested{PR: 2, At: epoch})
			planned, err := s.Plan(t.Context(), a, config, epoch)
			must(t, err)
			if len(planned) != 2 {
				t.Fatalf("repair or unrelated work missing: %+v", planned)
			}
			if _, ok := planned[1].Action.(queue.ReportStatus); !ok {
				t.Fatalf("unexpected work: %+v", planned)
			}
			claims, err = s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			if len(claims) != 1 || claims[0].QueueID != a {
				t.Fatalf("poison blocked healthy work in same queue: %+v", claims)
			}
			if n := count(t, s, `SELECT count(*) FROM actions WHERE status = 'poisoned'`); n != 0 {
				t.Fatalf("poison was not repaired: %d", n)
			}
		})
	}
}

func TestBeginConsequentialRenewsOriginalLease(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	begin := epoch.Add(50 * time.Second)
	// Caller-supplied lease fields cannot shorten or lengthen the stored lease.
	claim.LeaseUntil = epoch.Add(24 * time.Hour)
	proceed, err := s.BeginConsequential(t.Context(), claim, config, begin)
	must(t, err)
	if !proceed {
		t.Fatal("valid landing did not proceed")
	}
	var renewed time.Time
	must(t, s.pool.QueryRow(t.Context(), `SELECT lease_until FROM actions WHERE queue_id = $1 AND key = $2`, q, claim.Key).Scan(&renewed))
	if !renewed.Equal(begin.Add(time.Minute).Truncate(time.Microsecond)) {
		t.Fatalf("renewed until %v", renewed)
	}
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch.Add(61*time.Second))
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("slow forge call was reclaimed within renewed lease")
	}
	must(t, s.Emit(t.Context(), claim, config)(queue.LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "slow", At: epoch.Add(61 * time.Second)}))
	claims, err = s.Claim(t.Context(), 1, time.Minute, renewed)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("renewed lease did not expire")
	}
}

func TestClaimUsesPartialIndexOverRetainedHistory(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	name, payload, err := queue.EncodeAction(queue.DeleteRef{Ref: "old"})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO actions(queue_id, key, type, payload, watermark, status)
        SELECT $1, 'history/' || n, $2, $3::jsonb, 0, 'done' FROM generate_series(1, 5000) n`, q, name, string(payload))
	must(t, err)
	appendEvents(t, s, q, seedEvents()...)
	_, err = s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `ANALYZE actions`)
	must(t, err)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, COSTS OFF) "+claimQuery, epoch, 1)
	must(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "Index Scan using actions_claimable") {
		t.Fatalf("claim did not use partial index:\n%s", plan)
	}
	t.Logf("claim plan:\n%s", plan)
}

func TestOpenUnreachableDatabaseReportsConnectionError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	address := listener.Addr().String()
	must(t, listener.Close())
	s, err := Open(t.Context(), "postgres://postgres@"+address+"/postgres?sslmode=disable&connect_timeout=1")
	if s != nil {
		s.Close()
		t.Fatal("unreachable database opened")
	}
	var connectionError *pgconn.ConnectError
	if !errors.As(err, &connectionError) || !strings.Contains(err.Error(), "connect database") || strings.Contains(err.Error(), "migrate database") {
		t.Fatalf("wrong error stage: %v", err)
	}
}

func TestPlanReplacesRecoverablePayloadButNeverReactivatesDone(t *testing.T) {
	for _, status := range []string{"cancelled", "poisoned", "done"} {
		t.Run(status, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			firstWatermark := appendEvents(t, s, q, seedEvents()...)
			first, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			if len(first) != 1 {
				t.Fatal("missing first plan")
			}
			_, err = s.pool.Exec(t.Context(), `UPDATE actions SET status = $2, error = 'invalid payload', poison_count = 1,
                payload = jsonb_set(payload, '{data,ref}', '"old-payload"'::jsonb) WHERE queue_id = $1`, q, status)
			must(t, err)
			newWatermark := appendEvents(t, s, q, queue.PRObserved{PR: 99, HeadSHA: "unrelated"})
			planned, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			var storedStatus, ref string
			var watermark int64
			must(t, s.pool.QueryRow(t.Context(), `SELECT status, watermark, payload #>> '{data,ref}' FROM actions WHERE queue_id = $1`, q).
				Scan(&storedStatus, &watermark, &ref))
			if status != "done" {
				if len(planned) != 1 || storedStatus != "planned" || watermark != newWatermark || ref != first[0].Action.(queue.BuildTestRef).Ref {
					t.Fatalf("%s action retained old inputs: %+v / %s at %d ref %s", status, planned, storedStatus, watermark, ref)
				}
			} else if len(planned) != 0 || storedStatus != "done" || watermark != firstWatermark || ref != "old-payload" {
				t.Fatal("done action was reactivated or overwritten")
			}
		})
	}
}
