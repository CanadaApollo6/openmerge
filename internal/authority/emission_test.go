package authority

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFirstEmissionDrainBackupGate(t *testing.T) {
	now := time.Now().UTC()
	backup := filepath.Join(t.TempDir(), "pre-emission.dump")
	data := []byte("isolated backup fixture")
	if err := os.WriteFile(backup, data, 0600); err != nil {
		t.Fatal(err)
	}
	receipt := EmissionReceipt{DatabaseIdentity: "localhost:54329/test", BinaryRevision: "test-revision", DrainedWorkers: []string{"sandbox-host/worker-1", "sandbox-host/worker-2"}, DrainedAt: now.Add(-time.Minute), BackupPath: backup, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), RestoreTestedAt: now, CompatibleReadersOnly: true}
	if err := receipt.Validate(receipt.DatabaseIdentity, now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*EmissionReceipt)
	}{
		{"no workers", func(r *EmissionReceipt) { r.DrainedWorkers = nil }},
		{"old workers", func(r *EmissionReceipt) { r.CompatibleReadersOnly = false }},
		{"wrong DB", func(r *EmissionReceipt) { r.DatabaseIdentity = "other:5432/live" }},
		{"no backup", func(r *EmissionReceipt) { r.BackupPath += "-missing" }},
		{"wrong hash", func(r *EmissionReceipt) { r.BackupSHA256 = fmt.Sprintf("%064d", 0) }},
		{"no restore test", func(r *EmissionReceipt) { r.RestoreTestedAt = time.Time{} }},
		{"no revision", func(r *EmissionReceipt) { r.BinaryRevision = "" }},
		{"restore before drain", func(r *EmissionReceipt) { r.RestoreTestedAt = r.DrainedAt.Add(-time.Minute) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := receipt
			test.change(&bad)
			if bad.Validate(receipt.DatabaseIdentity, now) == nil {
				t.Fatal("first emission permitted without prerequisite")
			}
		})
	}
}
