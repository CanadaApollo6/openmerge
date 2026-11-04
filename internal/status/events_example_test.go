package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type pollingExampleSource struct {
	source
	calls  atomic.Int32
	failAt atomic.Int32
}

func (s *pollingExampleSource) Journals(ctx context.Context, keys []store.QueueKey) ([]store.Journal, error) {
	if s.calls.Add(1) == s.failAt.Load() {
		return nil, errors.New("temporary database outage")
	}
	return s.source.Journals(ctx, keys)
}

func TestQueueEventsDocumentedPoll(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required to execute documented polling example")
	}
	doc, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(doc), "```sh\npython3 <<'PY'\n")
	if !ok {
		t.Fatal("polling example missing")
	}
	snippet, _, ok := strings.Cut(rest, "\nPY\n```")
	if !ok {
		t.Fatal("polling example incomplete")
	}
	k := key
	k.Repository = "acme/project"
	j := store.Journal{Key: k}
	for id := int64(1); id <= 45; id++ {
		j.Events = append(j.Events, store.JournalEvent{ID: id, RecordedAt: at, Event: queue.RefreshRequested{}})
	}
	src := &pollingExampleSource{source: source{journals: []store.Journal{j}}}
	h := Handler(src, []store.QueueKey{k}, "secret", nil)
	var malformed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if malformed.Load() {
			recorder := httptest.NewRecorder()
			h.ServeHTTP(recorder, r)
			_, _ = w.Write([]byte(strings.Replace(recorder.Body.String(), `"next_cursor":20`, `"next_cursor":0`, 1)))
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	cursorFile := filepath.Join(t.TempDir(), "cursor")
	run := func(wantCursor string, wantIDs []int64, wantSuccess bool) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "python3", "-c", snippet)
		cmd.Env = append(os.Environ(), "OPENMERGE_API="+server.URL, "OPENMERGE_API_TOKEN=secret", "OPENMERGE_CURSOR_FILE="+cursorFile)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if (err == nil) != wantSuccess {
			t.Fatalf("poll error=%v stderr=%s output=%s", err, &stderr, out)
		}
		cursor, err := os.ReadFile(cursorFile)
		if err != nil || strings.TrimSpace(string(cursor)) != wantCursor {
			t.Fatalf("saved cursor=%q err=%v; want %s", cursor, err, wantCursor)
		}
		var ids []int64
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			var events []QueueEvent
			if err := json.Unmarshal([]byte(line), &events); err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				ids = append(ids, event.ID)
			}
		}
		if !reflect.DeepEqual(ids, wantIDs) {
			t.Fatal("consumed events", ids, "want", wantIDs)
		}
		t.Logf("success=%t persisted=%s consumed=%v", wantSuccess, wantCursor, ids)
	}
	if err := os.WriteFile(cursorFile, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src.failAt.Store(1)
	run("0", nil, false)
	src.calls.Store(0)
	src.failAt.Store(2)
	var first, remaining []int64
	for id := int64(1); id <= 45; id++ {
		if id <= 20 {
			first = append(first, id)
		} else {
			remaining = append(remaining, id)
		}
	}
	run("20", first, false)
	src.failAt.Store(0)
	run("45", remaining, true)
	run("45", nil, true)
	if err := os.WriteFile(cursorFile, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	malformed.Store(true)
	run("0", nil, false)
	malformed.Store(false)
	t.Run("consumer failure", func(t *testing.T) {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Skip("/dev/full required to inject output failure")
		}
		defer func() {
			if err := full.Close(); err != nil {
				t.Error(err)
			}
		}()
		cmd := exec.CommandContext(t.Context(), "python3", "-c", snippet)
		cmd.Env = append(os.Environ(), "OPENMERGE_API="+server.URL, "OPENMERGE_API_TOKEN=secret", "OPENMERGE_CURSOR_FILE="+cursorFile)
		cmd.Stdout = full
		if err := cmd.Run(); err == nil {
			t.Fatal("consumer failure succeeded")
		}
		cursor, err := os.ReadFile(cursorFile)
		if err != nil || string(cursor) != "0\n" {
			t.Fatalf("consumer failure changed cursor to %q: %v", cursor, err)
		}
	})
}
