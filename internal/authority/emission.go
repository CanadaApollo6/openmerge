package authority

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"time"
)

// EmissionReceipt is retained by the operator with the pre-emission backup.
// This is an explicit operator attestation, not automatic worker discovery.
// DatabaseIdentity is the credential-free connection identity printed by setup.
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
// Receipt failures precede DB access and seeing reads. Operators inventory all
// hosts, stop old workers, capture a backup, restore-test it, then attest that
// only compatible readers may restart. Period B permits isolated sandboxes only.
func (r EmissionReceipt) Validate(identity string, now time.Time) error {
	if r.DatabaseIdentity != identity || r.BinaryRevision == "" || len(r.DrainedWorkers) == 0 || !r.CompatibleReadersOnly || r.DrainedAt.IsZero() || r.RestoreTestedAt.Before(r.DrainedAt) || r.RestoreTestedAt.After(now) || !IsDigest(r.BackupSHA256) {
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
