package authority

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationalReceipts(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup")
	data := []byte("scratch restore fixture")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	valid := EmissionReceipt{DatabaseIdentity: "source", BinaryRevision: "new", DrainedWorkers: []string{"host/worker", "other/admin-reader"}, DrainedAt: now.Add(-time.Minute), BackupPath: path, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), RestoreTestedAt: now, CompatibleReadersOnly: true}
	completeEmissionOperations(&valid)
	for _, tc := range []struct {
		name   string
		mutate func(*EmissionReceipt)
	}{
		{"complete", func(*EmissionReceipt) {}},
		{"inventory absent", func(r *EmissionReceipt) { r.Inventory = nil }},
		{"inventory incomplete", func(r *EmissionReceipt) { r.Inventory.Complete = false }},
		{"host omitted", func(r *EmissionReceipt) { r.Inventory.Workers = r.Inventory.Workers[:1] }},
		{"duplicate worker", func(r *EmissionReceipt) { r.Inventory.Workers[1] = r.Inventory.Workers[0] }},
		{"unlisted reader", func(r *EmissionReceipt) { r.Inventory.Workers[1].ID = "other/strict-v7-reader" }},
		{"running worker", func(r *EmissionReceipt) { r.Inventory.Workers[0].StoppedAt = time.Time{} }},
		{"late stop", func(r *EmissionReceipt) { r.Inventory.Workers[0].StoppedAt = now }},
		{"unknown binary", func(r *EmissionReceipt) { r.Inventory.Workers[0].BinaryRevision = "" }},
		{"restore absent", func(r *EmissionReceipt) { r.Reconciliation = nil }},
		{"wrong source", func(r *EmissionReceipt) { r.Reconciliation.SourceDatabaseIdentity = "other" }},
		{"restore on source", func(r *EmissionReceipt) { r.Reconciliation.RestoredDatabaseIdentity = r.DatabaseIdentity }},
		{"wrong backup", func(r *EmissionReceipt) { r.Reconciliation.BackupSHA256 = fmt.Sprintf("%064d", 0) }},
		{"before drain", func(r *EmissionReceipt) { r.Reconciliation.BackupCapturedAt = r.DrainedAt.Add(-time.Second) }},
		{"restore watermark differs", func(r *EmissionReceipt) { r.Reconciliation.RestoredWatermark++ }},
		{"reconciliation omitted", func(r *EmissionReceipt) { r.Reconciliation.AllRefsAndEvidenceReconciled = false }},
		{"missing evidence", func(r *EmissionReceipt) { r.Reconciliation.EvidenceSHA256 = "" }},
		{"reconciliation before restore", func(r *EmissionReceipt) { r.Reconciliation.ReconciledAt = r.RestoreTestedAt.Add(-time.Second) }},
		{"future reconciliation", func(r *EmissionReceipt) { r.Reconciliation.ReconciledAt = now.Add(time.Second) }},
		{"stale receipt", func(r *EmissionReceipt) { r.DrainedAt = now.Add(-48 * time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var receipt EmissionReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&receipt)
			err := receipt.ValidateForBinary("source", now, time.Hour, "new")
			if (err == nil) != (tc.name == "complete") {
				t.Fatalf("gate result: %v", err)
			}
		})
	}
}
