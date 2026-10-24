package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOfflineAnchorContract(t *testing.T) {
	out, err := exec.Command("python3", "../../scripts/test_verify_audit_anchor.py").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestAnchorTamperAcceptance(t *testing.T) {
	anchorScratchGuard(t)
	f := database(t)
	f.queue(t, 1, "")
	for i, sha := range []string{"a", "b", "c", "e"} {
		at := "2026-10-22T12:00:00Z"
		if i == 3 {
			at = "2026-10-24T12:00:00Z"
		}
		_, err := f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'base_moved',$1,$2)`, `{"type":"base_moved","data":{"sha":"`+strings.Repeat(sha, 40)+`"}}`, at)
		must(t, err)
	}
	original := filepath.Join(t.TempDir(), "original")
	m, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	t.Logf("original manifest=%s terminal=%s", m.ManifestSHA256, m.TerminalSHA256)
	events := bytes.Split(bytes.TrimSuffix(read(t, filepath.Join(original, "events.jsonl")), []byte{'\n'}), []byte{'\n'})
	selections := bytes.Split(bytes.TrimSuffix(read(t, filepath.Join(original, "selection.jsonl")), []byte{'\n'}), []byte{'\n'})
	for _, name := range []string{"unchanged", "payload", "swap", "drop", "suffix", "truncate", "bad-link-H", "payload-H", "relink-old-manifest", "swap-RH", "drop-RH", "suffix-RH", "payload-db-RH", "watermark-H"} {
		t.Run(name, func(t *testing.T) {
			dir := copyExport(t, original)
			es := append([][]byte{}, events...)
			ss := append([][]byte{}, selections...)
			switch name {
			case "payload", "payload-H", "payload-db-RH":
				es[0] = bytes.ReplaceAll(es[0], []byte(strings.Repeat("a", 40)), []byte(strings.Repeat("d", 40)))
			case "swap", "swap-RH":
				es[0], es[1] = es[1], es[0]
				if name == "swap-RH" {
					ss[0], ss[1] = ss[1], ss[0]
				}
			case "drop", "drop-RH", "relink-old-manifest":
				es = append(es[:1], es[2:]...)
				if name == "drop-RH" {
					ss = append(ss[:1], ss[2:]...)
				}
			case "suffix", "suffix-RH":
				es = es[:2]
				if name == "suffix-RH" {
					ss = ss[:2]
				}
			case "truncate":
				es[2] = es[2][:len(es[2])-1]
			case "bad-link-H":
				var v map[string]any
				must(t, json.Unmarshal(es[1], &v))
				v["previous_sha256"] = strings.Repeat("0", 64)
				es[1], err = encode(v)
				must(t, err)
			}
			rehash := strings.HasSuffix(name, "-RH")
			terminal := m.TerminalSHA256
			if rehash || name == "relink-old-manifest" {
				terminal = strings.Repeat("0", 64)
				for i, b := range es {
					var v map[string]json.RawMessage
					must(t, json.Unmarshal(b, &v))
					v["previous_sha256"], err = json.Marshal(terminal)
					must(t, err)
					es[i], err = encode(v)
					must(t, err)
					terminal = digest(es[i])
				}
			}
			if name == "payload-db-RH" {
				_, err = f.admin.Exec(t.Context(), `UPDATE events SET payload=jsonb_set(payload,'{data,sha}',to_jsonb($1::text)) WHERE id=1`, strings.Repeat("d", 40))
				must(t, err)
				defer func() {
					_, e := f.admin.Exec(t.Context(), `UPDATE events SET payload=jsonb_set(payload,'{data,sha}',to_jsonb($1::text)) WHERE id=1`, strings.Repeat("a", 40))
					must(t, e)
				}()
				var s Selection
				must(t, json.Unmarshal(ss[0], &s))
				var v map[string]json.RawMessage
				must(t, json.Unmarshal(es[0], &v))
				s.PayloadSHA256 = digest(v["payload"])
				ss[0], err = encode(s)
				must(t, err)
			}
			eb := append(bytes.Join(es, []byte{'\n'}), '\n')
			if name == "truncate" {
				eb = eb[:len(eb)-1]
			}
			must(t, os.WriteFile(filepath.Join(dir, "events.jsonl"), eb, 0600))
			sb := append(bytes.Join(ss, []byte{'\n'}), '\n')
			must(t, os.WriteFile(filepath.Join(dir, "selection.jsonl"), sb, 0600))
			if rehash || name == "bad-link-H" || name == "payload-H" {
				rewriteManifest(t, dir, func(n *Manifest) {
					n.EventsSHA256 = digest(eb)
					n.SelectionSHA256 = digest(sb)
					if rehash {
						n.Count = int64(len(es))
						n.TerminalSHA256 = terminal
					}
				})
			}
			if name == "watermark-H" {
				w := readWatermarks(t, dir)
				w[0].ID = 3
				writeWatermarks(t, dir, w)
			}
			var candidate Manifest
			must(t, json.Unmarshal(read(t, filepath.Join(dir, "manifest.json")), &candidate))
			t.Logf("candidate=%s events_file=%s selection_file=%s watermarks_file=%s", candidate.ManifestSHA256, digest(eb), digest(sb), digest(read(t, filepath.Join(dir, "watermarks.jsonl"))))
			replay := filepath.Join(t.TempDir(), "replay")
			got, e := f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), replay)
			success := name == "unchanged" || name == "drop-RH" || name == "suffix-RH" || name == "payload-db-RH" || name == "watermark-H"
			if (e == nil) != success || !success && e != errIntegrity {
				t.Fatalf("replay success=%v want=%v: %v", e == nil, success, e)
			}
			if success {
				if (got.ManifestSHA256 == m.ManifestSHA256) != (name == "unchanged") {
					t.Fatal("anchor verdict")
				}
				receipt := filepath.Join(t.TempDir(), "receipt")
				stdout := filepath.Join(t.TempDir(), "stdout")
				must(t, os.WriteFile(receipt, []byte(m.ManifestSHA256+"\n"), 0600))
				must(t, os.WriteFile(stdout, []byte(replay+"\nmanifest SHA-256: "+got.ManifestSHA256+"\n"), 0600))
				out, e := exec.Command("python3", "../../scripts/verify-audit-anchor.py", "--anchor", receipt, "--replay-stdout", stdout, "--replay-out", replay, "--earlier", dir).CombinedOutput()
				want := 0
				if name != "unchanged" {
					want = 1
				}
				code := 0
				if e != nil {
					if x, ok := e.(*exec.ExitError); ok {
						code = x.ExitCode()
					} else {
						t.Fatal(e)
					}
				}
				if code != want {
					t.Fatalf("anchor code=%d: %s", code, out)
				}
				if name == "watermark-H" && (got.TerminalSHA256 != m.TerminalSHA256 || !bytes.Equal(read(t, filepath.Join(dir, "events.jsonl")), read(t, filepath.Join(original, "events.jsonl")))) {
					t.Fatal("terminal demonstrator changed events")
				}
				if name == "drop-RH" || name == "suffix-RH" {
					must(t, os.WriteFile(stdout, []byte(replay+"\nmanifest SHA-256: "+m.ManifestSHA256+"\n"), 0600))
					overlapFailure(t, receipt, stdout, replay, original, dir, "missing source")
				}
				t.Logf("replay=0 manifest=%s terminal=%s anchor=%d", got.ManifestSHA256, got.TerminalSHA256, want)
			} else {
				t.Logf("replay rejected: %v", e)
			}
		})
	}
	for _, at := range []string{"2026-10-22T12:30:00Z", "2026-10-22T12:15:00Z"} {
		insert(t, f.admin, 1, at)
		later := filepath.Join(t.TempDir(), "later")
		_, e := f.reader.Export(t.Context(), req(), later)
		must(t, e)
		_, e = f.reader.Replay(t.Context(), filepath.Join(later, "manifest.json"), filepath.Join(t.TempDir(), "later-replay"))
		must(t, e)
		replay := filepath.Join(t.TempDir(), "earlier-replay")
		got, e := f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), replay)
		must(t, e)
		if got.ManifestSHA256 != m.ManifestSHA256 {
			t.Fatal("late source changed frozen replay")
		}
		manifests(t, original, replay)
		receipt := filepath.Join(t.TempDir(), "receipt")
		stdout := filepath.Join(t.TempDir(), "stdout")
		must(t, os.WriteFile(receipt, []byte(m.ManifestSHA256+"\n"), 0600))
		must(t, os.WriteFile(stdout, []byte(replay+"\nmanifest SHA-256: "+m.ManifestSHA256+"\n"), 0600))
		overlapFailure(t, receipt, stdout, replay, original, later, "in-window addition")
	}

}

func TestAnchorRequiredCheckTamper(t *testing.T) {
	anchorScratchGuard(t)
	for _, edit := range []string{"archive", "database"} {
		t.Run(edit, func(t *testing.T) {
			f := database(t)
			rows := bytes.Split(bytes.TrimSpace(read(t, "testdata/retry_full-events.jsonl")), []byte{'\n'})
			var sources []Selection
			must(t, json.Unmarshal([]byte("["+strings.Join(strings.Split(strings.TrimSpace(string(read(t, "testdata/retry_full-selection.jsonl"))), "\n"), ",")+"]"), &sources))
			key := sources[0].Source.Queue
			_, err := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES(1,$1,$2,$3,$4)`, key.Installation, key.Repository, key.Base, key.Lane)
			must(t, err)
			for _, b := range rows {
				var v envelope
				must(t, json.Unmarshal(b, &v))
				_, err = f.admin.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload,recorded_at) VALUES($1,1,$2,$3,$4)`, v.Source.ID, v.Source.Type, v.Payload, v.Source.RecordedAt)
				must(t, err)
			}
			original := filepath.Join(t.TempDir(), "original")
			_, err = f.reader.Export(t.Context(), Request{Installation: key.Installation, Repository: key.Repository}, original)
			must(t, err)
			// ID 22 is the relied-on checks_observed source of owner 24.
			dir := copyExport(t, original)
			if edit == "database" {
				_, err = f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=22`)
				must(t, err)
			} else {
				var events, selection []byte
				terminal := strings.Repeat("0", 64)
				for i, s := range sources {
					if s.Source.ID == 22 {
						continue
					}
					var v map[string]json.RawMessage
					must(t, json.Unmarshal(rows[i], &v))
					v["previous_sha256"], err = json.Marshal(terminal)
					must(t, err)
					b, e := encode(v)
					must(t, e)
					terminal = digest(b)
					events = append(events, append(b, '\n')...)
					b, e = encode(s)
					must(t, e)
					selection = append(selection, append(b, '\n')...)
				}
				must(t, os.WriteFile(filepath.Join(dir, "events.jsonl"), events, 0600))
				must(t, os.WriteFile(filepath.Join(dir, "selection.jsonl"), selection, 0600))
				rewriteManifest(t, dir, func(m *Manifest) {
					m.Count--
					m.TerminalSHA256 = terminal
					m.EventsSHA256 = digest(events)
					m.SelectionSHA256 = digest(selection)
				})
			}
			_, err = f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			if err != errIntegrity {
				t.Fatalf("required source removal: got %v want integrity failure", err)
			}
			t.Logf("replay rejected: %v", err)
		})
	}
}

func anchorScratchGuard(t *testing.T) {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("shared test Postgres not configured")
	}
	// Reuse the connection-free allowlist before opening even the scratch schema.
	guard := exec.Command("python3", "-c", "from adr0008_scratch_db import scratch_url; import os; print(scratch_url(os.environ['OPENMERGE_TEST_DATABASE_URL']))")
	guard.Dir = "../../scripts"
	guarded, err := guard.Output()
	if err != nil {
		t.Fatal("scratch guard rejected endpoint")
	}
	maintenance, err := pgx.Connect(t.Context(), strings.TrimSpace(string(guarded)))
	must(t, err)
	var token [16]byte
	_, err = rand.Read(token[:])
	must(t, err)
	name := "om_9c2c_" + hex.EncodeToString(token[:])
	_, err = maintenance.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	must(t, err)
	t.Cleanup(func() {
		_, e := maintenance.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		must(t, e)
		var count int
		must(t, maintenance.QueryRow(context.Background(), "SELECT count(*) FROM pg_database WHERE datname=$1", name).Scan(&count))
		if count != 0 {
			t.Error("owned database remains")
		}
		must(t, maintenance.Close(context.Background()))
	})
	scoped := exec.Command("python3", "-c", "from adr0008_scratch_db import scratch_url; import os,sys; print(scratch_url(os.environ['OPENMERGE_TEST_DATABASE_URL'],sys.argv[1]))", name)
	scoped.Dir = "../../scripts"
	out, err := scoped.Output()
	must(t, err)
	t.Setenv("OPENMERGE_TEST_DATABASE_URL", strings.TrimSpace(string(out)))
}

func overlapFailure(t *testing.T, receipt, stdout, replay, earlier, later, reason string) {
	t.Helper()
	for _, full := range []bool{false, true} {
		args := []string{"../../scripts/verify-audit-anchor.py", "--anchor", receipt, "--replay-stdout", stdout, "--replay-out", replay, "--earlier", earlier, "--later", later}
		if full {
			args = append(args, "--require-full-coverage")
		}
		out, err := exec.Command("python3", args...).CombinedOutput()
		status, ok := err.(*exec.ExitError)
		if !ok || status.ExitCode() != 1 || !bytes.Contains(out, []byte(reason)) {
			t.Fatalf("overlap: %v %s", err, out)
		}
		t.Logf("overlap full=%t exit=1: %s", full, out)
	}
}

// Rehashing isolates semantic watermark checks from the file-hash gate.
func TestAnchorWatermarkBounds(t *testing.T) {
	anchorScratchGuard(t)
	f := database(t)
	f.queue(t, 1, "")
	for i := 0; i < 3; i++ {
		insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	}
	insert(t, f.admin, 1, "2026-10-24T12:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	for _, id := range []int64{2, 5} {
		dir := copyExport(t, original)
		waters := readWatermarks(t, dir)
		if len(waters) != 1 || waters[0].ID != 4 {
			t.Fatal("unexpected original watermark")
		}
		waters[0].ID = id
		writeWatermarks(t, dir, waters)
		_, err := f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
		if err != errIntegrity {
			t.Fatalf("watermark %d: got %v", id, err)
		}
	}
}

func TestAnchorReplayIdentityWriteDenial(t *testing.T) {
	anchorScratchGuard(t)
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	_, err = f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
	must(t, err)
	// Probe the same effective identity with read-only mode disabled so privileges,
	// rather than transaction mode, must deny writes.
	conn, err := f.reader.pool.Acquire(t.Context())
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(t.Context(), `SET default_transaction_read_only=off`)
	must(t, err)
	for _, sql := range []string{
		`INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'x','{}',now())`,
		`UPDATE events SET type='x'`, `DELETE FROM events`,
		`INSERT INTO queues VALUES(2,'i','owner/repo','other','')`,
		`UPDATE queues SET lane='x'`, `DELETE FROM queues`,
	} {
		_, err := conn.Exec(t.Context(), sql)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("write probe: expected permission denied, got %v", err)
		}
	}
	replay := filepath.Join(t.TempDir(), "after-probes")
	_, err = f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), replay)
	must(t, err)
	manifests(t, original, replay)
}

func TestAnchorSelectionErasedAfterReplay(t *testing.T) {
	anchorScratchGuard(t)
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	a := filepath.Join(t.TempDir(), "a")
	m, err := f.reader.Export(t.Context(), req(), a)
	must(t, err)
	replay := filepath.Join(t.TempDir(), "replay")
	got, err := f.reader.Replay(t.Context(), filepath.Join(a, "manifest.json"), replay)
	must(t, err)
	if got.ManifestSHA256 != m.ManifestSHA256 {
		t.Fatal("replay commitment changed")
	}
	b := filepath.Join(t.TempDir(), "b")
	empty := req()
	empty.From = "2026-10-24T12:00:00Z"
	empty.Until = "2026-10-24T13:00:00Z"
	bm, err := f.reader.Export(t.Context(), empty, b)
	must(t, err)
	if bm.Count != 0 {
		t.Fatal("B must be empty")
	}
	_, err = f.reader.Replay(t.Context(), filepath.Join(b, "manifest.json"), filepath.Join(t.TempDir(), "b-replay"))
	must(t, err)
	receipt, stdout := filepath.Join(t.TempDir(), "receipt"), filepath.Join(t.TempDir(), "stdout")
	must(t, os.WriteFile(receipt, []byte(m.ManifestSHA256+"\n"), 0600))
	must(t, os.WriteFile(stdout, []byte(replay+"\nmanifest SHA-256: "+got.ManifestSHA256+"\n"), 0600))
	args := []string{"../../scripts/verify-audit-anchor.py", "--anchor", receipt, "--replay-stdout", stdout, "--replay-out", replay, "--earlier", a, "--later", b}
	out, err := exec.Command("python3", args...).CombinedOutput()
	must(t, err)
	if string(out) != "anchor and overlap match\n" {
		t.Fatalf("control: %s", out)
	}
	must(t, os.WriteFile(filepath.Join(a, "selection.jsonl"), nil, 0600))
	out, err = exec.Command("python3", args...).CombinedOutput()
	status, ok := err.(*exec.ExitError)
	if !ok || status.ExitCode() != 2 || string(out) != "malformed or unreadable input\n" {
		t.Fatalf("erasure: %v %s", err, out)
	}
}
