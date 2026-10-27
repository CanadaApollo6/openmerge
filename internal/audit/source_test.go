package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	admin  *pgx.Conn
	reader *Reader
	dsn    string
}

func sourceDatabaseURL(raw string) (*url.URL, bool) {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() != "localhost" {
		return nil, false
	}
	return u, true
}

func database(t *testing.T) *fixture {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("shared test Postgres not configured")
	}
	u, ok := sourceDatabaseURL(raw)
	if !ok {
		t.Fatal("only shared test Postgres is permitted")
	}
	var e error
	conn, e := pgx.Connect(t.Context(), raw)
	if e != nil {
		t.Fatal("test database unavailable")
	}
	var token [12]byte
	_, e = rand.Read(token[:])
	must(t, e)
	name := "om_audit_" + hex.EncodeToString(token[:])
	role := name + "_reader"
	schemaSQL := pgx.Identifier{name}.Sanitize()
	roleSQL := pgx.Identifier{role}.Sanitize()
	_, e = conn.Exec(t.Context(), "CREATE SCHEMA "+schemaSQL)
	must(t, e)
	t.Cleanup(func() {
		_, e := conn.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE")
		must(t, e)
		_, e = conn.Exec(context.Background(), "DROP ROLE IF EXISTS "+roleSQL)
		must(t, e)
		must(t, conn.Close(context.Background()))
	})
	_, e = conn.Exec(t.Context(), "SET search_path TO "+schemaSQL)
	must(t, e)
	_, e = conn.Exec(t.Context(), `CREATE TABLE queues(id bigint PRIMARY KEY,installation text NOT NULL,repository text NOT NULL,base text COLLATE "und-x-icu" NOT NULL,lane text COLLATE "und-x-icu" NOT NULL,UNIQUE(installation,repository,base,lane));
 CREATE TABLE events(id bigserial PRIMARY KEY,queue_id bigint NOT NULL REFERENCES queues(id),type text NOT NULL,payload jsonb NOT NULL,recorded_at timestamptz NOT NULL);
 CREATE INDEX events_queue_watermark ON events(queue_id,id);
 CREATE TABLE canonical_queues(queue_id bigint PRIMARY KEY)`)
	must(t, e)
	_, e = conn.Exec(t.Context(), "CREATE ROLE "+roleSQL+" NOSUPERUSER NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT USAGE ON SCHEMA "+schemaSQL+" TO "+roleSQL+"; GRANT SELECT ON ALL TABLES IN SCHEMA "+schemaSQL+" TO "+roleSQL)
	must(t, e)
	q := u.Query()
	q.Set("search_path", name)
	q.Set("role", role)
	u.RawQuery = q.Encode()
	reader, e := Open(t.Context(), u.String(), time.Minute)
	must(t, e)
	t.Cleanup(reader.Close)
	return &fixture{admin: conn, reader: reader, dsn: u.String()}
}

func TestSourceDatabaseURLGuardAllowsLocalhostPorts(t *testing.T) {
	for _, raw := range []string{
		"postgres://postgres:openmerge@localhost:5432/openmerge_test?sslmode=disable",
		"postgres://postgres:openmerge@localhost:5432/openmerge_test?sslmode=require&application_name=ci",
		"postgres://postgres@localhost:54329/postgres?sslmode=disable",
	} {
		if _, ok := sourceDatabaseURL(raw); !ok {
			t.Fatal("rejected permitted localhost test database URL")
		}
	}
	if _, ok := sourceDatabaseURL("postgres://postgres@example.invalid:5432/openmerge_test?sslmode=disable"); ok {
		t.Fatal("accepted non-localhost database URL")
	}
	if _, ok := sourceDatabaseURL("postgres://postgres@127.0.0.1:5432/openmerge_test?sslmode=disable"); ok {
		t.Fatal("accepted non-localhost database URL")
	}
	if _, ok := sourceDatabaseURL("postgres://postgres@localhost:5432"); !ok {
		t.Fatal("rejected localhost URL without a database path")
	}
	if _, ok := sourceDatabaseURL("postgres://postgres@localhost:5432/openmerge_test?sslmode=disable&search_path=custom"); !ok {
		t.Fatal("rejected localhost URL with extra options")
	}
}

func (f *fixture) queue(t *testing.T, id int, lane string) {
	t.Helper()
	_, e := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES($1,'i','owner/repo','main',$2)`, id, lane)
	must(t, e)
}
func insert(t *testing.T, conn interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, q int, at string) int64 {
	t.Helper()
	var id int64
	must(t, conn.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES($1,'unknown_historical','{"type":"unknown_historical","n":9007199254740993,"url":"https://historical.invalid/secret"}',$2) RETURNING id`, q, at).Scan(&id))
	return id
}
func req() Request {
	return Request{Repository: "owner/repo", From: "2026-10-22T12:00:00Z", Until: "2026-10-22T13:00:00Z"}
}
func manifests(t *testing.T, a, b string) {
	t.Helper()
	for _, name := range []string{"manifest.json", "events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
		if !reflect.DeepEqual(read(t, filepath.Join(a, name)), read(t, filepath.Join(b, name))) {
			t.Fatalf("replay changed %s", name)
		}
	}
}

type queryContextKey struct{}
type queryTrace struct {
	mu      sync.Mutex
	queries []string
	barrier func()
	sample  func()
	once    sync.Once
}

func (tr *queryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if tr.sample == nil {
		tr.mu.Lock()
		tr.queries = append(tr.queries, d.SQL)
		tr.mu.Unlock()
	}
	return context.WithValue(ctx, queryContextKey{}, d.SQL)
}
func (tr *queryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if tr.sample != nil {
		tr.sample()
	}
	s, _ := ctx.Value(queryContextKey{}).(string)
	if tr.barrier != nil && strings.Contains(s, "SELECT DISTINCT installation") {
		tr.once.Do(tr.barrier)
	}
}
func traced(t *testing.T, f *fixture, tr *queryTrace) *Reader {
	t.Helper()
	cfg, e := pgxpool.ParseConfig(f.dsn)
	must(t, e)
	cfg.ConnConfig.Tracer = tr
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	p, e := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, e)
	r := &Reader{pool: p, timeout: time.Minute}
	t.Cleanup(r.Close)
	return r
}

func TestSourceSnapshotLateCommitReplay(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	f.queue(t, 2, "é")
	f.queue(t, 3, "z")
	// Retired queues do not appear in this live/canonical inventory.
	_, e := f.admin.Exec(t.Context(), `INSERT INTO canonical_queues VALUES(1)`)
	must(t, e)
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	pending, e := f.admin.Begin(t.Context())
	must(t, e)
	defer func() { _ = pending.Rollback(context.Background()) }()
	lateID := insert(t, pending, 1, "2026-10-22T12:30:00Z")
	// A second admin connection commits a higher ID before the export snapshot.
	cfg, e := pgx.ParseConfig(f.dsn)
	must(t, e)
	delete(cfg.RuntimeParams, "role")
	other, e := pgx.ConnectConfig(t.Context(), cfg)
	must(t, e)
	defer func() { _ = other.Close(context.Background()) }()
	highID := insert(t, other, 1, "2026-10-22T12:45:00Z")
	if highID <= lateID {
		t.Fatal("missing sequence inversion")
	}
	insert(t, other, 1, "2026-10-22T13:00:00Z")
	insert(t, other, 1, "2026-10-22T11:59:59.999999Z")
	insert(t, other, 2, "2026-10-22T12:00:00Z")
	insert(t, other, 3, "2026-10-22T12:00:00Z")
	acquired := make(chan struct{})
	release := make(chan struct{})
	tr := &queryTrace{barrier: func() { close(acquired); <-release }}
	r := traced(t, f, tr)
	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	done := make(chan error, 1)
	go func() { _, e := r.Export(t.Context(), req(), first); done <- e }()
	select {
	case <-acquired:
	case err := <-done:
		t.Fatalf("export ended before barrier: %v", err)
	}
	must(t, pending.Commit(t.Context()))
	close(release)
	must(t, <-done)
	var m Manifest
	must(t, json.Unmarshal(read(t, filepath.Join(first, "manifest.json")), &m))
	if m.Count != 4 {
		t.Fatalf("count %d, want boundary/retired selection 4", m.Count)
	}
	raw := string(read(t, filepath.Join(first, "events.jsonl")))
	if strings.Index(raw, `"lane":"z"`) > strings.Index(raw, `"lane":"é"`) {
		t.Fatal("queue order is not bytewise")
	}
	if !strings.Contains(raw, `9007199254740993`) {
		t.Fatal("integer lost")
	}
	replay := filepath.Join(parent, "replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(first, "manifest.json"), replay)
	must(t, e)
	manifests(t, first, replay)
	fresh := filepath.Join(parent, "fresh")
	newM, e := f.reader.Export(t.Context(), req(), fresh)
	must(t, e)
	if newM.Count != m.Count+1 || newM.TerminalSHA256 == m.TerminalSHA256 {
		t.Fatal("fresh snapshot lost late commit")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if !strings.Contains(strings.Join(tr.queries, "\n"), "begin isolation level repeatable read read only") {
		t.Fatal("missing explicit snapshot transaction")
	}
	for _, query := range tr.queries {
		s := strings.ToLower(query)
		for _, forbidden := range []string{"advisory", "for update", "lock table", "insert ", "update ", "delete ", "create ", "alter "} {
			if strings.Contains(s, forbidden) {
				t.Fatal("reader issued forbidden SQL")
			}
		}
	}
}

func TestSourceReadOnlyTenancyAndPaging(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	empty := filepath.Join(t.TempDir(), "empty")
	em, e := f.reader.Export(t.Context(), req(), empty)
	must(t, e)
	if em.Count != 0 || em.TerminalSHA256 != zeroDigest {
		t.Fatal("invalid empty export")
	}
	replay := filepath.Join(t.TempDir(), "empty-replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(empty, "manifest.json"), replay)
	must(t, e)
	manifests(t, empty, replay)
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var ro, iso string
		var canInsert, canCreate bool
		if e := tx.QueryRow(ctx, `SELECT has_table_privilege(current_user, 'queues', 'INSERT'),has_schema_privilege(current_schema(),'CREATE')`).Scan(&canInsert, &canCreate); e != nil {
			return e
		}
		if canInsert || canCreate {
			t.Fatal("reader role has write privileges")
		}
		if e := tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only'),current_setting('transaction_isolation')`).Scan(&ro, &iso); e != nil {
			return e
		}
		if ro != "on" || iso != "repeatable read" {
			t.Fatal("incorrect snapshot settings")
		}
		return nil
	}))
	if _, e := f.reader.pool.Exec(t.Context(), `INSERT INTO queues VALUES(99,'i','owner/repo','main','x')`); e == nil {
		t.Fatal("reader could write")
	}
	for i := 0; i < pageSize+3; i++ {
		insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	}
	for i := 2; i < pageSize+4; i++ {
		f.queue(t, i, string(rune('a'+i)))
	}
	out := filepath.Join(t.TempDir(), "out")
	m, e := f.reader.Export(t.Context(), req(), out)
	must(t, e)
	if m.Count != pageSize+3 {
		t.Fatal("page truncated")
	}
	_, e = f.admin.Exec(t.Context(), `INSERT INTO queues VALUES(999,'other','owner/repo','main','')`)
	must(t, e)
	if _, e = f.reader.Export(t.Context(), req(), filepath.Join(t.TempDir(), "ambiguous")); e == nil {
		t.Fatal("combined tenancy")
	}
	explicit := req()
	explicit.Installation = "i"
	_, e = f.reader.Export(t.Context(), explicit, filepath.Join(t.TempDir(), "explicit"))
	must(t, e)
	for _, request := range []Request{{}, {Repository: "missing"}, {Repository: "owner/repo", From: "bad"}, {Repository: "owner/repo", From: explicit.Until, Until: explicit.From}} {
		if _, e = f.reader.Export(t.Context(), request, filepath.Join(t.TempDir(), "invalid")); e == nil {
			t.Fatal("accepted invalid range/identity")
		}
	}
}

func rewriteManifest(t *testing.T, dir string, change func(*Manifest)) {
	t.Helper()
	var m Manifest
	must(t, json.Unmarshal(read(t, filepath.Join(dir, "manifest.json")), &m))
	change(&m)
	m.ManifestSHA256 = ""
	b, e := encode(m)
	must(t, e)
	m.ManifestSHA256 = digest(b)
	b, e = encode(m)
	must(t, e)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0600))
}
func copyExport(t *testing.T, from string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"manifest.json", "events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
		must(t, os.WriteFile(filepath.Join(dir, name), read(t, filepath.Join(from, name)), 0600))
	}
	return dir
}

func TestSourceStrictReplayIntegrity(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	parent := t.TempDir()
	original := filepath.Join(parent, "original")
	_, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	cases := map[string]func(string){
		"version":  func(d string) { rewriteManifest(t, d, func(m *Manifest) { m.Version = 3 }) },
		"path":     func(d string) { rewriteManifest(t, d, func(m *Manifest) { m.Selection = "../selection.jsonl" }) },
		"absolute": func(d string) { rewriteManifest(t, d, func(m *Manifest) { m.Selection = "/tmp/selection.jsonl" }) },
		"count":    func(d string) { rewriteManifest(t, d, func(m *Manifest) { m.Count++ }) },
		"terminal": func(d string) { rewriteManifest(t, d, func(m *Manifest) { m.TerminalSHA256 = zeroDigest }) },
		"metadata": func(d string) {
			b := strings.Replace(string(read(t, filepath.Join(d, "selection.jsonl"))), "12:00:00", "12:00:01", 1)
			must(t, os.WriteFile(filepath.Join(d, "selection.jsonl"), []byte(b), 0600))
			rewriteManifest(t, d, func(m *Manifest) { m.SelectionSHA256 = digest([]byte(b)) })
		},
		"event-unknown": func(d string) {
			b := read(t, filepath.Join(d, "events.jsonl"))
			b = append([]byte(`{"unknown":true,`), b[1:]...)
			must(t, os.WriteFile(filepath.Join(d, "events.jsonl"), b, 0600))
			rewriteManifest(t, d, func(m *Manifest) { m.EventsSHA256 = digest(b) })
		},
		"selection-unknown": func(d string) {
			b := read(t, filepath.Join(d, "selection.jsonl"))
			b = append([]byte(`{"unknown":true,`), b[1:]...)
			must(t, os.WriteFile(filepath.Join(d, "selection.jsonl"), b, 0600))
			rewriteManifest(t, d, func(m *Manifest) { m.SelectionSHA256 = digest(b) })
		},
		"payload": func(_ string) {
			_, e := f.admin.Exec(t.Context(), `UPDATE events SET payload='{"type":"unknown_historical","changed":true}' WHERE queue_id=1`)
			must(t, e)
		},
		"unknown": func(d string) {
			b := read(t, filepath.Join(d, "manifest.json"))
			b = append([]byte(`{"unknown":true,`), b[1:]...)
			must(t, os.WriteFile(filepath.Join(d, "manifest.json"), b, 0600))
		},
		"manifest-metadata": func(d string) {
			b := strings.Replace(string(read(t, filepath.Join(d, "manifest.json"))), "owner/repo", "owner/other", 1)
			must(t, os.WriteFile(filepath.Join(d, "manifest.json"), []byte(b), 0600))
		},
		"truncated": func(d string) {
			b := read(t, filepath.Join(d, "selection.jsonl"))
			must(t, os.WriteFile(filepath.Join(d, "selection.jsonl"), b[:len(b)/2], 0600))
		},
		"symlink": func(d string) {
			must(t, os.Remove(filepath.Join(d, "selection.jsonl")))
			must(t, os.Symlink(filepath.Join(original, "selection.jsonl"), filepath.Join(d, "selection.jsonl")))
		},
	}
	// Each subtest restores the database so hash failures cannot mask another case.
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, e := f.admin.Exec(t.Context(), `UPDATE events SET payload='{"type":"unknown_historical","n":9007199254740993,"url":"https://historical.invalid/secret"}' WHERE queue_id=1`)
			must(t, e)
			dir := copyExport(t, original)
			change(dir)
			target := filepath.Join(t.TempDir(), "rejected")
			if _, e = f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), target); e == nil {
				t.Fatal("accepted corrupt replay")
			}
			if _, e = os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("failed replay published")
			}
		})
	}
	_, e = f.admin.Exec(t.Context(), `DELETE FROM events WHERE queue_id=1`)
	must(t, e)
	if _, e = f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), filepath.Join(t.TempDir(), "missing")); e == nil {
		t.Fatal("accepted missing source")
	}

}

func TestSourceStreamingGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("growth/memory evidence is non-short")
	}
	f := database(t)
	f.queue(t, 1, "")
	parent := t.TempDir()
	var retained, peaks []uint64
	var peak uint64
	tr := &queryTrace{sample: func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > peak {
			peak = m.HeapAlloc
		}
	}}
	r := traced(t, f, tr)
	for _, n := range []int{5000, 20000} {
		_, e := f.admin.Exec(t.Context(), `TRUNCATE events`)
		must(t, e)
		_, e = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'history',jsonb_build_object('type','history','pad',repeat('x',1024)), '2026-10-22T12:00:00Z'::timestamptz FROM generate_series(1,$1)`, n)
		must(t, e)
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		out := filepath.Join(parent, time.Unix(int64(n), 0).UTC().Format("150405"))
		peak = 0
		started := time.Now()
		m, e := r.Export(t.Context(), req(), out)
		must(t, e)
		if m.Count != int64(n) {
			t.Fatal("stream truncated")
		}
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		retained = append(retained, after.HeapAlloc)
		t.Logf("events=%d wall=%s heap_before=%d heap_after_GC=%d total_alloc_delta=%d page_size=%d payload_limit=%d sampled_peak_heap=%d", n, time.Since(started), before.HeapAlloc, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc, pageSize, MaxBytes, peak)
		exportPeak := peak
		peak = 0
		_, e = r.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(parent, "replay"+filepath.Base(out)))
		must(t, e)
		t.Logf("events=%d replay_sampled_peak_heap=%d", n, peak)
		peaks = append(peaks, max(exportPeak, peak))
	}
	if peaks[1] > peaks[0]+16<<20 {
		t.Fatal("peak heap grows with repository")
	}
	if retained[1] > retained[0]+16<<20 {
		t.Fatal("retained heap grows with repository")
	}

	// A sparse queue amongst retained history demonstrates the queue/watermark
	// access path. A one-queue table can legitimately prefer its primary index.
	_, e := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES(2,'i','other/repo','main',''); INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 2,'history','{}','2026-10-22T12:00:00Z' FROM generate_series(1,32); ANALYZE events`)
	must(t, e)
	var plan string
	rows, e := f.admin.Query(t.Context(), "EXPLAIN (ANALYZE,BUFFERS) "+sourcePageSQL, 2, int64(0), int64(1<<62), nil, nil)
	must(t, e)
	for rows.Next() {
		var line string
		must(t, rows.Scan(&line))
		plan += line + "\n"
	}
	must(t, rows.Err())
	rows.Close()
	t.Log(plan)
	if !strings.Contains(plan, "events_queue_watermark") {
		t.Fatal("paged query did not use events_queue_watermark")
	}
	_, e = f.admin.Exec(t.Context(), `TRUNCATE events; INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'large',jsonb_build_object('pad',repeat('x',4194305)),'2026-10-22T12:00:00Z')`)
	must(t, e)
	target := filepath.Join(parent, "oversized")
	if _, e = r.Export(t.Context(), req(), target); e == nil {
		t.Fatal("accepted oversized source")
	}
	if _, e = os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("oversized source published")
	}
	entries, e := os.ReadDir(parent)
	must(t, e)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".audit-staging-") {
			t.Fatal("failed stream left staging")
		}
	}
}

func TestSourceSanitizedErrors(t *testing.T) {
	if _, e := Open(t.Context(), "postgres://token:secret@invalid.invalid/db?bad=%zz", time.Minute); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("unsafe connection error")
	}
	r, e := Open(t.Context(), "postgres://token:secret@localhost:1/db?sslmode=disable", time.Second)
	must(t, e)
	defer r.Close()
	_, e = r.Export(t.Context(), req(), filepath.Join(t.TempDir(), "out"))
	if e == nil || strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "postgres://") {
		t.Fatal("unsafe query error")
	}
}

func TestSourceSubmicrosecondBoundaries(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	for _, at := range []string{"2026-10-22T12:00:00Z", "2026-10-22T12:00:00.000001Z", "2026-10-22T12:00:00.000002Z"} {
		insert(t, f.admin, 1, at)
	}
	for _, tc := range []struct {
		from, until string
		count       int64
	}{
		{"2026-10-22T12:00:00.000000001Z", "2026-10-22T12:00:00.000001001Z", 1},
		{"2026-10-22T12:00:00.000000001Z", "2026-10-22T12:00:00.000002Z", 1},
		{"2026-10-22T12:00:00.000000001Z", "2026-10-22T12:00:00.000000002Z", 0},
		{"2026-10-22T07:00:00-05:00", "2026-10-22T07:00:00.000002-05:00", 2},
	} {
		m, e := f.reader.Export(t.Context(), Request{Repository: "owner/repo", From: tc.from, Until: tc.until}, filepath.Join(t.TempDir(), "bounds"))
		must(t, e)
		if m.Count != tc.count {
			t.Fatalf("submicrosecond count=%d want=%d", m.Count, tc.count)
		}
	}
}
