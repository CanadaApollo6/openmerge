package audit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Supply a separately built CLI so this small integration test needs no build plumbing.
func TestDailyAnchorScript(t *testing.T) {
	binary := os.Getenv("OPENMERGE_AUDIT_TEST_BIN")
	if binary == "" {
		t.Skip("set OPENMERGE_AUDIT_TEST_BIN to a revision-stamped CLI")
	}
	anchorScratchGuard(t)
	f := database(t)
	f.queue(t, 1, "")
	_, err := f.admin.Exec(t.Context(), "UPDATE queues SET installation='7'")
	must(t, err)
	_, err = f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,'base_moved',$1,$2)`, `{"type":"base_moved","data":{"sha":"`+strings.Repeat("a", 40)+`"}}`, time.Now().UTC().Truncate(24*time.Hour).Add(-time.Hour))
	must(t, err)
	checkout := t.TempDir()
	git := exec.Command("git", "init", checkout)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("OPENMERGE_DATABASE_URL", f.dsn)
	t.Setenv("OPENMERGE_BIN", binary)
	t.Setenv("AUDIT_REPO", "owner/repo")
	t.Setenv("AUDIT_INSTALLATION", "7")
	t.Setenv("AUDIT_OUTPUT_BASE", t.TempDir())
	t.Setenv("AUDIT_ANCHOR_CHECKOUT", checkout)
	t.Setenv("AUDIT_LAG_SECONDS", "0")
	cmd := exec.Command("bash", "../../scripts/audit-anchor-daily.sh", "--dry-run")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dry run: %v %s", err, out)
	}
	receipt, err := os.ReadFile(filepath.Join(checkout, "receipts-owner_repo-7.tsv"))
	must(t, err)
	if len(strings.Split(strings.TrimSpace(string(receipt)), "\t")) != 7 {
		t.Fatal("expected one seven-column receipt")
	}
	if exec.Command("git", "-C", checkout, "rev-parse", "HEAD").Run() == nil {
		t.Fatal("dry run created a commit")
	}
	must(t, os.Remove(filepath.Join(checkout, "receipts-owner_repo-7.tsv")))
	wrapper := filepath.Join(t.TempDir(), "bad-replay")
	must(t, os.WriteFile(wrapper, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ ${3:-} == --manifest ]]; then
    "$REAL_AUDIT_BIN" "$@" | sed 's/^manifest SHA-256: .*/manifest SHA-256: 0000000000000000000000000000000000000000000000000000000000000000/'
else
    exec "$REAL_AUDIT_BIN" "$@"
fi
`), 0700))
	t.Setenv("REAL_AUDIT_BIN", binary)
	t.Setenv("OPENMERGE_BIN", wrapper)
	cmd = exec.Command("bash", "../../scripts/audit-anchor-daily.sh")
	if cmd.Run() == nil {
		t.Fatal("invalid replay digest accepted")
	}
	if _, err := os.Stat(filepath.Join(checkout, "receipts-owner_repo-7.tsv")); !os.IsNotExist(err) {
		t.Fatal("failed replay wrote a receipt")
	}
}
