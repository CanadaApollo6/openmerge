package authority

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"
)

// ErrBinaryRevisionMismatch identifies a receipt for a different build.
var ErrBinaryRevisionMismatch = errors.New("emission receipt BinaryRevision does not match running binary revision")

// DefaultEmissionReceiptMaxAge bounds both drain and restore-test timestamps.
const DefaultEmissionReceiptMaxAge = 24 * time.Hour

// BuildRevision may be set by release tooling with -ldflags -X. VCS metadata
// takes precedence; a dirty build cannot attest to a committed revision.
var BuildRevision string

// RunningBinaryRevision returns the attestable embedded revision, or empty.
func RunningBinaryRevision() string {
	info, _ := debug.ReadBuildInfo()
	revision, attestable := EmbeddedBinaryRevision(info, BuildRevision)
	if !attestable {
		return ""
	}
	return revision
}

// EmbeddedBinaryRevision reports the effective revision and whether it can be
// used in a receipt. VCS metadata takes precedence over the ldflags fallback;
// dirty metadata prevents attestation regardless of the revision source.
func EmbeddedBinaryRevision(info *debug.BuildInfo, fallback string) (revision string, attestable bool) {
	dirty := false
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				dirty = true
			}
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
		}
	}
	if revision == "" {
		revision = fallback
	}
	return revision, revision != "" && !dirty
}

// EmissionReceipt is retained by the operator with the pre-emission backup.
// This is an explicit operator attestation, not automatic worker discovery.
// DatabaseIdentity is the versioned server-side cluster/database/schema/genesis digest.
type EmissionReceipt struct {
	DatabaseIdentity      string    `json:"database_identity"`
	BinaryRevision        string    `json:"binary_revision"`
	DrainedWorkers        []string  `json:"drained_workers"`
	DrainedAt             time.Time `json:"drained_at"`
	BackupPath            string    `json:"backup_path"`
	BackupSHA256          string    `json:"backup_sha256"`
	RestoreTestedAt       time.Time `json:"restore_tested_at"`
	CompatibleReadersOnly bool      `json:"compatible_readers_only"`
}

// Validate gates every explicit verification, including still-github queues.
// Local receipt failures precede identity reads; binding failures precede
// writes and seeing reads. Operators inventory all hosts, stop old workers, capture a backup, restore-test it, then attest that
// only compatible readers may restart. Period B permits isolated sandboxes only.
func (r EmissionReceipt) Validate(identity string, now time.Time) error {
	return r.ValidateForBinary(identity, now, DefaultEmissionReceiptMaxAge, RunningBinaryRevision())
}

// ValidateForBinary checks the operator attestation against local build metadata.
// It cannot prove remote workers are stopped or authenticate the binary itself.
func (r EmissionReceipt) ValidateForBinary(identity string, now time.Time, maxAge time.Duration, revision string) error {
	if maxAge <= 0 {
		return fmt.Errorf("emission receipt maximum age must be positive")
	}
	if revision == "" {
		return fmt.Errorf("cannot attest BinaryRevision offline: this binary has no embedded revision available for attestation (dirty builds and Docker images built without .git cannot attest); rebuild with `make build` from a clean git checkout; see docs/research/adr-0008-4-evidence.md")
	}
	if r.BinaryRevision != revision {
		return fmt.Errorf("%w %s", ErrBinaryRevisionMismatch, revision)
	}
	if r.DrainedAt.Before(now.Add(-maxAge)) || r.RestoreTestedAt.Before(now.Add(-maxAge)) {
		return fmt.Errorf("stale emission receipt: drain and restore test must be within %s; repeat drain, backup and restore test", maxAge)
	}
	if identity == "" || r.DatabaseIdentity == "" || r.DatabaseIdentity != identity || r.BinaryRevision == "" || len(r.DrainedWorkers) == 0 || !r.CompatibleReadersOnly || r.DrainedAt.IsZero() || r.RestoreTestedAt.Before(r.DrainedAt) || r.RestoreTestedAt.After(now) || !IsDigest(r.BackupSHA256) {
		return fmt.Errorf("first-emission gate requires a database-bound drain inventory and restore-tested backup receipt; see docs/research/adr-0008-4-evidence.md")
	}
	seen := map[string]bool{}
	for _, worker := range r.DrainedWorkers {
		if worker == "" || seen[worker] {
			return fmt.Errorf("invalid drained worker inventory")
		}
		seen[worker] = true
	}
	f, err := os.Open(r.BackupPath)
	if err != nil {
		return fmt.Errorf("first-emission backup unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("first-emission backup must be a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("cannot hash first-emission backup")
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != r.BackupSHA256 {
		return fmt.Errorf("first-emission backup hash mismatch")
	}
	return nil
}
