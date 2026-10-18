package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Exercise the exact read-back SQL on a populated, isolated journal, not a
// simplified query that could accidentally have different index predicates.
func TestLandingEvidenceIdentityIndexes(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	ddl, err := migrations.ReadFile("migrations/006_evidence_type.sql")
	must(t, err)
	for _, n := range []int{2600, 260000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), `DROP INDEX events_landing_decision_identity; DROP INDEX events_eligibility_read_identity; TRUNCATE events`)
			must(t, err)
			_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload)
    SELECT $1, CASE WHEN i%2=0 THEN 'landing_decision_recorded' ELSE 'approval_eligibility_observed' END,
    jsonb_build_object('data',jsonb_build_object('batch_id','b'||i,'attempt_id','a'||i,
    'basis',jsonb_build_object('change_id','c'||i,'read_id','r'||i,'revision',i,'head','h'||i)))
    FROM generate_series(1,$2) i`, q, n)
			must(t, err)
			var before, after uint32
			must(t, s.pool.QueryRow(t.Context(), `SELECT relfilenode FROM pg_class WHERE oid='events'::regclass`).Scan(&before))
			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			started := time.Now()
			_, err = tx.Exec(t.Context(), string(ddl))
			must(t, err)
			t.Logf("rows=%d both-index build=%s", n, time.Since(started))
			var lock string
			must(t, tx.QueryRow(t.Context(), `SELECT mode FROM pg_locks WHERE pid=pg_backend_pid() AND relation='events'::regclass AND mode='ShareLock'`).Scan(&lock))
			t.Log("held until commit:", lock)
			conn, err := s.pool.Acquire(t.Context())
			must(t, err)
			_, err = conn.Exec(t.Context(), `SET lock_timeout='100ms'`)
			must(t, err)
			_, err = conn.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,'probe','{}')`, q)
			if err == nil || !strings.Contains(err.Error(), "lock timeout") {
				t.Fatalf("writer not blocked: %v", err)
			}
			_, resetErr := conn.Exec(t.Context(), `RESET lock_timeout`)
			must(t, resetErr)
			conn.Release()
			must(t, tx.Commit(t.Context()))
			must(t, s.pool.QueryRow(t.Context(), `SELECT relfilenode FROM pg_class WHERE oid='events'::regclass`).Scan(&after))
			if before != after {
				t.Fatal("events heap rewritten")
			}
			_, err = s.pool.Exec(t.Context(), `ANALYZE events`)
			must(t, err)
			for _, forced := range []bool{false, true} {
				tx, err := s.pool.Begin(t.Context())
				must(t, err)
				defer func() { _ = tx.Rollback(t.Context()) }()
				if forced {
					_, err = tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`)
					must(t, err)
				}
				assertEvidencePlan(t, tx, forced, "events_landing_decision_identity", landingDecisionEvidenceSQL, q, fmt.Sprint("b", n), fmt.Sprint("a", n))
				i := n - 1
				assertEvidencePlan(t, tx, forced, "events_eligibility_read_identity", eligibilityReadEvidenceSQL, q, fmt.Sprint("c", i), fmt.Sprint("r", i), fmt.Sprint(i), fmt.Sprint("h", i))
				must(t, tx.Rollback(t.Context()))
			}
		})
	}
}

func assertEvidencePlan(t *testing.T, tx pgx.Tx, forced bool, index, query string, args ...any) {
	t.Helper()
	var raw []byte
	must(t, tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&raw))
	var plans []struct {
		Plan          evidencePlan
		ExecutionTime float64 `json:"Execution Time"`
	}
	must(t, json.Unmarshal(raw, &plans))
	found := false
	var visit func(evidencePlan)
	visit = func(p evidencePlan) {
		if p.Removed != 0 {
			t.Fatalf("history filtered instead of indexed: %s", raw)
		}
		if p.Index == index {
			found = true
			if p.Rows != 1 || p.Loops != 1 {
				t.Fatalf("unexpected examined rows: %s", raw)
			}
		}
		for _, child := range p.Plans {
			visit(child)
		}
	}
	visit(plans[0].Plan)
	if !found || plans[0].Plan.Rows != 1 {
		t.Fatalf("missing identity index/result: %s", raw)
	}
	t.Logf("forced=%t index=%s execution=%.3fms plan=%s", forced, index, plans[0].ExecutionTime, raw)
}

type evidencePlan struct {
	Index   string  `json:"Index Name"`
	Rows    float64 `json:"Actual Rows"`
	Loops   float64 `json:"Actual Loops"`
	Removed float64 `json:"Rows Removed by Filter"`
	Plans   []evidencePlan
}
