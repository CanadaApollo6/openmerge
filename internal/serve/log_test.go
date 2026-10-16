package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func logRows(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		must(t, json.Unmarshal([]byte(line), &row))
		rows = append(rows, row)
	}
	return rows
}

func TestOperationalLogsCommittedLanding(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	found := map[string]int{}
	for _, row := range logRows(t, &output) {
		message := row["msg"].(string)
		found[message]++
		if row["level"] != "INFO" || row["repository"] != repo.Path || row["base"] != "main" || row["pr"] != float64(1) || row["sha"] == "" {
			t.Fatalf("missing operational context: %v", row)
		}
		if message != "enqueued" && (row["batch"] != "b1" || row["sha"] != state.BaseSHA) {
			t.Fatalf("missing batch context: %v", row)
		}
	}
	for _, message := range []string{"enqueued", "test ref built", "checks result", "landing started", "landed"} {
		if found[message] != 1 {
			t.Fatalf("%q logs=%d; output=%s", message, found[message], &output)
		}
	}
	before := output.String()
	must(t, s.cycle(t.Context(), w))
	if output.String() != before {
		t.Fatalf("unchanged reconciliation duplicated INFO logs: %s", &output)
	}
}

func TestOperationalLogsIgnoreStaleRulesEvents(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{
		queue.RulesBlocked{Problems: []string{"original"}},
		queue.RulesBlocked{Problems: []string{"newer"}, Basis: 1},
		queue.RulesCleared{Basis: 1},
		queue.RulesBlocked{Problems: []string{"stale"}, Basis: 1},
	}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	if strings.Contains(output.String(), "cleared") || strings.Contains(output.String(), "stale") || strings.Contains(output.String(), "resumed") {
		t.Fatalf("ignored rules event logged as a transition: %s", &output)
	}
	if strings.Count(output.String(), `"msg":"repository rules blocked"`) != 2 || !strings.Contains(output.String(), "newer") {
		t.Fatalf("missing committed rules transitions: %s", &output)
	}
}

func TestOperationalLogsDequeueAndPause(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	f.AddComment(repo, 1, "writer", "@openmerge dequeue")
	must(t, s.Notify(t.Context(), "7", repo.Path, "dequeue-command"))
	must(t, s.cycle(t.Context(), w))
	must(t, s.pause(t.Context(), w, "invalid-config", "broken configuration"))
	must(t, s.pause(t.Context(), w, "invalid-config", "broken configuration"))
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.ResumeRequested{}}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	found := map[string]int{}
	for _, row := range logRows(t, &output) {
		message := row["msg"].(string)
		found[message]++
		switch message {
		case "dequeued":
			if row["level"] != "INFO" || row["reason"] != "requested" || row["pr"] != float64(1) || row["sha"] == "" {
				t.Fatalf("dequeue context: %v", row)
			}
		case "queue paused", "queue resumed", "configuration error":
			if row["level"] != "INFO" || row["reason"] != "invalid-config" {
				t.Fatalf("pause context: %v", row)
			}
		case "reconcile cycle started", "reconcile cycle finished":
			if row["level"] != "DEBUG" {
				t.Fatalf("cycle logged above DEBUG: %v", row)
			}
		}
	}
	for _, message := range []string{"dequeued", "queue paused", "queue resumed", "configuration error"} {
		if found[message] != 1 {
			t.Fatalf("%q logs=%d; output=%s", message, found[message], &output)
		}
	}
	if found["reconcile cycle started"] != 2 || found["reconcile cycle finished"] != 2 {
		t.Fatalf("missing cycle logs: %s", &output)
	}
}

func TestOperationalLogsAbandonmentAndQuota(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{
		queue.LandingStarted{BatchID: state.Active.ID, AttemptID: "uncertain", FenceToken: state.Fence, At: time.Now()},
		queue.LandingAbandoned{BatchID: state.Active.ID, NewFence: "advanced"},
	}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	found := false
	for _, row := range logRows(t, &output) {
		if row["msg"] == "abandoned" {
			found = row["level"] == "INFO" && row["batch"] == state.Active.ID && row["sha"] == state.Active.TestedSHA && row["pr"] == float64(1)
		}
	}
	if !found {
		t.Fatalf("missing abandonment: %s", &output)
	}
	// Quota exhaustion is distinguished from an operational failure.
	s.options.BackoffUntil = func() time.Time { return time.Now().Add(time.Hour) }
	err = s.quota()
	if _, ok := err.(*quotaBackoff); !ok {
		t.Fatalf("quota has no typed backoff: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.options.Logger = slog.New(cancelOnQuota{Handler: slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}), cancel: cancel})
	s.loop(ctx, w)
	rows := logRows(t, &output)
	last := rows[len(rows)-1]
	if last["msg"] != "quota backoff" || last["level"] != "DEBUG" || last["repository"] != repo.Path || last["base"] != "main" {
		t.Fatalf("quota backoff was not DEBUG: %v", last)
	}
}

type cancelOnQuota struct {
	slog.Handler
	cancel context.CancelFunc
}

func (h cancelOnQuota) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	if record.Message == "quota backoff" {
		h.cancel()
	}
	return err
}
