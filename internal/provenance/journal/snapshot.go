package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// SnapshotVersion reserves v9, leaving dependency's explicit v8 unchanged.
// A v9 BaseKind discriminates an unchanged queue snapshot from a dependency
// wrapper. Kind-specific version allowlists must be updated for future readers.
// The shared journalreader composes both via BaseDecoder, without teaching
// old decoders to ignore evidence.
const SnapshotVersion = 9

// BaseDecoder strictly validates an unchanged legacy/dependency snapshot. The
// caller owns the concrete state and independently verified dependency identity.
// Kind is "queue" or "dependency" and selects the decoder independently of its
// version namespace. Unwrapped legacy input uses the frozen pre-v9 version map.
// It must not perform I/O or acquire mutable external evidence during replay.
type BaseDecoder func(kind string, raw []byte) error

// Snapshot returns retained provenance and the exact base bytes.
type Snapshot struct {
	BaseKind   string
	Base       json.RawMessage
	Provenance State
}

type snapshot struct {
	Version  int             `json:"Version"`
	BaseKind string          `json:"BaseKind"`
	Base     json.RawMessage `json:"Base"`
	LastID   uint64          `json:"LastID"`
	Records  []Record        `json:"Records"`
}

// DecodeSnapshot accepts legacy/v8 bytes through the
// supplied decoder and v9 through a strict retained-evidence fold. Nested v9
// wrappers and unrecognized base versions are rejected before invoking callbacks.
// V9 record payloads must already be canonical; replay preserves their exact bytes.
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
	baseKind := ""
	var state State
	if header.Version == SnapshotVersion {
		var v snapshot
		if err := strictJSON(raw, &v); err != nil {
			return Snapshot{}, err
		}
		if len(v.Records) > maxRecords {
			return Snapshot{}, RetentionLimitError{Records: len(v.Records), Bytes: len(raw)}
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
			canonical := state.records[len(state.records)-1].Payload
			if !bytes.Equal(r.Payload, canonical) {
				return Snapshot{}, ErrMalformed
			}
		}
		var lastID uint64
		if len(v.Records) > 0 {
			lastID = v.Records[len(v.Records)-1].ID
		}
		if v.LastID != lastID {
			return Snapshot{}, ErrBinding
		}
		base = v.Base
		baseKind = v.BaseKind
		if baseKind != "queue" && baseKind != "dependency" {
			return Snapshot{}, ErrMalformed
		}
	}
	var baseHeader struct{ Version int }
	if err := json.Unmarshal(base, &baseHeader); err != nil {
		return Snapshot{}, err
	}
	// Legacy unwrapped input has no discriminator. V9 always supplies one, so
	// future queue and dependency versions can overlap without ambiguity.
	if baseKind == "" {
		baseKind = "queue"
		if baseHeader.Version == 8 {
			baseKind = "dependency"
		}
	}
	if !supportedBase(baseKind, baseHeader.Version) {
		return Snapshot{}, fmt.Errorf("unsupported %s base snapshot version %d", baseKind, baseHeader.Version)
	}
	if err := decodeBase(baseKind, slices.Clone(base)); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{BaseKind: baseKind, Base: slices.Clone(base), Provenance: state}, nil
}

func supportedBase(kind string, version int) bool {
	switch kind {
	case "queue":
		return slices.Contains([]int{2, 3, 4, 5, 6, 7}, version)
	case "dependency":
		return version == 8
	default:
		return false
	}
}

// EncodeSnapshot composes a derived v9 cursor from a validated base and fold.
// Empty provenance uses LastID zero, including dependency-only cursors and a
// writer's required format upgrade before its first side-evidence append.
func EncodeSnapshot(kind string, base []byte, state State) ([]byte, error) {
	var header struct{ Version int }
	if err := json.Unmarshal(base, &header); err != nil {
		return nil, err
	}
	if !supportedBase(kind, header.Version) {
		return nil, ErrMalformed
	}
	records := state.Records()
	var lastID uint64
	if len(records) > 0 {
		lastID = records[len(records)-1].ID
	}
	raw, err := json.Marshal(snapshot{SnapshotVersion, kind, base, lastID, records})
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSnapshotBytes {
		return nil, ErrLimit
	}
	return raw, nil
}
