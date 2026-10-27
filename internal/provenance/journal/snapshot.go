package journal

import (
	"encoding/json"
	"fmt"
	"slices"
)

// SnapshotVersion reserves v9, leaving dependency's explicit v8 unchanged.
// A v9 Base is exactly one legacy queue snapshot OR dependency v8 wrapper.
// Slice 3c can compose one reader for both via BaseDecoder, without introducing
// a production dependency import or teaching old decoders to ignore evidence.
const SnapshotVersion = 9

// BaseDecoder strictly validates an unchanged legacy/dependency snapshot. The
// caller owns the concrete state and independently verified dependency identity.
// It must not perform I/O; readers must never resolve evidence during replay.
type BaseDecoder func([]byte) error

// Snapshot returns retained provenance and the exact base bytes. There is no
// provenance snapshot encoder in this slice, nor any new snapshot writes.
type Snapshot struct {
	Base       json.RawMessage
	Provenance State
}

type snapshot struct {
	Version int             `json:"Version"`
	Base    json.RawMessage `json:"Base"`
	LastID  uint64          `json:"LastID"`
	Records []Record        `json:"Records"`
}

// DecodeSnapshot is explicit and UNWIRED. It accepts legacy/v8 bytes through the
// supplied decoder and v9 through a strict retained-evidence fold. Nested v9
// wrappers and unrecognized base versions are rejected before invoking callbacks.
func DecodeSnapshot(raw []byte, decodeBase BaseDecoder) (Snapshot, error) {
	if len(raw) > maxSnapshotBytes {
		return Snapshot{}, ErrLimit
	}
	if decodeBase == nil {
		return Snapshot{}, ErrMalformed
	}
	var header struct{ Version int }
	if err := json.Unmarshal(raw, &header); err != nil {
		return Snapshot{}, err
	}
	base := raw
	var state State
	if header.Version == SnapshotVersion {
		var v snapshot
		if err := strictJSON(raw, &v); err != nil {
			return Snapshot{}, err
		}
		if len(v.Records) == 0 || len(v.Records) > maxRecords {
			return Snapshot{}, ErrMalformed
		}
		for _, r := range v.Records {
			e, err := DecodeEvent(r.Payload)
			if err != nil {
				return Snapshot{}, err
			}
			state, err = Apply(state, r.ID, e)
			if err != nil {
				return Snapshot{}, err
			}
		}
		if v.LastID != v.Records[len(v.Records)-1].ID {
			return Snapshot{}, ErrBinding
		}
		base = v.Base
	}
	var baseHeader struct{ Version int }
	if err := json.Unmarshal(base, &baseHeader); err != nil {
		return Snapshot{}, err
	}
	if baseHeader.Version < 2 || baseHeader.Version > 8 {
		return Snapshot{}, fmt.Errorf("unsupported base snapshot version %d", baseHeader.Version)
	}
	if err := decodeBase(slices.Clone(base)); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{slices.Clone(base), state}, nil
}
