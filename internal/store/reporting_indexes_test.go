package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// This opt-in integration test measures migration 007's three reporting
// indexes against mixed journal rows and confirms the migration lock blocks
// journal writes until commit. Run separately when database timing is wanted.
func TestReportingIndexesBuildTiming(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	ddl, err := migrations.ReadFile("migrations/007_reporting_indexes.sql")
	must(t, err)
	for _, rows := range []int{2600, 260000} {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), `DROP INDEX events_reporting_pr_alias;
DROP INDEX events_reporting_change_decision;
DROP INDEX events_reporting_settlement;
TRUNCATE events`)
			must(t, err)
			_, err = s.pool.Exec(t.Context(), `WITH journal_rows AS (
    SELECT i,
        CASE
            WHEN i%100 < 5 THEN 'pr_observed'
            WHEN i%100 < 7 THEN 'change_revision_observed'
            WHEN i%100 < 10 THEN 'landing_decision_recorded'
            WHEN i%100 < 15 THEN 'landed'
            WHEN i%100 < 16 THEN 'landing_abandoned'
            WHEN i%100 < 18 THEN 'landing_started'
            ELSE 'check_observed'
        END AS event_type
    FROM generate_series(1,$2) AS series(i)
)
INSERT INTO events(queue_id,type,payload)
SELECT $1, event_type,
    jsonb_build_object('type',event_type,'version',1,'data',
        CASE event_type
            WHEN 'pr_observed' THEN jsonb_build_object('pr',i%50000+1,'head_sha','sha-'||i)
            WHEN 'change_revision_observed' THEN jsonb_build_object('pr',i%50000+1,'revision',i,'change_id','change-'||i)
            WHEN 'landing_decision_recorded' THEN jsonb_build_object('batch_id','batch-'||i,'attempt_id','attempt-'||i,
                'approval',jsonb_build_object('basis',jsonb_build_object('change_id','change-'||i)))
            WHEN 'landed' THEN jsonb_build_object('batch_id','batch-'||i,'attempt_id','attempt-'||i,'new_base_sha','sha-'||i)
            WHEN 'landing_abandoned' THEN jsonb_build_object('batch_id','batch-'||i,'attempt_id','attempt-'||i)
            WHEN 'landing_started' THEN jsonb_build_object('batch_id','batch-'||i,'attempt_id','attempt-'||i)
            ELSE jsonb_build_object('check_name','ci-'||i,'conclusion','success','head_sha','sha-'||i)
        END)
FROM journal_rows`, q, rows)
			must(t, err)

			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			started := time.Now()
			_, err = tx.Exec(t.Context(), string(ddl))
			must(t, err)
			t.Logf("rows=%d three-index build=%s", rows, time.Since(started))
			var lock string
			must(t, tx.QueryRow(t.Context(), `SELECT mode FROM pg_locks WHERE pid=pg_backend_pid() AND relation='events'::regclass AND mode='ShareLock'`).Scan(&lock))
			t.Log("held until commit:", lock)

			conn, err := s.pool.Acquire(t.Context())
			must(t, err)
			_, err = conn.Exec(t.Context(), `SET lock_timeout='100ms'`)
			must(t, err)
			_, err = conn.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,'lock_probe','{"type":"lock_probe"}')`, q)
			if err == nil || !strings.Contains(err.Error(), "lock timeout") {
				t.Fatalf("journal writer not blocked by migration lock: %v", err)
			}
			_, err = conn.Exec(t.Context(), `RESET lock_timeout`)
			must(t, err)
			conn.Release()
			must(t, tx.Commit(t.Context()))
		})
	}
}
