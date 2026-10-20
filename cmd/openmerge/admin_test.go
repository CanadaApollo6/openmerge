package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAdminAcknowledgeFirstEmissionGate(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://unreachable.invalid/postgres")
	dir := t.TempDir()
	good := filepath.Join(dir, "receipt")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(good, []byte("isolated fixture receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	required := []string{"admin", "acknowledge", "--queue", "1", "--event", "42", "--sha", "outside", "--operator", "on-call"}
	for _, extra := range [][]string{nil, {"--drain-receipt", good}, {"--drain-receipt", good, "--backup-receipt", empty}, {"--drain-receipt", dir, "--backup-receipt", good}} {
		var out, stderr bytes.Buffer
		if code := run(append(append([]string{}, required...), extra...), &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "first-emission gate") {
			t.Fatalf("gate failed before DB access: %d %s %s", code, out.String(), stderr.String())
		}
	}
	for _, args := range [][]string{{"admin"}, {"admin", "acknowledge", "--help"}, {"admin", "acknowledge", "--queue", "-1"}, append(required, "extra")} {
		var out, stderr bytes.Buffer
		code := run(args, &out, &stderr)
		if code != 2 && (code != 0 || len(args) != 3) {
			t.Fatalf("bad CLI exit: %d %s", code, stderr.String())
		}
	}
}

func TestAdminAcknowledgeCLI(t *testing.T) {
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	schema := fmt.Sprintf("ack_cli_%d", time.Now().UnixNano())
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
	q, err := s.EnsureQueue(t.Context(), store.QueueKey{Installation: "1", Repository: "local/fixture", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.Append(t.Context(), q, []queue.Event{queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "outside", VerificationBeforeLanding: "none", At: time.Now().UTC()}}, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	drain := filepath.Join(dir, "drain")
	backup := filepath.Join(dir, "backup")
	for _, path := range []string{drain, backup} {
		if err = os.WriteFile(path, []byte("fixture operator attestation"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENMERGE_DATABASE_URL", u.String())
	args := []string{"admin", "acknowledge", "--queue", fmt.Sprint(q), "--event", fmt.Sprint(source), "--sha", "outside", "--operator", "first operator", "--drain-receipt", drain, "--backup-receipt", backup}
	var first []byte
	for i := range 2 {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 {
			t.Fatalf("command exit %d: %s", code, stderr.String())
		}
		if i == 0 {
			first = append([]byte{}, out.Bytes()...)
			args[9] = "retry operator"
		} else if !bytes.Equal(first, out.Bytes()) {
			t.Fatal("retry changed original receipt")
		}
		var receipt store.OutsidePushAcknowledgement
		if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.SourceEventID != source || receipt.SHA != "outside" || receipt.Operator != "first operator" {
			t.Fatalf("bad receipt: %s %v", out.String(), err)
		}
	}
	// The admin opener must work without migrations. A pending migration is left
	// untouched, proving no startup DDL is hidden in an explicit acknowledgement.
	if _, err = conn.Exec(t.Context(), "DROP TABLE "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatalf("admin ran migrations: %d %s", code, stderr.String())
	}
}
