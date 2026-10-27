package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/audit"
	"github.com/jackc/pgx/v5"
)

func TestAuditUsageAndSanitization(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://secret:token@%/private")
	good := []string{"audit", "export", "--repo", "owner/name", "--installation", "7", "--from", "2026-10-22T12:00:00Z", "--until", "2026-10-22T13:00:00Z", "--out", "unused"}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"audit"}, 2}, {[]string{"audit", "help"}, 0}, {[]string{"audit", "--help"}, 0}, {[]string{"audit", "export", "-h"}, 0},
		{[]string{"audit", "export", "--secret=https://token.invalid"}, 2},
		{[]string{"audit", "export", "--manifest", "a", "--out", "b", "--repo", ""}, 2},
		{[]string{"audit", "export", "--manifest", "", "--out", "b"}, 2},
		{good, 1},
		{append(append([]string{}, good...), "--version", "2"), 1},
		{append(append([]string{}, good...), "--version", "0"), 2},
		{append(append([]string{}, good...), "--version", "3"), 2},
		{[]string{"audit", "export", "--manifest", "a", "--out", "b", "--version", "2"}, 2},
		{append(append([]string{}, good...), "--timeout", "0s"), 2},
		{append(append([]string{}, good...), "--scratch-max-bytes", "0"), 2},
		{append(append([]string{}, good...), "--scratch-max-files", "-1"), 2},
		{append(append([]string{}, good...), "--until", "10000-01-01T00:00:00Z"), 2},
		{append(append([]string{}, good...), "stray"), 2},
	} {
		var out, err bytes.Buffer
		if code := run(tc.args, &out, &err); code != tc.code {
			t.Fatalf("code=%d want=%d: %s", code, tc.code, err.String())
		}
		if strings.Contains(err.String(), "token") || strings.Contains(err.String(), "private") || strings.Contains(err.String(), "https:") {
			t.Fatal("unsanitized error")
		}
	}
	for flag, value := range map[string]string{"--repo": "../name", "--installation": "0", "--from": "bad", "--until": "2026-10-21T00:00:00Z", "--out": ""} {
		args := append([]string{}, good...)
		for i := range args {
			if args[i] == flag {
				args[i+1] = value
			}
		}
		var out, err bytes.Buffer
		if run(args, &out, &err) != 2 {
			t.Fatal("invalid selector accepted", flag)
		}
	}
	for _, id := range []string{"007", "+7"} {
		args := append([]string{}, good...)
		args[5] = id
		var out, err bytes.Buffer
		if run(args, &out, &err) != 2 {
			t.Fatal("noncanonical installation accepted", id)
		}
	}
	t.Setenv("OPENMERGE_DATABASE_URL", "")
	var out, err bytes.Buffer
	if run(good, &out, &err) != 1 {
		t.Fatal("missing connection accepted")
	}
	auditFailure(&err, audit.ErrTimeout)
	if !strings.Contains(err.String(), "audit: export timed out") {
		t.Fatal("missing timeout diagnostic")
	}
	auditFailure(&err, audit.ErrProvenanceVersion)
	if !strings.Contains(err.String(), "--version 1 cannot include provenance; use --version 2 or omit --version") {
		t.Fatal("missing provenance version diagnostic")
	}
	auditFailure(&err, &audit.ScratchLimitError{Resource: "files", Limit: 1})
	if !strings.Contains(err.String(), "provenance scratch limit exceeded; provision disk and raise --scratch-max-bytes or --scratch-max-files") {
		t.Fatal("missing scratch limit diagnostic")
	}
	auditFailure(&err, audit.ErrPinRejected)
	if !strings.Contains(err.String(), "governing authority pin rejected by queue fold") {
		t.Fatal("missing rejected pin diagnostic")
	}
}

// This role has SELECT only. In particular, command startup cannot run schema
// migrations, take worker locks, or write a journal entry with these privileges.
func TestAuditSelectOnlyCommand(t *testing.T) {
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("shared test Postgres not configured")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("scratch endpoint required")
	}
	allowedPort := u.Port() == "54329" || u.Port() == "5432" && os.Getenv("CI") == "true" && os.Getenv("GITHUB_ACTIONS") == "true"
	if u.Hostname() != "localhost" || !allowedPort {
		t.Fatal("scratch endpoint required")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal("scratch database unavailable")
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "om_audit_cli_" + hex.EncodeToString(token[:])
	role := name + "_reader"
	schemaSQL := pgx.Identifier{name}.Sanitize()
	roleSQL := pgx.Identifier{role}.Sanitize()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schemaSQL)
	defer func() { exec("DROP SCHEMA " + schemaSQL + " CASCADE"); exec("DROP ROLE IF EXISTS " + roleSQL) }()
	exec("SET search_path TO " + schemaSQL)
	exec(`CREATE TABLE queues(id bigint PRIMARY KEY,installation text,repository text,base text,lane text);
 CREATE TABLE events(id bigint PRIMARY KEY,queue_id bigint,type text,payload jsonb,recorded_at timestamptz);
 INSERT INTO queues VALUES(1,'7','owner/name','main','');
 INSERT INTO events VALUES(1,1,'historic','{"type":"historic","n":9007199254740993}','2026-10-22T12:00:00.000001Z')`)
	exec("CREATE ROLE " + roleSQL + " NOSUPERUSER NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS")
	exec("GRANT USAGE ON SCHEMA " + schemaSQL + " TO " + roleSQL + "; GRANT SELECT ON ALL TABLES IN SCHEMA " + schemaSQL + " TO " + roleSQL)
	q := u.Query()
	q.Set("search_path", name)
	q.Set("role", role)
	u.RawQuery = q.Encode()
	t.Setenv("OPENMERGE_DATABASE_URL", u.String())
	root := t.TempDir()
	target := filepath.Join(root, "export")
	call := func(args ...string) int {
		t.Helper()
		var out, err bytes.Buffer
		code := run(append([]string{"audit", "export"}, args...), &out, &err)
		if code != 0 {
			t.Log(err.String())
		} else {
			var target string
			for i, arg := range args {
				if arg == "--out" {
					target = args[i+1]
				}
			}
			raw, e := os.ReadFile(filepath.Join(target, "manifest.json"))
			if e != nil {
				t.Fatal(e)
			}
			var manifest audit.Manifest
			if json.Unmarshal(raw, &manifest) != nil {
				t.Fatal("manifest")
			}
			if out.String() != target+"\nmanifest SHA-256: "+manifest.ManifestSHA256+"\n" {
				t.Fatal("missing publication path/digest")
			}
		}
		return code
	}
	selectors := []string{"--repo", "owner/name", "--installation", "7", "--from", "2026-10-22T07:00:00.000000001-05:00", "--until", "2026-10-22T13:00:00Z"}
	if call(append(selectors, "--out", target)...) != 0 {
		t.Fatal("export failed")
	}
	var m audit.Manifest
	b, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Count != 1 || m.Request.From != "2026-10-22T12:00:00.000001Z" {
		t.Fatalf("normalization: %+v", m)
	}
	replay := filepath.Join(root, "replay")
	if call("--manifest", filepath.Join(target, "manifest.json"), "--out", replay) != 0 {
		t.Fatal("replay failed")
	}
	for _, file := range []string{"manifest.json", "events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
		a, e := os.ReadFile(filepath.Join(target, file))
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(replay, file))
		if e != nil || !bytes.Equal(a, b) {
			t.Fatal("replay differs", file)
		}
	}
	explicit := filepath.Join(root, "explicit-v2")
	if call(append(selectors, "--out", explicit, "--version", "2")...) != 0 {
		t.Fatal("explicit v2 failed")
	}
	rawV2, err := os.ReadFile(filepath.Join(explicit, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m2 audit.Manifest
	if json.Unmarshal(rawV2, &m2) != nil || m2.Version != 2 {
		t.Fatal("explicit version ignored")
	}
	if call("--manifest", filepath.Join(explicit, "manifest.json"), "--out", filepath.Join(root, "v2-replay")) != 0 {
		t.Fatal("v2 CLI replay failed")
	}
	if call(append(selectors, "--out", target)...) != 1 {
		t.Fatal("overwrite accepted")
	}
	exec(`INSERT INTO queues VALUES(2,'8','owner/name','main','')`)
	wrong := append([]string{}, selectors...)
	wrong[3] = "9"
	if call(append(wrong, "--out", filepath.Join(root, "wrong"))...) != 2 {
		t.Fatal("wrong tenancy accepted")
	}
	var count int
	if err = conn.QueryRow(t.Context(), "SELECT count(*) FROM events").Scan(&count); err != nil || count != 1 {
		t.Fatal(fmt.Sprint("journal changed: ", count))
	}
	// The same SELECT-only connection must resolve every decision evidence family.
	var vectors map[string]struct {
		Events []struct {
			Source  audit.Source    `json:"source"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	rawVector, e := os.ReadFile("../../internal/audit/testdata/journal-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(rawVector, &vectors) != nil {
		t.Fatal("journal inputs")
	}
	exec(`INSERT INTO queues VALUES(3,'7','Owner/Repo','release','')`)
	for _, event := range vectors["approval"].Events {
		exec(`INSERT INTO events VALUES($1,3,$2,$3,$4)`, event.Source.ID+100, event.Source.Type, event.Payload, event.Source.RecordedAt)
	}
	if call("--repo", "Owner/Repo", "--installation", "7", "--from", "2026-10-22T00:00:00Z", "--until", "2026-10-23T00:00:00Z", "--out", filepath.Join(root, "approval")) != 0 {
		t.Fatal("decision-bearing SELECT-only export failed")
	}
	for _, repo := range []string{"missing/repo", "owner/Repo"} {
		if call("--repo", repo, "--installation", "7", "--from", "2026-10-22T00:00:00Z", "--until", "2026-10-23T00:00:00Z", "--out", filepath.Join(root, "bad-selector")) != 2 {
			t.Fatal("missing or wrong-case repository accepted")
		}
	}

}
