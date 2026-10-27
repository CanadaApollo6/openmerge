package journalreader

import (
	"encoding/json"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// LoadSnapshot reads legacy/v8/v9 through the one v9 BaseKind dispatcher. Through
// is the containing cursor's journal watermark, including intervening queue IDs.
// Legacy format asserts a side-evidence-free prefix; v8/v9 retain composed state.
func (r *Reader) LoadSnapshot(raw []byte, through uint64) error {
	next := New(queue.State{}, r.expected, r.lookup)
	decoded, err := pj.DecodeSnapshot(raw, func(kind string, base []byte) error {
		expected := r.expected
		if kind == "dependency" {
			var header struct {
				Initialization dependency.JournalInitialized
				LastID         uint64
			}
			if err := json.Unmarshal(base, &header); err != nil {
				return err
			}
			if header.LastID > through {
				return ErrReference
			}
			// Structural decoding is distinct from trust. Without startup binding this
			// remains quarantined by Dependencies, just like full journal replay.
			if expected == (dependency.JournalInitialized{}) {
				expected = header.Initialization
			}
		}
		var err error
		next.Queue, next.dependencies, err = dependency.DecodeSnapshot(base, expected)
		return err
	}, func(id uint64, l pj.Landing) error {
		if id > through {
			return ErrReference
		}
		return next.validateLanding(id, l)
	})
	if err != nil {
		return err
	}
	if decoded.Provenance.Through() > through {
		return ErrReference
	}
	next.Provenance = decoded.Provenance
	for _, record := range next.Provenance.Records() {
		if record.ID > through {
			return ErrReference
		}

	}
	var header struct{ Version int }
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	next.composed = header.Version >= 8
	next.lastID = through
	*r = next
	return nil
}

// CursorSnapshot preserves the deployed queue bytes for a legacy-only prefix.
// After side evidence (or a new-format load), v9 retains both projections so the
// next poll resumes the suffix. This does not enable journal/snapshot-table writes.
func (r Reader) CursorSnapshot() ([]byte, error) {
	if !r.composed {
		return queue.EncodeSnapshot(r.Queue)
	}
	base, err := dependency.EncodeSnapshot(r.Queue, r.dependencies)
	if err != nil {
		return nil, err
	}
	kind := "queue"
	var header struct{ Version int }
	if err := json.Unmarshal(base, &header); err != nil {
		return nil, err
	}
	if header.Version == dependency.SnapshotVersion {
		kind = "dependency"
	}
	return pj.EncodeSnapshot(kind, base, r.Provenance)
}

// UpgradeCursor makes the prefix invariant explicit even before any side record.
func (r *Reader) UpgradeCursor() { r.composed = true }

// Preflight checks the complete candidate projection before any durable insert.
func (r *Reader) Preflight(highWater ...int) error {
	return r.Provenance.Preflight(highWater...)
}
