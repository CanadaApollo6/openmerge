package store

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestProvenanceBackupRestoreDowngradeRehearsal(t *testing.T) {
	f := newWriterFixture(t, queue.Squash)
	dump, restore := provenanceBackupTools(t, f.s)
	auditPass(t, f.s, f.q)
	before, err := f.s.Observation(t.Context(), f.q, outsidePushCursorKey)
	must(t, err)
	state, _, err := f.s.Load(t.Context(), f.q)
	must(t, err)
	snapshot, err := queue.EncodeSnapshot(state)
	must(t, err)
	countBefore := eventCount(t, f.s, f.q)
	config := f.s.pool.Config().ConnConfig
	schema := config.RuntimeParams["search_path"]
	dir := t.TempDir()
	backup := filepath.Join(dir, "verified-pre-emission.dump")
	run := func(name string, args ...string) {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	run(dump, "--format=custom", "--schema="+schema, "--file="+backup, os.Getenv("OPENMERGE_TEST_DATABASE_URL"))
	must(t, f.start(t.Context()))
	appendEvents(t, f.s, f.q, queue.Landed{BatchID: f.c.Start.BatchID, NewBaseSHA: f.decisions[0].LandedSHA, NewFence: "next"})
	must(t, f.s.RecoverProvenance(t.Context(), f.q))
	// A retained post-backup archive is required before restoring away side-era
	// history. It is never erased merely to satisfy an old decoder.
	retained := filepath.Join(dir, "retained-post-backup.dump")
	run(dump, "--format=custom", "--schema="+schema, "--file="+retained, os.Getenv("OPENMERGE_TEST_DATABASE_URL"))
	info, err := os.Stat(retained)
	must(t, err)
	if info.Size() == 0 {
		t.Fatal("post-backup evidence not retained")
	}
	var side []byte
	must(t, f.s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='change_provenance_observed' LIMIT 1`, f.q).Scan(&side))
	if _, err := queue.DecodeEvent(side); err == nil {
		t.Fatal("old event decoder accepted side-era history")
	}
	r, _, err := replayJournal(t.Context(), f.s.pool, f.q, queue.State{BaseBranch: "release", Fence: "fence"}, 0, true)
	must(t, err)
	cursor, err := r.CursorSnapshot()
	must(t, err)
	if _, err := queue.DecodeSnapshot(cursor); err == nil {
		t.Fatal("old snapshot decoder accepted v9")
	}
	// Only the explicitly configured disposable Postgres endpoint is used. The
	// restored database is isolated from the original schema and concurrent tests.
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	endpoint, err := url.Parse(raw)
	must(t, err)
	if endpoint.Hostname() != "localhost" && endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "::1" {
		t.Fatal("restore rehearsal requires the explicitly configured local test endpoint")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	must(t, err)
	defer func() { must(t, admin.Close(context.Background())) }()
	database := schema + "_restore"
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{database}.Sanitize())
	must(t, err)
	defer func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)")
		must(t, err)
	}()
	endpoint.Path = "/" + database
	run(restore, "--exit-on-error", "--dbname="+endpoint.String(), backup)
	params := endpoint.Query()
	params.Set("search_path", schema)
	endpoint.RawQuery = params.Encode()
	restored, err := Open(t.Context(), endpoint.String(), WithTestLockScope(schema+"_restore"))
	must(t, err)
	defer restored.Close()
	after, err := restored.Observation(t.Context(), f.q, outsidePushCursorKey)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("restored cursor bytes differ")
	}
	restoredState, _, err := restored.Load(t.Context(), f.q)
	must(t, err)
	restoredSnapshot, err := queue.EncodeSnapshot(restoredState)
	must(t, err)
	if !bytes.Equal(snapshot, restoredSnapshot) || eventCount(t, restored, f.q) != countBefore {
		t.Fatal("pre-emission state not restored exactly")
	}
	if _, err := queue.DecodeSnapshot(restoredSnapshot); err != nil {
		t.Fatal("restored backup cannot be read by legacy decoder", err)
	}
	var legacy outsidePushProgress
	must(t, json.Unmarshal(after, &legacy))
	if _, err := queue.DecodeSnapshot(legacy.State); err != nil {
		t.Fatal("restored cursor is incompatible", err)
	}
	t.Logf("verified pg_dump/pg_restore: %d pre-emission rows; retained %d-byte side-era archive; legacy decoder refused live v9 and accepted restored backup", countBefore, info.Size())
}

// The CI image may install several client versions. Select one that can read
// the explicitly configured test server; this does not install tools or change CI.
func provenanceBackupTools(t *testing.T, s *Store) (string, string) {
	t.Helper()
	var version int
	must(t, s.pool.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::int`).Scan(&version))
	candidates := []string{}
	if path, err := exec.LookPath("pg_dump"); err == nil {
		candidates = append(candidates, path)
	}
	installed, err := filepath.Glob("/usr/lib/postgresql/*/bin/pg_dump")
	must(t, err)
	candidates = append(candidates, installed...)
	for _, dump := range candidates {
		output, err := exec.CommandContext(t.Context(), dump, "--version").Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(string(output))
		if len(fields) < 3 {
			continue
		}
		major, _, _ := strings.Cut(fields[2], ".")
		n, err := strconv.Atoi(major)
		if err != nil || n < version/10000 {
			continue
		}
		restore := filepath.Join(filepath.Dir(dump), "pg_restore")
		if _, err := os.Stat(restore); err == nil {
			return dump, restore
		}
	}
	t.Skipf("backup/restore rehearsal requires pg_dump/pg_restore >= PostgreSQL %d; other writer/replay tests still run", version/10000)
	return "", ""
}
