package journalreader

import (
	"encoding/json"
	"fmt"

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
	})
	if err != nil {
		return err
	}
	next.Provenance = decoded.Provenance
	for _, record := range next.Provenance.Records() {
		if record.ID > through {
			return ErrReference
		}
		e, err := pj.DecodeEvent(record.Payload)
		if err != nil {
			return err
		}
		if l, ok := e.(pj.Landing); ok {
			if err := next.validateLanding(record.ID, l); err != nil {
				return fmt.Errorf("snapshot event %d: %w", record.ID, err)
			}
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
