package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type chainLogFixture struct {
	s      *Server
	w      *worker
	output bytes.Buffer
	at     time.Time
}

func newChainLogFixture(t *testing.T) *chainLogFixture {
	t.Helper()
	f := &chainLogFixture{w: &worker{}, at: time.Unix(1, 0).UTC()}
	f.w.Key.Repository, f.w.Key.BaseBranch = repo.Path, "main"
	f.w.logState = queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	f.s = &Server{options: Options{Logger: slog.New(slog.NewJSONHandler(&f.output, nil))}}
	for pr := 1; pr <= 3; pr++ {
		f.apply(queue.PRObserved{PR: pr, HeadSHA: fmt.Sprint("head", pr), Eligible: true, Trusted: true})
		f.apply(queue.EnqueueRequested{PR: pr, At: f.at})
	}
	f.apply(queue.ChainedBatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head1"}}, Position: 1, At: f.at})
	f.apply(queue.ChainedTestRefBuilt{BatchID: "b1", TestedSHA: "tested1", LandingSHA: "tested1", Position: 1, Method: queue.Merge, At: f.at})
	f.apply(queue.ChainedBatchStarted{BatchID: "b2", BaseSHA: "tested1", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head2"}, {PR: 3, HeadSHA: "head3"}}, ParentBatchID: "b1", Position: 2, At: f.at})
	f.apply(queue.ChainedTestRefBuilt{BatchID: "b2", TestedSHA: "tested2", LandingSHA: "tested2", ParentBatchID: "b1", Position: 2, Method: queue.Merge, At: f.at})
	if len(f.w.logState.Batches()) != 2 || f.w.logState.Batch("b2").Phase != queue.Testing {
		t.Fatal("fixture did not build a two-batch chain")
	}
	return f
}

func (f *chainLogFixture) apply(event queue.Event) {
	before := f.w.logState
	after := queue.Apply(before, event)
	f.s.logEvent(context.Background(), f.w, before, after, event)
	f.w.logState = after
}

func (f *chainLogFixture) observe(batch string, terminal, success bool, attempt uint64) {
	state := "pending"
	if terminal {
		state = "failure"
		if success {
			state = "success"
		}
	}
	f.apply(queue.ChecksObserved{BatchID: batch, TestedSHA: f.w.logState.Batch(batch).TestedSHA, Terminal: terminal, Success: success,
		Runs: []queue.CheckRun{{ID: 1, Attempt: attempt, Name: "ci", Source: "app:ci", State: state}}})
}

func (f *chainLogFixture) want(t *testing.T, message string, want ...string) {
	t.Helper()
	var got []string
	for _, row := range logRows(t, &f.output) {
		if row["msg"] != message {
			continue
		}
		if row["level"] != "INFO" || row["repository"] != repo.Path || row["base"] != "main" {
			t.Errorf("missing log context: %v", row)
		}
		result := row["reason"]
		if success, ok := row["success"]; ok {
			result = success
		}
		got = append(got, fmt.Sprintf("%s/%v/%s/%v", row["batch"], row["pr"], row["sha"], result))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s records = %v, want %v; output=%s", message, got, want, &f.output)
	}
}

func TestOperationalLogsChainResultsAndLanding(t *testing.T) {
	f := newChainLogFixture(t)
	f.want(t, "test ref built", "b1/1/tested1/", "b2/2/tested2/", "b2/3/tested2/")
	f.observe("b1", true, true, 1)
	f.observe("b2", true, true, 1)
	f.observe("b1", true, true, 1)
	f.observe("b2", true, true, 1)
	f.observe("b2", false, false, 2)
	f.observe("b1", true, true, 1)
	f.observe("b2", false, false, 2)
	f.observe("b2", true, true, 2)
	f.observe("b2", true, false, 1) // An older attempt cannot revoke a pass.
	f.want(t, "checks result", "b1/1/tested1/true", "b2/2/tested2/true", "b2/3/tested2/true",
		"b2/2/tested2/pending", "b2/3/tested2/pending", "b2/2/tested2/true", "b2/3/tested2/true")
	for _, id := range []string{"b1", "b2"} {
		b := f.w.logState.Batch(id)
		f.apply(queue.LandingStarted{BatchID: id, AttemptID: "attempt-" + id, FenceToken: f.w.logState.Fence, At: f.at})
		f.apply(queue.Landed{BatchID: id, NewBaseSHA: b.LandingCommit(), NewFence: "fence-" + id})
	}
	f.want(t, "landing started", "b1/1/tested1/", "b2/2/tested2/", "b2/3/tested2/")
	f.want(t, "landed", "b1/1/tested1/", "b2/2/tested2/", "b2/3/tested2/")
}

func TestOperationalLogsChainLegacyResults(t *testing.T) {
	f := newChainLogFixture(t)
	f.output.Reset()
	for _, sha := range []string{"tested1", "tested2", "tested1", "tested2"} {
		f.apply(queue.ChecksCompleted{TestedSHA: sha, Success: true})
	}
	f.apply(queue.ChecksReopened{TestedSHA: "tested2", At: f.at})
	f.apply(queue.ChecksCompleted{TestedSHA: "tested1", Success: true})
	f.apply(queue.ChecksReopened{TestedSHA: "tested2", At: f.at})
	f.apply(queue.ChecksCompleted{TestedSHA: "tested2", Success: true})
	f.want(t, "checks result", "b1/1/tested1/true", "b2/2/tested2/true", "b2/3/tested2/true",
		"b2/2/tested2/pending", "b2/3/tested2/pending", "b2/2/tested2/true", "b2/3/tested2/true")
}

func TestOperationalLogsChainFailedPredecessor(t *testing.T) {
	f := newChainLogFixture(t)
	f.output.Reset()
	f.observe("b2", true, true, 1)
	f.observe("b1", true, false, 1)
	f.apply(queue.ChecksFailureConfirmed{BatchID: "b1", TestedSHA: "tested1", Reason: queue.ChecksFailed})
	f.apply(queue.BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "predecessor-failed"})
	f.apply(queue.ChecksCompleted{TestedSHA: "tested2", Success: false})
	f.apply(queue.ChainedTestRefBuilt{BatchID: "b2", TestedSHA: "tested2", LandingSHA: "tested2", ParentBatchID: "b1", Position: 2, Method: queue.Merge, At: f.at})
	f.want(t, "checks result", "b2/2/tested2/true", "b2/3/tested2/true", "b1/1/tested1/false")
	f.want(t, "batch failed", "b1/1/tested1/checks-failed")
	f.want(t, "abandoned", "b2/2/tested2/predecessor-failed", "b2/3/tested2/predecessor-failed")
	f.want(t, "test ref built")
}

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
	t.Parallel()
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
	}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	if strings.Contains(output.String(), "cleared") || strings.Contains(output.String(), "resumed") {
		t.Fatalf("ignored rules event logged as a transition: %s", &output)
	}
	if strings.Count(output.String(), `"msg":"repository rules blocked"`) != 2 || !strings.Contains(output.String(), "newer") {
		t.Fatalf("missing committed rules transitions: %s", &output)
	}
}

func TestOperationalLogsDequeueAndPause(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig)
		var output bytes.Buffer
		s.options.Logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		f.AddComment(repo, 1, "writer", "@openmerge dequeue")
		must(t, s.Notify(t.Context(), "7", repo.Path, "dequeue-command"))
		must(t, s.cycle(t.Context(), w))
		must(t, s.block(t.Context(), w, queue.ConfigSource, "broken configuration", ""))
		must(t, s.block(t.Context(), w, queue.ConfigSource, "broken configuration", ""))
		_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.BlockSourceCleared{Source: queue.ConfigSource, Basis: 2}}, "")
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
	})
}

func TestOperationalLogsAbandonmentAndQuota(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
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
	})
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

func TestOperationalLogsOutsidePush(t *testing.T) {
	t.Parallel()
	s, w, f, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	must(t, s.cycle(t.Context(), w))
	before, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	tip, err := f.PushBase(repo, "main", "outside", nil)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	must(t, s.cycle(t.Context(), w))
	rows := logRows(t, &output)
	if len(rows) != 1 || rows[0]["msg"] != "outside push recorded" || rows[0]["previous_sha"] != before.BaseSHA || rows[0]["sha"] != tip || rows[0]["verification_before_landing"] != "none" || rows[0]["rewrite"] != false || rows[0]["history_known"] != true {
		t.Fatalf("outside push operational log: %+v", rows)
	}
}
