package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

// Owner × retained-event growth for the complete pipeline with real check
// history. Lookups are counted, so per-lookup cost and lookups per owner are
// separable; the honest bound is documented in the 10a-2a evidence note.
func TestEvidenceOwnerEventScalingProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("owner × event scaling evidence")
	}
	for _, owners := range []int{64, 256} {
		for _, filler := range []int{0, 16384} {
			t.Run(fmt.Sprintf("owners=%d/filler=%d", owners, filler), func(t *testing.T) {
				f := database(t)
				f.queue(t, 1, "")
				tr := &queryTrace{}
				r := traced(t, f, tr)
				r.timeout = 10 * time.Minute
				c, _, _ := closureSharedChecks(t, f)
				closureEvent(t, f, "2026-10-22T14:00:02Z", queue.ChecksObserved{BatchID: c.BatchID, TestedSHA: c.TestedSHA, Runs: []queue.CheckRun{{ID: 701, Attempt: 2, WorkflowRunID: 700, Name: "ci", Source: "app:15368", State: "success"}, {ID: 800, Name: "lint", Source: "status:bot", State: "success"}}})
				_, err := f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'history','{"type":"history"}'::jsonb,'2026-10-22T14:00:00Z' FROM generate_series(1,$1::int)`, filler)
				must(t, err)
				for i := 0; i < owners; i++ {
					c.AttemptID = fmt.Sprint(i)
					closureEvent(t, f, "2026-10-22T12:00:00Z", c)
				}
				_, err = f.admin.Exec(t.Context(), `ANALYZE events`)
				must(t, err)
				count := func() int {
					tr.mu.Lock()
					defer tr.mu.Unlock()
					n := 0
					for _, q := range tr.queries {
						if strings.Contains(q, "retained_events") {
							n++
						}
					}
					tr.queries = tr.queries[:0]
					return n
				}
				out := filepath.Join(t.TempDir(), "export")
				start := time.Now()
				m, err := r.Export(t.Context(), req(), out)
				must(t, err)
				exportWall, exportLookups := time.Since(start), count()
				start = time.Now()
				_, err = r.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
				must(t, err)
				replayWall, replayLookups := time.Since(start), count()
				if m.Count != int64(owners+3) {
					t.Fatal("owner/basis count", m.Count)
				}
				t.Logf("owners=%d retained_events=%d checks_per_owner=%d export_wall=%s export_lookups=%d replay_wall=%s replay_lookups=%d", owners, owners+filler+3, len(c.Checks), exportWall.Round(time.Millisecond), exportLookups, replayWall.Round(time.Millisecond), replayLookups)
			})
		}
	}
}
