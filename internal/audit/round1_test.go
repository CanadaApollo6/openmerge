package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type sourceQueueFixture struct {
	id   int
	base string
	lane string
}

func insertQueueKey(t *testing.T, f *fixture, q sourceQueueFixture) {
	t.Helper()
	_, e := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES($1,'i','owner/repo',$2,$3)`, q.id, q.base, q.lane)
	must(t, e)
}

func readWatermarks(t *testing.T, dir string) []watermark {
	t.Helper()
	raw := bytes.TrimSpace(read(t, filepath.Join(dir, "watermarks.jsonl")))
	if len(raw) == 0 {
		return nil
	}
	lines := bytes.Split(raw, []byte{'\n'})
	result := make([]watermark, 0, len(lines))
	for _, line := range lines {
		var w watermark
		must(t, json.Unmarshal(line, &w))
		result = append(result, w)
	}
	return result
}

func readSelections(t *testing.T, dir string) []Selection {
	t.Helper()
	raw := bytes.TrimSpace(read(t, filepath.Join(dir, "selection.jsonl")))
	if len(raw) == 0 {
		return nil
	}
	lines := bytes.Split(raw, []byte{'\n'})
	result := make([]Selection, 0, len(lines))
	for _, line := range lines {
		var s Selection
		must(t, json.Unmarshal(line, &s))
		result = append(result, s)
	}
	return result
}

func writeWatermarks(t *testing.T, dir string, watermarks []watermark) {
	t.Helper()
	var b bytes.Buffer
	for _, w := range watermarks {
		raw, e := encode(w)
		must(t, e)
		b.Write(raw)
		b.WriteByte('\n')
	}
	must(t, os.WriteFile(filepath.Join(dir, "watermarks.jsonl"), b.Bytes(), 0600))
	rewriteManifest(t, dir, func(m *Manifest) { m.WatermarksSHA256 = digest(b.Bytes()) })
}

func readEventSources(t *testing.T, dir string) []Source {
	t.Helper()
	raw := bytes.TrimSpace(read(t, filepath.Join(dir, "events.jsonl")))
	if len(raw) == 0 {
		return nil
	}
	lines := bytes.Split(raw, []byte{'\n'})
	result := make([]Source, 0, len(lines))
	for _, line := range lines {
		var e envelope
		must(t, json.Unmarshal(line, &e))
		result = append(result, e.Source)
	}
	return result
}

func TestSourcePagingAcrossQueuesAndCollation(t *testing.T) {
	f := database(t)
	baseSeeds := []string{"A", "a", "a-", "aa", "B", "b", "é", "z", "😀"}
	laneSeeds := []string{"z", "A", "a", "é", "😀", ""}
	queues := make([]sourceQueueFixture, 0, pageSize+8)
	for i := 0; i < pageSize+8; i++ {
		q := sourceQueueFixture{
			id:   100 + i,
			base: baseSeeds[i%len(baseSeeds)],
			lane: fmt.Sprintf("%02d-%s", i, laneSeeds[i%len(laneSeeds)]),
		}
		insertQueueKey(t, f, q)
		queues = append(queues, q)
		// pageSize+2 places events immediately on both sides of the event-page
		// boundary for every queue, not just the first queue in the export.
		for n := 0; n < pageSize+2; n++ {
			insert(t, f.admin, q.id, "2026-10-22T12:00:00Z")
		}
	}

	wantQueues := append([]sourceQueueFixture(nil), queues...)
	sort.Slice(wantQueues, func(i, j int) bool {
		if wantQueues[i].base != wantQueues[j].base {
			return wantQueues[i].base < wantQueues[j].base
		}
		if wantQueues[i].lane != wantQueues[j].lane {
			return wantQueues[i].lane < wantQueues[j].lane
		}
		return wantQueues[i].id < wantQueues[j].id
	})
	wantByteOrder := make([]string, 0, len(wantQueues))
	for _, q := range wantQueues {
		wantByteOrder = append(wantByteOrder, q.base+"\x00"+q.lane)
	}
	rows, e := f.admin.Query(t.Context(), `SELECT base,lane FROM queues WHERE installation='i' AND repository='owner/repo' ORDER BY base,lane`)
	must(t, e)
	columnOrder := make([]string, 0, len(queues))
	for rows.Next() {
		var base, lane string
		must(t, rows.Scan(&base, &lane))
		columnOrder = append(columnOrder, base+"\x00"+lane)
	}
	must(t, rows.Err())
	rows.Close()
	if reflect.DeepEqual(columnOrder, wantByteOrder) {
		t.Fatal("ICU-column queue order unexpectedly matched the bytewise export order")
	}

	out := filepath.Join(t.TempDir(), "ordered")
	m, e := f.reader.Export(t.Context(), req(), out)
	must(t, e)
	wantCount := int64(len(queues) * (pageSize + 2))
	if m.Count != wantCount {
		t.Fatalf("paged count=%d want=%d", m.Count, wantCount)
	}
	sources := readEventSources(t, out)
	if len(sources) != int(wantCount) {
		t.Fatalf("event rows=%d want=%d", len(sources), wantCount)
	}
	for i, source := range sources {
		want := wantQueues[i/(pageSize+2)]
		if source.QueueID != int64(want.id) || source.Queue.Base != want.base || source.Queue.Lane != want.lane {
			t.Fatalf("event %d queue=%d %q/%q, want queue=%d %q/%q", i, source.QueueID, source.Queue.Base, source.Queue.Lane, want.id, want.base, want.lane)
		}
		if i%(pageSize+2) > 0 && source.ID <= sources[i-1].ID {
			t.Fatalf("queue %d event IDs are not increasing at row %d", want.id, i)
		}
	}
}

func TestSourceReplayRejectsForgedWatermarks(t *testing.T) {
	f := database(t)
	queues := []sourceQueueFixture{{id: 1, base: "a", lane: "A"}, {id: 2, base: "b", lane: "a"}}
	for _, q := range queues {
		insertQueueKey(t, f, q)
		insert(t, f.admin, q.id, "2026-10-22T12:00:00Z")
		insert(t, f.admin, q.id, "2026-10-22T12:00:01Z")
	}
	original := filepath.Join(t.TempDir(), "original")
	_, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	watermarks := readWatermarks(t, original)
	selections := readSelections(t, original)
	if len(watermarks) != len(queues) || len(selections) != len(queues)*2 {
		t.Fatalf("fixture rows watermarks=%d selections=%d", len(watermarks), len(selections))
	}
	minSelected := make(map[int64]int64, len(queues))
	for _, s := range selections {
		if prior, ok := minSelected[s.Source.QueueID]; !ok || s.Source.ID < prior {
			minSelected[s.Source.QueueID] = s.Source.ID
		}
	}

	cases := []struct {
		name   string
		mutate func([]watermark) []watermark
	}{
		{
			name: "below-selected",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				out[0].ID = minSelected[out[0].QueueID] - 1
				return out
			},
		},
		{
			name: "unknown-queue",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				out[0].QueueID = 999999
				return out
			},
		},
		{
			name: "queue-key-mapping",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				out[0].Queue.Base += "-forged"
				return out
			},
		},
		{
			name: "queue-id-mapping",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				out[0].QueueID, out[1].QueueID = out[1].QueueID, out[0].QueueID
				return out
			},
		},
		{
			name: "duplicate",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				return append(out, out[0])
			},
		},
		{
			name: "order",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				out[0], out[1] = out[1], out[0]
				return out
			},
		},
		{
			name: "missing",
			mutate: func(in []watermark) []watermark {
				out := append([]watermark(nil), in...)
				return out[:len(out)-1]
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyExport(t, original)
			writeWatermarks(t, dir, tc.mutate(watermarks))
			target := filepath.Join(t.TempDir(), "rejected")
			if _, e := f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), target); e == nil {
				t.Fatal("accepted forged watermarks")
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("forged replay published")
			}
		})
	}
}

func TestSourceReplayRejectsEventCorruptionWithoutManifestRehash(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	manifest := read(t, filepath.Join(original, "manifest.json"))
	dir := copyExport(t, original)
	events := read(t, filepath.Join(dir, "events.jsonl"))
	old := []byte("9007199254740993")
	idx := bytes.Index(events, old)
	if idx < 0 {
		t.Fatal("fixture event integer missing")
	}
	events[idx+len(old)-1] = '2'
	must(t, os.WriteFile(filepath.Join(dir, "events.jsonl"), events, 0600))
	if _, e := f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "rejected")); e == nil {
		t.Fatal("accepted event corruption with unchanged manifest")
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "manifest.json")), manifest) {
		t.Fatal("event-only corruption changed manifest")
	}
}

func TestSourceEquivalentMicrosecondBoundsHaveIdenticalManifest(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	for _, at := range []string{
		"2026-10-22T12:00:00Z",
		"2026-10-22T12:00:00.000001Z",
		"2026-10-22T12:00:00.000002Z",
	} {
		insert(t, f.admin, 1, at)
	}
	fractional := Request{
		Repository: "owner/repo",
		From:       "2026-10-22T12:00:00.000000999Z",
		Until:      "2026-10-22T12:00:00.000001999Z",
	}
	exact := Request{
		Repository: "owner/repo",
		From:       "2026-10-22T12:00:00.000001Z",
		Until:      "2026-10-22T12:00:00.000002Z",
	}
	firstDir := filepath.Join(t.TempDir(), "fractional")
	secondDir := filepath.Join(t.TempDir(), "exact")
	first, e := f.reader.Export(t.Context(), fractional, firstDir)
	must(t, e)
	second, e := f.reader.Export(t.Context(), exact, secondDir)
	must(t, e)
	if first.Count != 1 || second.Count != 1 {
		t.Fatalf("ceil-to-microsecond counts=%d/%d want 1/1", first.Count, second.Count)
	}
	if first != second {
		t.Fatalf("equivalent timestamp bounds produced different manifests:\n%+v\n%+v", first, second)
	}
	manifests(t, firstDir, secondDir)
}
