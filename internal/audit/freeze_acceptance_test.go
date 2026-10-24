package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// The Python reference derives every expected file from journal-v1.json and
// explicit relationships. This path exercises source selection/closure too.
func TestCompleteExportV1(t *testing.T) {
	var vectors map[string]struct {
		Request    Request     `json:"request"`
		Watermarks []watermark `json:"watermarks"`
		Events     []struct {
			Source  Source          `json:"source"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	must(t, json.Unmarshal(read(t, "testdata/journal-v1.json"), &vectors))
	must(t, json.Unmarshal(read(t, "testdata/journal-r2.json"), &vectors))
	for name, v := range vectors {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "export")
			f := database(t)
			queues := map[int64]Key{}
			for _, event := range v.Events {
				queues[event.Source.QueueID] = event.Source.Queue
			}
			for _, mark := range v.Watermarks {
				queues[mark.QueueID] = mark.Queue
			}
			for id, key := range queues {
				_, err := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES($1,$2,$3,$4,$5)`, id, key.Installation, key.Repository, key.Base, key.Lane)
				must(t, err)
			}
			for _, event := range v.Events {
				_, err := f.admin.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload,recorded_at) VALUES($1,$2,$3,$4,$5)`, event.Source.ID, event.Source.QueueID, event.Source.Type, event.Payload, event.Source.RecordedAt)
				must(t, err)
			}
			_, err := f.reader.Export(t.Context(), v.Request, target)
			must(t, err)
			replay := filepath.Join(t.TempDir(), "replay")
			_, err = f.reader.Replay(t.Context(), filepath.Join(target, "manifest.json"), replay)
			must(t, err)
			manifests(t, target, replay)

			for _, file := range []string{"selection.jsonl", "events.jsonl", "manifest.json", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
				if !bytes.Equal(read(t, filepath.Join(target, file)), read(t, filepath.Join("testdata", name+"-"+file))) {
					t.Fatalf("independent frozen bytes differ %s\ngot %s\nwant %s", file, read(t, filepath.Join(target, file)), read(t, filepath.Join("testdata", name+"-"+file)))
				}
			}
		})
	}
}

// Real commit objects and a bare remote supply the SHA oracle independently
// from the journal. No openmerge refs, forge calls or external remotes are used.
func TestAuditBareRemote(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	remote := filepath.Join(root, "remote.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Audit Test", "GIT_AUTHOR_EMAIL=audit@example.invalid", "GIT_COMMITTER_NAME=Audit Test", "GIT_COMMITTER_EMAIL=audit@example.invalid", "GIT_AUTHOR_DATE=2026-10-22T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-22T12:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", work)
	commit := func(text string) string {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(work, "file"), []byte(text), 0600))
		git(work, "add", "file")
		git(work, "commit", "-m", text)
		return git(work, "rev-parse", "HEAD")
	}
	base := commit("base")
	tested := commit("tested")
	git(work, "remote", "add", "origin", remote)
	git(work, "push", "origin", "HEAD:refs/heads/main")
	f := database(t)
	f.queue(t, 1, "")
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	closureEvent(t, f, "2026-10-22T11:00:00Z", queue.BatchStarted{BatchID: "real", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: tested}}, At: at})
	closureEvent(t, f, "2026-10-22T11:01:00Z", queue.TestRefBuilt{BatchID: "real", TestedSHA: tested, Method: "merge", At: at})
	closureEvent(t, f, "2026-10-22T12:00:00Z", queue.LandingStarted{BatchID: "real", AttemptID: "attempt", FenceToken: "fence", At: at})
	closureEvent(t, f, "2026-10-22T12:01:00Z", queue.Landed{BatchID: "real", NewBaseSHA: tested, NewFence: "next"})
	outside := commit("outside")
	git(work, "push", "origin", "HEAD:refs/heads/main")
	closureEvent(t, f, "2026-10-22T12:02:00Z", queue.OutsidePushRecorded{PreviousSHA: tested, NewSHA: outside, AncestryKnown: true, HistoryKnown: true, Commits: []string{outside}, VerificationBeforeLanding: "none", At: at})
	target := filepath.Join(root, "export")
	_, err := f.reader.Export(t.Context(), Request{Installation: "i", Repository: "owner/repo"}, target)
	must(t, err)
	seen := map[string]bool{}
	must(t, lines(bytes.NewReader(read(t, filepath.Join(target, "events.jsonl"))), func(raw []byte) error {
		var e envelope
		must(t, json.Unmarshal(raw, &e))
		switch e.Source.Type {
		case "test_ref_built":
			var p struct {
				Data struct {
					TestedSHA string `json:"tested_sha"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(e.Payload, &p))
			if p.Data.TestedSHA != tested || git(remote, "rev-parse", "refs/heads/main^") != p.Data.TestedSHA {
				t.Fatal("tested SHA differs")
			}
			seen[e.Source.Type] = true
		case "landed":
			var p struct {
				Data struct {
					SHA string `json:"new_base_sha"`
				} `json:"data"`
			}
			must(t, json.Unmarshal(e.Payload, &p))
			if p.Data.SHA != tested || git(remote, "rev-parse", outside+"^") != p.Data.SHA {
				t.Fatal("landing not in remote graph")
			}
			seen[e.Source.Type] = true
		case "outside_push_recorded":
			var p struct {
				Data queue.OutsidePushRecorded `json:"data"`
			}
			must(t, json.Unmarshal(e.Payload, &p))
			if p.Data.PreviousSHA != tested || p.Data.NewSHA != git(remote, "rev-parse", "refs/heads/main") || strings.Join(p.Data.Commits, "\n") != git(remote, "rev-list", outside, "^"+tested) {
				t.Fatal("outside move differs from bare remote")
			}
			seen[e.Source.Type] = true
		}
		return nil
	}))
	if len(seen) != 3 {
		t.Fatal("missing exported graph facts", seen)
	}
	replay := filepath.Join(root, "replay")
	_, err = f.reader.Replay(t.Context(), filepath.Join(target, "manifest.json"), replay)
	must(t, err)
	manifests(t, target, replay)
}

// Keep realistic payload/config growth out of -short and routine race stress.
func TestAuditRealisticGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("realistic growth probe")
	}
	for _, n := range []int{0, 512, 2048} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			f := fx.f
			// Exclude filler from scope without removing its cost from retained lookups.
			_, err := f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T14:00:00Z'`)
			must(t, err)
			checks := queue.ChecksObserved{BatchID: "unrelated", TestedSHA: strings.Repeat("b", 40), Runs: []queue.CheckRun{}}
			for i := 1; i <= 12; i++ {
				checks.Runs = append(checks.Runs, queue.CheckRun{ID: uint64(i), Attempt: 1, WorkflowRunID: uint64(i), Name: fmt.Sprintf("CI / shard %02d", i), Source: "app:15368", State: "success"})
			}
			_, payload, err := queue.EncodeEvent(checks)
			must(t, err)
			_, err = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'checks_observed',$1::jsonb,'2026-10-22T14:00:00Z' FROM generate_series(1,$2::int)`, payload, n)
			must(t, err)
			pin := fx.events["policy_authority_applied"][0].(queue.PolicyAuthorityApplied)
			for i := 0; i < n; i++ {
				closureEvent(t, f, "2026-10-22T14:00:00Z", queue.ConfigApplied{Commit: fmt.Sprintf("config-%040d", i), Digest: pin.EffectiveDigest})
			}
			// Shared immutable batch/basis; distinct attempts have their own check/start.
			for i := 0; i < 64; i++ {
				d := fx.decision
				d.AttemptID = fmt.Sprintf("growth-%d", i)
				closureEvent(t, f, "2026-10-22T12:00:00Z", d)
				c := fx.events["landing_checks_recorded"][0].(queue.LandingChecksRecorded)
				c.AttemptID = d.AttemptID
				closureEvent(t, f, "2026-10-22T12:00:00Z", c)
				s := fx.events["landing_started"][0].(queue.LandingStarted)
				s.AttemptID = d.AttemptID
				closureEvent(t, f, "2026-10-22T12:00:00Z", s)
			}
			_, err = f.admin.Exec(t.Context(), "ANALYZE events")
			must(t, err)
			rows, err := f.admin.Query(t.Context(), `EXPLAIN (ANALYZE,BUFFERS) SELECT id,payload FROM events WHERE queue_id=1 AND type IN ('config_applied','policy_authority_applied') ORDER BY id`)
			must(t, err)
			for rows.Next() {
				var line string
				must(t, rows.Scan(&line))
				t.Log(line)
			}
			must(t, rows.Err())
			rows.Close()
			var closureWall time.Duration
			must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
				dir := t.TempDir()
				request, e := resolve(ctx, tx, Request{Installation: "7", Repository: "Owner/Repo", From: "2026-10-22T12:00:00Z", Until: "2026-10-22T13:00:00Z"})
				if e != nil {
					return e
				}
				input := filepath.Join(dir, "roots")
				e = spoolFile(input, func(out io.Writer) error {
					return queues(ctx, tx, request, dir, func(mark watermark) error {
						return scan(ctx, tx, mark, request, func(event sourceEvent) error {
							return spoolWrite(out, spoolRecord{Source: event.source, Payload: event.payload, Role: "scope", MaxID: mark.ID})
						})
					})
				})
				if e != nil {
					return e
				}
				start := time.Now()
				_, e = closure(ctx, tx, dir, input)
				closureWall = time.Since(start)
				return e
			}))
			out := filepath.Join(t.TempDir(), "export")
			start := time.Now()
			m, err := f.reader.Export(t.Context(), Request{Installation: "7", Repository: "Owner/Repo", From: "2026-10-22T12:00:00Z", Until: "2026-10-22T13:00:00Z"}, out)
			must(t, err)
			exportWall := time.Since(start)
			start = time.Now()
			replay := filepath.Join(t.TempDir(), "replay")
			_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
			must(t, err)
			replayWall := time.Since(start)
			manifests(t, out, replay)
			t.Logf("decisions=64 realistic_checks=%d runs_per_payload=12 payload_bytes=%d config_history=%d selected=%d closure=%s export=%s replay=%s", n, len(payload), n, m.Count, closureWall, exportWall, replayWall)
		})
	}
}
