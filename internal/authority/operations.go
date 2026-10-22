package authority

import (
	"errors"
	"strings"
	"time"
)

// WorkerInventory includes every host/process that can read or write the source
// journal, including admin tools, stopped services and strict read_error/v7
// decoders. Exhaustiveness is an operator attestation, not host discovery.
type WorkerInventory struct {
	Complete bool            `json:"complete"`
	Workers  []StoppedWorker `json:"workers"`
}

// StoppedWorker identifies one inventoried process and its confirmed stop time.
type StoppedWorker struct {
	ID             string    `json:"id"`
	BinaryRevision string    `json:"binary_revision"`
	StoppedAt      time.Time `json:"stopped_at"`
}

// RestoreReconciliation records a tested restore and deliberate reconciliation
// of post-backup refs/evidence before resumption. An empty change set still
// requires a retained evidence digest and explicit all-refs/evidence attestation.
type RestoreReconciliation struct {
	SourceDatabaseIdentity       string    `json:"source_database_identity"`
	RestoredDatabaseIdentity     string    `json:"restored_database_identity"`
	BackupSHA256                 string    `json:"backup_sha256"`
	BackupCapturedAt             time.Time `json:"backup_captured_at"`
	BackupWatermark              int64     `json:"backup_watermark"`
	RestoredWatermark            int64     `json:"restored_watermark"`
	ReconciledAt                 time.Time `json:"reconciled_at"`
	EvidenceSHA256               string    `json:"evidence_sha256"`
	AllRefsAndEvidenceReconciled bool      `json:"all_refs_and_evidence_reconciled"`
}

func (r EmissionReceipt) validateOperations(now time.Time) error {
	if r.Inventory == nil || !r.Inventory.Complete || len(r.Inventory.Workers) != len(r.DrainedWorkers) {
		return errors.New("complete all-host stopped worker inventory required")
	}
	workers := map[string]bool{}
	for _, worker := range r.DrainedWorkers {
		workers[worker] = true
	}
	for _, worker := range r.Inventory.Workers {
		if !workers[worker.ID] || strings.TrimSpace(worker.BinaryRevision) == "" || worker.StoppedAt.IsZero() || worker.StoppedAt.After(r.DrainedAt) {
			return errors.New("worker inventory missing, duplicate or not stopped before drain")
		}
		delete(workers, worker.ID)
	}
	proof := r.Reconciliation
	if proof == nil || proof.SourceDatabaseIdentity != r.DatabaseIdentity || proof.RestoredDatabaseIdentity == "" || proof.RestoredDatabaseIdentity == r.DatabaseIdentity || proof.BackupSHA256 != r.BackupSHA256 || proof.BackupCapturedAt.Before(r.DrainedAt) || proof.BackupCapturedAt.After(r.RestoreTestedAt) || proof.BackupWatermark <= 0 || proof.RestoredWatermark != proof.BackupWatermark || proof.ReconciledAt.Before(r.RestoreTestedAt) || proof.ReconciledAt.After(now) || !proof.AllRefsAndEvidenceReconciled || !IsDigest(proof.EvidenceSHA256) {
		return errors.New("restore-tested backup and complete ref/evidence reconciliation receipt required")
	}
	return nil
}
