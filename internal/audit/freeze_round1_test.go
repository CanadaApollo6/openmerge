package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestIdentityJSONArray(t *testing.T) {
	id := evidenceIdentity("b1", "attempt")
	raw, err := json.Marshal(id)
	must(t, err)
	if string(raw) != `["b1","attempt"]` {
		t.Fatalf("identity is not an array: %s", raw)
	}
	var decoded Identity
	must(t, json.Unmarshal(raw, &decoded))
	if decoded != id {
		t.Fatal("identity round trip")
	}
	for _, raw := range []string{`"[\"b1\"]"`, `[1]`, `null`, `[]`} {
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			t.Fatal("accepted non-string tuple", raw)
		}
	}
}

func TestAuditSnapshotTimeout(t *testing.T) {
	f := database(t)
	f.reader.timeout = 0 // Deadline expired before any query; no clock or lock race.
	err := f.reader.snapshot(t.Context(), func(context.Context, pgx.Tx) error { t.Fatal("expired snapshot entered callback"); return nil })
	if !errors.Is(err, ErrTimeout) {
		t.Fatal("missing sanitized timeout", err)
	}
}

func TestAuditDecisionCountScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("decision-count scaling")
	}
	for _, n := range []int{32, 128, 512} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			f := fx.f
			f.reader.timeout = 5 * time.Minute
			for i := 1; i < n; i++ {
				d := fx.decision
				d.AttemptID = fmt.Sprintf("scaling-%d", i)
				evidenceInsert(t, f, d)
				c := fx.events["landing_checks_recorded"][0].(queue.LandingChecksRecorded)
				c.AttemptID = d.AttemptID
				evidenceInsert(t, f, c)
				start := fx.events["landing_started"][0].(queue.LandingStarted)
				start.AttemptID = d.AttemptID
				evidenceInsert(t, f, start)
			}
			_, err := f.admin.Exec(t.Context(), "ANALYZE events")
			must(t, err)
			rows, err := f.admin.Query(t.Context(), `EXPLAIN (ANALYZE,BUFFERS) SELECT id FROM events WHERE queue_id=1 AND type='landing_started' AND payload->'data'->>'batch_id'=$1 AND payload->'data'->>'attempt_id'=$2 LIMIT 2`, fx.decision.BatchID, fx.decision.AttemptID)
			must(t, err)
			for rows.Next() {
				var line string
				must(t, rows.Scan(&line))
				t.Log(line)
			}
			must(t, rows.Err())
			rows.Close()
			out := filepath.Join(t.TempDir(), "export")
			start := time.Now()
			m, err := f.reader.Export(t.Context(), Request{Installation: "7", Repository: "Owner/Repo"}, out)
			must(t, err)
			export := time.Since(start)
			start = time.Now()
			_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			must(t, err)
			t.Logf("decisions=%d selected=%d export=%s replay=%s ms_per_decision=%.3f", n, m.Count, export, time.Since(start), float64(export.Microseconds())/1000/float64(n))
		})
	}
}
