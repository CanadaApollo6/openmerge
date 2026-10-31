package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestReportSavingsUsage(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "")
	valid := []string{"report", "savings", "--installation", "1", "--repository", "fixture/app", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-02T00:00:00Z"}
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"report"}, 2, "Usage: openmerge report savings"},
		{[]string{"report", "other"}, 2, "Usage: openmerge report savings"},
		{[]string{"report", "savings", "--database-url", "postgres://u:hunter2@h/db", "--bogus"}, 2, "invalid command flags"},
		{append(valid[:len(valid):len(valid)], "extra"), 2, "flags only"},
		{[]string{"report", "savings", "--installation", "1", "--repository", "nope", "--since", "2026-10-01T00:00:00Z"}, 2, "owner/name"},
		{[]string{"report", "savings", "--installation", "1", "--repository", "fixture/app"}, 2, "RFC3339"},
		{[]string{"report", "savings", "--installation", "1", "--repository", "fixture/app", "--since", "2026-10-02T00:00:00Z", "--until", "2026-10-01T00:00:00Z"}, 2, "RFC3339"},
		{[]string{"report", "savings", "--installation", "1", "--repository", "fixture/app", "--since", "2026-10-01T00:00:00Z", "--until", "2999-01-01T00:00:00Z"}, 2, "future"},
		{append(valid[:len(valid):len(valid)], "--full-suite-minutes", "-1"), 2, "negative"},
		{valid, 2, "OPENMERGE_DATABASE_URL"},
	} {
		var out, stderr bytes.Buffer
		code := run(tc.args, &out, &stderr)
		if code != tc.code || !strings.Contains(stderr.String(), tc.want) || out.Len() != 0 || strings.Contains(stderr.String(), "hunter2") {
			t.Fatalf("%v: exit %d stdout %q stderr %q", tc.args, code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"report", "--help"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "--full-suite-minutes") {
		t.Fatalf("help: %d %s", code, stderr.String())
	}
}

// TestReportSavingsCLI reads a fixture journal from the test Postgres through
// the read-only journal reader. It never touches a deployment database.
func TestReportSavingsCLI(t *testing.T) {
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	schema := testSchemaName(t, "savings_cli_")
	if _, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	}()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	s, err := store.Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := store.QueueKey{Installation: "1", Repository: "fixture/app", BaseBranch: "main"}
	q, err := s.EnsureQueue(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }
	events := pgx.Identifier{schema, "events"}.Sanitize()
	var state queue.State
	emit := func(seconds int, e queue.Event) {
		t.Helper()
		id, err := s.Append(t.Context(), q, []queue.Event{e}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(t.Context(), "UPDATE "+events+" SET recorded_at=$1 WHERE id=$2", at(seconds), id); err != nil {
			t.Fatal(err)
		}
		state = queue.Apply(state, e)
	}
	emit(0, queue.BaseMoved{SHA: "base"})
	// Mirror the stored fold so fixture events carry the exact base and fence.
	j, err := queuemetrics.ReadJournal(t.Context(), u.String(), key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state = j.Seed
	for _, rec := range j.Events {
		state = queue.Apply(state, rec.Event)
	}
	// Three PRs share one run; a fourth fails alone and is dequeued.
	for _, pr := range []int{1, 2, 3, 4} {
		emit(10, queue.PRObserved{PR: pr, HeadSHA: "head", Eligible: true, Trusted: true})
		emit(10, queue.EnqueueRequested{PR: pr, At: at(10)})
	}
	three := []queue.BatchPR{{PR: 1, HeadSHA: "head"}, {PR: 2, HeadSHA: "head"}, {PR: 3, HeadSHA: "head"}}
	emit(20, queue.BatchStarted{BatchID: "b1", BaseSHA: state.BaseSHA, PRs: three, At: at(20)})
	emit(20, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested-b1", At: at(20), Method: queue.Merge})
	emit(80, queue.ChecksCompleted{TestedSHA: "tested-b1", Success: true})
	emit(80, queue.LandingStarted{BatchID: "b1", AttemptID: "a1", FenceToken: state.Fence, At: at(80)})
	emit(90, queue.Landed{BatchID: "b1", NewBaseSHA: "tested-b1", NewFence: "f1"})
	emit(100, queue.BatchStarted{BatchID: "b2", BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: 4, HeadSHA: "head"}}, At: at(100)})
	emit(100, queue.TestRefBuilt{BatchID: "b2", TestedSHA: "tested-b2", At: at(100), Method: queue.Merge})
	emit(160, queue.ChecksCompleted{TestedSHA: "tested-b2", Success: false})
	en := queuemetrics.Entry(state, 4)
	if en == nil || state.Batch("b2") == nil || state.Batch("b1") != nil || len(state.Entries) != 1 {
		t.Fatalf("fixture fold diverged: entry=%+v batches=%+v", en, state.Batches())
	}
	emit(165, queue.Dequeued{PR: 4, AdmissionID: en.AdmissionID, Reason: queue.ChecksFailed})
	if len(state.Entries) != 0 {
		t.Fatal("dequeue not applied")
	}

	base := []string{"report", "savings", "--database-url", u.String(), "--installation", "1", "--repository", "fixture/app", "--since", at(0).Format(time.RFC3339), "--until", at(3600).Format(time.RFC3339)}
	for _, tc := range []struct {
		name  string
		extra []string
	}{{"report-savings.json", []string{"--json", "--full-suite-minutes", "20"}}, {"report-savings.txt", nil}} {
		var out, stderr bytes.Buffer
		if code := run(append(base[:len(base):len(base)], tc.extra...), &out, &stderr); code != 0 {
			t.Fatalf("%s: exit %d: %s", tc.name, code, stderr.String())
		}
		reportGolden(t, tc.name, out.Bytes())
	}
	var out, stderr bytes.Buffer
	if code := run(append(base[:len(base):len(base)], "--json"), &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var got queuemetrics.Savings
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Baseline 3 runs (three landed PRs, no reruns); executed 2 (b1 and the failed b2).
	if got.PRsLanded != 3 || got.Runs.Executed != 2 || got.Runs.Baseline != 3 || got.Runs.Saved != 1 || got.Minutes.Source != queuemetrics.MinutesObserved || got.Minutes.PerRun != 1 || got.Isolation.ByReason["checks-failed"] != 1 {
		t.Fatalf("report %+v", got)
	}

	// Unknown queues fail closed without echoing the connection string.
	out.Reset()
	stderr.Reset()
	other := append(base[:len(base):len(base)], "--base", "release")
	if code := run(other, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "no journal") || strings.Contains(stderr.String(), schema) {
		t.Fatalf("unknown queue: %d %s", code, stderr.String())
	}
}

func reportGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}
