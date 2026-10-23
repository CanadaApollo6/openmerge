package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func closureEvent(t *testing.T, f *fixture, at string, event queue.Event) int64 {
	t.Helper()
	typ, raw, e := queue.EncodeEvent(event)
	must(t, e)
	var id int64
	must(t, f.admin.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,$1,$2,$3) RETURNING id`, typ, raw, at).Scan(&id))
	return id
}

// closureSharedChecks provides a complete batch/build identity. Multiple landing
// attempts can retain the same immutable basis without duplicating its sources.
func closureSharedChecks(t *testing.T, f *fixture) (queue.LandingChecksRecorded, int64, int64) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_landing_checks_recorded.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	checks := event.(queue.LandingChecksRecorded)
	members := make([]queue.BatchPR, len(checks.Members))
	for i, member := range checks.Members {
		members[i] = queue.BatchPR{PR: member.PR, HeadSHA: member.HeadSHA}
	}
	batch := closureEvent(t, f, "2026-10-22T14:00:00Z", queue.BatchStarted{BatchID: checks.BatchID, BaseSHA: checks.TestedSHA, PRs: members, At: checks.ObservedAt})
	built := closureEvent(t, f, "2026-10-22T14:00:01Z", queue.TestRefBuilt{BatchID: checks.BatchID, TestedSHA: checks.TestedSHA, Method: checks.Method, At: checks.ObservedAt})
	return checks, batch, built
}

func TestClosureCrossRangeSharedSource(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	checks, batch, built := closureSharedChecks(t, f)
	checks.AttemptID = "one"
	first := closureEvent(t, f, "2026-10-22T12:00:00Z", checks)
	checks.AttemptID = "two"
	second := closureEvent(t, f, "2026-10-22T12:01:00Z", checks)
	out := filepath.Join(t.TempDir(), "export")
	m, e := f.reader.Export(t.Context(), req(), out)
	must(t, e)
	if m.Count != 4 {
		t.Fatalf("closure count=%d want four unique retained sources", m.Count)
	}
	selections := readSelections(t, out)
	if len(selections) != 4 {
		t.Fatal("shared dependencies duplicated")
	}
	for i, id := range []int64{batch, built, first, second} {
		if selections[i].Source.ID != id {
			t.Fatal("source comparator order differs")
		}
	}
	for _, s := range selections[:2] {
		if s.Role != "dependency" || len(s.Kinds) != 1 || s.Kinds[0] != "check" {
			t.Fatal("out-of-range source lacks dependency role or evidence kind")
		}
	}
	if m.Dependencies != "dependency_boundaries.jsonl" {
		t.Fatal("missing dependency manifest")
	}
	var boundary struct {
		QueueID            int64 `json:"queue_id"`
		Queue              Key   `json:"queue"`
		First, Last, Count int64
	}
	must(t, json.Unmarshal(bytes.TrimSpace(read(t, filepath.Join(out, m.Dependencies))), &boundary))
	if boundary.QueueID != 1 || boundary.First != batch || boundary.Last != built || boundary.Count != 2 || boundary.Queue != selections[0].Source.Queue {
		t.Fatal("dependency boundary differs from selected sources")
	}
	for _, s := range selections[2:] {
		foundBatch, foundBuild, foundAbsence := false, false, false
		for _, ref := range s.References {
			if ref.ExpectedType == "batch_started" {
				foundBatch = ref.Kind == "check" && ref.Status == "recorded" && ref.Source != nil && ref.Source.ID == batch && ref.Owner.EventID == s.Source.ID && ref.Owner.BatchID == checks.BatchID
			}
			if ref.ExpectedType == "test_ref_built" {
				foundBuild = ref.Kind == "check" && ref.Status == "recorded" && ref.Source != nil && ref.Source.ID == built
			}
			if ref.ExpectedType == "checks_observed" && ref.Status == "not_recorded" {
				foundAbsence = true
				if ref.Source != nil {
					t.Fatal("absence fabricated a source")
				}
			}
		}
		if !foundBatch || !foundBuild || !foundAbsence {
			t.Fatal("missing typed shared check relationship or historical absence")
		}
	}
	replay := filepath.Join(t.TempDir(), "replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
	must(t, e)
	manifests(t, out, replay)
	entries, e := os.ReadDir(out)
	must(t, e)
	if len(entries) != 5 {
		t.Fatal("private closure spool leaked into published export")
	}
}

func TestClosureSpoolTotalOrderAndDedup(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "sorted")
	source := func(id int64, lane string) Source {
		return Source{QueueID: 1, ID: id, Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main", Lane: lane}, Type: "history"}
	}
	must(t, spoolFile(input, func(w io.Writer) error {
		for _, r := range []spoolRecord{
			{Source: source(9, "é"), Payload: json.RawMessage(`{"n":9}`), Role: "dependency"},
			{Source: source(2, "z"), Payload: json.RawMessage(`{"n":2}`), Role: "dependency"},
			{Source: source(1, "z"), Payload: json.RawMessage(`{"n":1}`), Role: "scope"},
			{Source: source(2, "z"), Payload: json.RawMessage(`{"n":2}`), Role: "scope"},
		} {
			if e := spoolWrite(w, r); e != nil {
				return e
			}
		}
		return nil
	}))
	must(t, sortSpool(dir, input, output))
	c, e := openSpool(output)
	must(t, e)
	defer func() { _ = c.f.Close() }()
	for i, want := range []int64{1, 2, 9} {
		if !c.ok || c.value.Source.ID != want {
			t.Fatalf("record %d differs", i)
		}
		if want == 2 && c.value.Role != "scope" {
			t.Fatal("shared source lost scope role")
		}
		must(t, c.next())
	}
	if c.ok {
		t.Fatal("duplicate source retained")
	}
	info, e := os.Stat(output)
	must(t, e)
	if info.Mode().Perm() != 0600 {
		t.Fatal("spool is not private")
	}
}

// This is a full export/closure/replay probe, including shared out-of-range
// dependencies and unknown historical payloads. Query-boundary sampling adds
// no goroutine, timing assumption, or repository-sized instrumentation list.
func TestClosurePipelineGrowthBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("complete pipeline growth/memory evidence is non-short")
	}
	f := database(t)
	f.queue(t, 1, "")
	var peak uint64
	tr := &queryTrace{sample: func() { var m runtime.MemStats; runtime.ReadMemStats(&m); peak = max(peak, m.HeapAlloc) }}
	r := traced(t, f, tr)
	r.timeout = 10 * time.Minute
	var peaks, retained []uint64
	for _, n := range []int{128, 1024} {
		_, e := f.admin.Exec(t.Context(), `TRUNCATE events`)
		must(t, e)
		checks, _, _ := closureSharedChecks(t, f)
		// Keep real checks: growth includes observation/retry history queries.
		runs := []queue.CheckRun{{ID: 701, Attempt: 2, WorkflowRunID: 700, Name: "ci", Source: "app:15368", State: "success"}, {ID: 800, Name: "lint", Source: "status:bot", State: "success"}}
		closureEvent(t, f, "2026-10-22T14:00:02Z", queue.ChecksObserved{BatchID: checks.BatchID, TestedSHA: checks.TestedSHA, Runs: runs})
		for i := 0; i < n; i++ {
			checks.AttemptID = fmt.Sprint(i)
			closureEvent(t, f, "2026-10-22T12:00:00Z", checks)
		}
		_, e = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'unknown_history',jsonb_build_object('type','unknown_history','pad',repeat('x',65536)),'2026-10-22T12:00:00Z' FROM generate_series(1,$1)`, n)
		must(t, e)
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		peak = before.HeapAlloc
		parent := t.TempDir()
		out := filepath.Join(parent, "export")
		start := time.Now()
		m, e := r.Export(t.Context(), req(), out)
		must(t, e)
		if m.Count != int64(2*n+3) {
			t.Fatal("pipeline closure truncated or duplicated")
		}
		_, e = r.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(parent, "replay"))
		must(t, e)
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		peaks = append(peaks, peak)
		retained = append(retained, after.HeapAlloc)
		t.Logf("owners=%d unique_sources=%d export_and_replay_wall=%s sampled_peak_heap=%d retained_heap=%d total_alloc_delta=%d", n, m.Count, time.Since(start), peak, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc)
	}
	// Fixed buffers, bounded source pages and fixed merge fan-in need working
	// space; the eightfold input growth must not retain an input-sized graph.
	if retained[1] > retained[0]+8<<20 {
		t.Fatal("pipeline retained heap grows with input")
	}
	if peaks[1] > peaks[0]+32<<20 {
		t.Fatal("pipeline peak heap grows with input")
	}
}

func TestClosureReplayDBStatusContradiction(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded-%t", recorded), func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			closureEvent(t, f, "2026-10-22T12:00:00Z", queue.LandingStarted{BatchID: "batch", AttemptID: "attempt"})
			var sourceID int64
			landed := queue.Landed{BatchID: "batch", NewBaseSHA: "base", NewFence: "fence"}
			if recorded {
				sourceID = closureEvent(t, f, "2026-10-22T14:00:00Z", landed)
			} else {
				// Retain a high watermark before export, then contradict historical
				// absence inside that boundary without relying on a later append.
				sourceID = insert(t, f.admin, 1, "2026-10-22T14:00:00Z")
			}
			out := filepath.Join(t.TempDir(), "export")
			_, e := f.reader.Export(t.Context(), req(), out)
			must(t, e)
			if recorded {
				_, e = f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=$1`, sourceID)
			} else {
				typ, raw, err := queue.EncodeEvent(landed)
				must(t, err)
				_, e = f.admin.Exec(t.Context(), `UPDATE events SET type=$1,payload=$2 WHERE id=$3`, typ, raw, sourceID)
			}
			must(t, e)
			target := filepath.Join(t.TempDir(), "replay")
			if _, e = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), target); e == nil {
				t.Fatal("replay accepted source/status contradiction")
			}
			if _, e = os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("failed replay published")
			}
		})
	}
}

func TestClosureReplayRejectsRehashedMissingDependency(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	closureEvent(t, f, "2026-10-22T12:00:00Z", queue.LandingStarted{BatchID: "batch", AttemptID: "attempt"})
	closureEvent(t, f, "2026-10-22T14:00:00Z", queue.Landed{BatchID: "batch", NewBaseSHA: "base", NewFence: "fence"})
	original := filepath.Join(t.TempDir(), "original")
	m, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	selections := readSelections(t, original)
	var events []envelope
	must(t, lines(bytes.NewReader(read(t, filepath.Join(original, "events.jsonl"))), func(raw []byte) error {
		var event envelope
		if e := json.Unmarshal(raw, &event); e != nil {
			return e
		}
		events = append(events, event)
		return nil
	}))
	if len(selections) != 2 || selections[1].Role != "dependency" {
		t.Fatal("fixture lacks dependency")
	}
	// All streams, boundary metadata, counts, chain hashes and manifest hashes
	// are rebuilt. Only the selected dependency source is dropped; the owning
	// recorded reference is unchanged and the retained DB source still exists.
	coherent := t.TempDir()
	must(t, build(coherent, m.Request, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
		for _, w := range readWatermarks(t, original) {
			if e := water(w); e != nil {
				return e
			}
		}
		s := selections[0]
		return emit(sourceEvent{source: s.Source, payload: events[0].Payload}, &s)
	}, nil, &m))
	if _, e = f.reader.Replay(t.Context(), filepath.Join(coherent, "manifest.json"), filepath.Join(t.TempDir(), "replay")); e == nil {
		t.Fatal("replay accepted reference to omitted selected source")
	}
}

func TestClosureReplayIgnoresSettlementAboveSavedWatermark(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	closureEvent(t, f, "2026-10-22T12:00:00Z", queue.LandingStarted{BatchID: "batch", AttemptID: "attempt"})
	out := filepath.Join(t.TempDir(), "original")
	_, e := f.reader.Export(t.Context(), req(), out)
	must(t, e)
	closureEvent(t, f, "2026-10-22T14:00:00Z", queue.Landed{BatchID: "batch", NewBaseSHA: "base", NewFence: "fence"})
	replay := filepath.Join(t.TempDir(), "replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
	must(t, e)
	manifests(t, out, replay)
}

func TestClosureSpoolMultipleMergePasses(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "sorted")
	const count = pageSize*17 + 1
	must(t, spoolFile(input, func(w io.Writer) error {
		for i := count; i > 0; i-- {
			r := spoolRecord{Source: Source{QueueID: 1, ID: int64(i), Type: "history", Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main"}}, Payload: json.RawMessage(`{}`), Role: "scope"}
			if e := spoolWrite(w, r); e != nil {
				return e
			}
			if i%31 == 0 {
				r.Role = "dependency"
				if e := spoolWrite(w, r); e != nil {
					return e
				}
			}
		}
		return nil
	}))
	must(t, sortSpool(dir, input, output))
	c, e := openSpool(output)
	must(t, e)
	defer func() { _ = c.f.Close() }()
	for id := 1; id <= count; id++ {
		if !c.ok || c.value.Source.ID != int64(id) || c.value.Role != "scope" {
			t.Fatalf("merge record %d differs", id)
		}
		must(t, c.next())
	}
	if c.ok {
		t.Fatal("merge retained duplicate")
	}
	entries, e := os.ReadDir(dir)
	must(t, e)
	if len(entries) != 2 {
		t.Fatal("intermediate merge runs leaked")
	}
}
