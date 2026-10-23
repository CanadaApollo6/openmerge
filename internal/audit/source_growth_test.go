package audit

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourcePageQueriesBoundCandidateBeforeSizing(t *testing.T) {
	for name, query := range map[string]string{
		"export": sourcePageSQL,
		"replay": replayPageSQL,
	} {
		t.Run(name, func(t *testing.T) {
			lower := strings.ToLower(query)
			limit := strings.Index(lower, "limit 32")
			window := strings.Index(lower, "sum(octet_length(payload)) over")
			if limit < 0 || window < 0 || limit > window {
				t.Fatal("candidate limit does not precede cumulative byte window")
			}
			if strings.Count(lower, "payload::text") != 1 {
				t.Fatal("payload is converted to text more than once")
			}
			if !strings.Contains(lower, "candidate as materialized") {
				t.Fatal("candidate page is not materialized")
			}
		})
	}
}

func TestSourceQueueIdentityQueryOnce(t *testing.T) {
	f := database(t)
	for i := 0; i < pageSize; i++ {
		f.queue(t, i+1, fmt.Sprintf("lane-%02d", i))
	}
	tr := &queryTrace{}
	r := traced(t, f, tr)
	request := req()
	request.Installation = "i"
	_, e := r.Export(t.Context(), request, filepath.Join(t.TempDir(), "identity"))
	must(t, e)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	needle := strings.ToLower(queueIdentitySQL)
	identityQueries := 0
	for _, query := range tr.queries {
		lower := strings.ToLower(query)
		if strings.Contains(lower, needle) {
			identityQueries++
		}
		if strings.Contains(lower, "base collate") || strings.Contains(lower, "lane collate") {
			t.Fatal("queue inventory repeated a collation-dependent page query")
		}
	}
	if identityQueries != 1 {
		t.Fatalf("identity query count=%d want=1", identityQueries)
	}
}

func TestSourceQueueGrowthPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("queue growth EXPLAIN/timing evidence is non-short")
	}
	f := database(t)
	for _, n := range []int{32, 512, 2048, 8192} {
		t.Run(fmt.Sprintf("queues-%d", n), func(t *testing.T) {
			_, e := f.admin.Exec(t.Context(), `TRUNCATE events,queues`)
			must(t, e)
			total := n * 8
			_, e = f.admin.Exec(t.Context(), `INSERT INTO queues(id,installation,repository,base,lane)
SELECT g,
       CASE WHEN g <= $1::bigint THEN 'i' ELSE 'noise' END,
       CASE WHEN g <= $1::bigint THEN 'owner/repo' ELSE 'other/repo' END,
       'base-' || lpad(g::text,8,'0'),
       'lane-' || lpad(g::text,8,'0')
FROM generate_series(1,$2::bigint) AS generated(g)`, n, total)
			must(t, e)
			_, e = f.admin.Exec(t.Context(), `ANALYZE queues`)
			must(t, e)

			started := time.Now()
			rows, e := f.admin.Query(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,TIMING ON) "+queueIdentitySQL, "i", "owner/repo")
			must(t, e)
			var plan strings.Builder
			for rows.Next() {
				var line string
				must(t, rows.Scan(&line))
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			must(t, rows.Err())
			rows.Close()
			text := plan.String()
			t.Logf("matching_queues=%d total_queues=%d explain_wall=%s\n%s", n, total, time.Since(started), text)
			if n == 8192 && !strings.Contains(strings.ToLower(text), "index") {
				t.Fatal("largest identity query did not use an index access path")
			}
		})
	}
}

func TestSourceLargePayloadPagesBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large payload streaming evidence is non-short")
	}
	f := database(t)
	f.queue(t, 1, "")
	tr := &queryTrace{}
	r := traced(t, f, tr)

	_, e := f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at)
SELECT 1,'history',jsonb_build_object('type','history','n',g),'2026-10-22T12:00:00Z'
FROM generate_series(1,$1) AS generated(g)`, pageSize+1)
	must(t, e)
	countOut := filepath.Join(t.TempDir(), "count-bound")
	m, e := r.Export(t.Context(), req(), countOut)
	must(t, e)
	if m.Count != pageSize+1 {
		t.Fatalf("count-bound export=%d want=%d", m.Count, pageSize+1)
	}

	_, e = f.admin.Exec(t.Context(), `TRUNCATE events`)
	must(t, e)
	large := MaxBytes / 3
	_, e = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at)
SELECT 1,'history',jsonb_build_object('type','history','pad',repeat('x',$1)),'2026-10-22T12:00:00Z'
FROM generate_series(1,$2)`, large, pageSize+1)
	must(t, e)
	var bytesPerEvent int
	must(t, f.admin.QueryRow(t.Context(), `SELECT octet_length(payload::text) FROM events LIMIT 1`).Scan(&bytesPerEvent))
	t.Logf("events=%d bytes_per_payload=%d payload_page_budget=%d max_retained_payloads_per_page=%d", pageSize+1, bytesPerEvent, MaxBytes, MaxBytes/bytesPerEvent)
	resetQueryTrace(tr)
	largeOut := filepath.Join(t.TempDir(), "byte-bound")
	m, e = r.Export(t.Context(), req(), largeOut)
	must(t, e)
	if m.Count != pageSize+1 {
		t.Fatalf("byte-bound export=%d want=%d", m.Count, pageSize+1)
	}
	if got := countTraceQueries(tr, "FROM events WHERE queue_id=$1"); got < (pageSize+2)/2 {
		t.Fatalf("byte-bound export page queries=%d want at least 17", got)
	}

	resetQueryTrace(tr)
	replayOut := filepath.Join(t.TempDir(), "byte-bound-replay")
	_, e = r.Replay(t.Context(), filepath.Join(largeOut, "manifest.json"), replayOut)
	must(t, e)
	if got := countTraceQueries(tr, "FROM events e JOIN queues"); got < (pageSize+2)/2 {
		t.Fatalf("byte-bound replay page queries=%d want at least 17", got)
	}
	manifests(t, largeOut, replayOut)
}

func resetQueryTrace(tr *queryTrace) {
	tr.mu.Lock()
	tr.queries = nil
	tr.mu.Unlock()
}

func countTraceQueries(tr *queryTrace, needle string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	needle = strings.ToLower(needle)
	count := 0
	for _, query := range tr.queries {
		if strings.Contains(strings.ToLower(query), needle) {
			count++
		}
	}
	return count
}
