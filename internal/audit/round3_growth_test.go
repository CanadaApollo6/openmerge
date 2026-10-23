package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// Small fixtures isolate unrelated retained-history growth from owner growth.
// Timings are descriptive; plans preserve the actual predicate and row counts.
func TestEvidenceCheckHistoryGrowthPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("growth EXPLAIN evidence")
	}
	for _, n := range []int{0, 2000, 8000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			c, _, _ := closureSharedChecks(t, f)
			closureEvent(t, f, "2026-10-22T14:00:02Z", queue.ChecksObserved{BatchID: c.BatchID, TestedSHA: c.TestedSHA, Runs: []queue.CheckRun{{ID: 701, Attempt: 2, WorkflowRunID: 700, Name: "ci", Source: "app:15368", State: "success"}, {ID: 800, Name: "lint", Source: "status:bot", State: "success"}}})
			_, err := f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'history','{"type":"history"}'::jsonb,'2026-10-22T14:00:00Z' FROM generate_series(1,$1::int)`, n)
			must(t, err)
			for i := 0; i < 32; i++ {
				c.AttemptID = fmt.Sprint(i)
				closureEvent(t, f, "2026-10-22T12:00:00Z", c)
			}
			_, err = f.admin.Exec(t.Context(), `ANALYZE events`)
			must(t, err)
			// This is lookup's exact retained boundary and check observation predicate.
			query := `EXPLAIN (ANALYZE,BUFFERS) WITH retained_events AS NOT MATERIALIZED (SELECT * FROM events WHERE queue_id=$1 AND id<=$8) SELECT id,type,recorded_at,CASE WHEN octet_length(payload::text)<=4194304 THEN payload::text ELSE NULL END FROM retained_events WHERE queue_id=$1 AND type=$2 AND ` + checkObservationPredicate + ` LIMIT 2`
			must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
				rows, e := tx.Query(ctx, query, int64(1), "checks_observed", c.BatchID, c.TestedSHA, int64(n+100), c.Checks[0].Name, c.Checks[0].Source, int64(n+100))
				if e != nil {
					return e
				}
				defer rows.Close()
				for rows.Next() {
					var line string
					if e = rows.Scan(&line); e != nil {
						return e
					}
					t.Log(line)
				}
				return rows.Err()
			}))
			start := time.Now()
			out := filepath.Join(t.TempDir(), "export")
			m, err := f.reader.Export(t.Context(), req(), out)
			must(t, err)
			_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			must(t, err)
			if m.Count != 35 {
				t.Fatal("owner/basis count", m.Count)
			}
			t.Logf("owners=32 checks_per_owner=%d filler=%d selected=%d export_replay=%s", len(c.Checks), n, m.Count, time.Since(start))
		})
	}
}
