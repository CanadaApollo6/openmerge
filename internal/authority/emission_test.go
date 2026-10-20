package authority

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
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
	if err := receipt.ValidateForBinary(receipt.DatabaseIdentity, now, DefaultEmissionReceiptMaxAge, "test-revision"); err != nil {
		t.Fatal(err)
	}
	r := receipt
	r.DrainedAt = now.Add(-25 * time.Hour)
	r.RestoreTestedAt = now.Add(-24 * time.Hour)
	if err := r.ValidateForBinary(r.DatabaseIdentity, now, 26*time.Hour, "test-revision"); err != nil {
		t.Fatal(err)
	}
	r.DrainedAt = now.Add(-DefaultEmissionReceiptMaxAge)
	if err := r.ValidateForBinary(r.DatabaseIdentity, now, DefaultEmissionReceiptMaxAge, "test-revision"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*EmissionReceipt)
	}{
		{"stale drain", func(r *EmissionReceipt) { r.DrainedAt = now.Add(-25 * time.Hour) }},
		{"stale restore", func(r *EmissionReceipt) {
			r.DrainedAt = now.Add(-26 * time.Hour)
			r.RestoreTestedAt = now.Add(-25 * time.Hour)
		}},
		{"future drain", func(r *EmissionReceipt) { r.DrainedAt = now.Add(time.Minute) }},
		{"wrong binary", func(r *EmissionReceipt) { r.BinaryRevision = "other" }},
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
			if bad.ValidateForBinary(receipt.DatabaseIdentity, now, DefaultEmissionReceiptMaxAge, "test-revision") == nil {
				t.Fatal("first emission permitted without prerequisite")
			}
		})
	}
}

func TestEmissionReceiptBinaryAndAgePolicy(t *testing.T) {
	r := EmissionReceipt{BinaryRevision: "revision"}
	for _, tt := range []struct {
		revision string
		age      time.Duration
		want     string
	}{
		{"", time.Hour, "cannot attest BinaryRevision offline"},
		{"revision", 0, "maximum age must be positive"},
		{"other", time.Hour, "does not match"},
	} {
		err := r.ValidateForBinary("db", time.Now(), tt.age, tt.revision)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%+v: %v", tt, err)
		}
	}
}

func TestBinaryRevisionMetadata(t *testing.T) {
	for _, tt := range []struct {
		name           string
		info           *debug.BuildInfo
		fallback, want string
	}{
		{"missing", nil, "", ""},
		{"ldflags", nil, "release-commit", "release-commit"},
		{"empty metadata", &debug.BuildInfo{}, "release-commit", "release-commit"},
		{"vcs", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "vcs-commit"}, {Key: "vcs.modified", Value: "false"}}}, "release-commit", "vcs-commit"},
		{"dirty", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "vcs-commit"}, {Key: "vcs.modified", Value: "true"}}}, "release-commit", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := binaryRevision(tt.info, tt.fallback); got != tt.want {
				t.Fatalf("revision=%q want %q", got, tt.want)
			}
		})
	}
}
