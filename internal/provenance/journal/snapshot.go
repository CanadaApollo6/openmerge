package journal

import (
	"bytes"
	"encoding/base64"
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

// A separately discriminated shape preserves both earlier v9 formats. The
// table expands to the same exact checkpoint and uses its existing validation.
type tableSnapshot struct {
	Version    int             `json:"Version"`
	BaseKind   string          `json:"BaseKind"`
	Base       json.RawMessage `json:"Base"`
	LastID     uint64          `json:"LastID"`
	Records    []Record        `json:"Records"`
	FenceTable string          `json:"FenceTable"`
}

// recoverySnapshot is a distinct strict v9 shape. Earlier readers reject its
// Awaiting discriminator; snapshots without pending recovery keep their bytes.
type recoverySnapshot struct {
	Version    int             `json:"Version"`
	BaseKind   string          `json:"BaseKind"`
	Base       json.RawMessage `json:"Base"`
	LastID     uint64          `json:"LastID"`
	Records    []Record        `json:"Records"`
	FenceTable string          `json:"FenceTable"`
	Awaiting   []fence         `json:"Awaiting"`
}

// DecodeSnapshot accepts legacy/v8 bytes through the
// supplied decoder and v9 through a strict retained-evidence fold. Nested v9
// wrappers and unrecognized base versions are rejected before invoking callbacks.
// V9 record payloads must already be canonical; replay preserves their exact bytes.
func DecodeSnapshot(raw []byte, decodeBase BaseDecoder, validate ...func(uint64, Landing) error) (Snapshot, error) {
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
	var records []Record
	var compacted *checkpoint
	var awaiting []fence
	if header.Version == SnapshotVersion {
		var discriminator map[string]json.RawMessage
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return Snapshot{}, err
		}
		var v snapshot
		if _, ok := discriminator["FenceTable"]; ok {
			var tv tableSnapshot
			if _, pending := discriminator["Awaiting"]; pending {
				var rv recoverySnapshot
				if err := strictJSON(raw, &rv); err != nil {
					return Snapshot{}, err
				}
				if len(rv.Awaiting) == 0 {
					return Snapshot{}, ErrBinding
				}
				awaiting = rv.Awaiting
				tv = tableSnapshot{rv.Version, rv.BaseKind, rv.Base, rv.LastID, rv.Records, rv.FenceTable}
			} else if err := strictJSON(raw, &tv); err != nil {
				return Snapshot{}, err
			}
			table, err := base64.StdEncoding.Strict().DecodeString(tv.FenceTable)
			if err != nil || base64.StdEncoding.EncodeToString(table) != tv.FenceTable {
				return Snapshot{}, ErrMalformed
			}
			c, err := unpackFences(table, tv.LastID)
			if err != nil {
				return Snapshot{}, err
			}
			v = snapshot{tv.Version, tv.BaseKind, tv.Base, tv.LastID, tv.Records}
			compacted = &c
		} else if _, ok := discriminator["Compaction"]; ok {
			var cv compactSnapshot
			if err := strictJSON(raw, &cv); err != nil {
				return Snapshot{}, err
			}
			v = snapshot{cv.Version, cv.BaseKind, cv.Base, cv.LastID, cv.Records}
			compacted = &cv.Compaction
			if cv.LastID != cv.Compaction.Through {
				return Snapshot{}, ErrBinding
			}
		} else if err := strictJSON(raw, &v); err != nil {
			return Snapshot{}, err
		}
		if len(v.Records) > maxRecords {
			return Snapshot{}, RetentionLimitError{Records: len(v.Records), Bytes: len(raw)}
		}
		if compacted == nil {
			var last uint64
			if len(v.Records) > 0 {
				last = v.Records[len(v.Records)-1].ID
			}
			if v.LastID != last {
				return Snapshot{}, ErrBinding
			}
		}
		records = v.Records
		base, baseKind = v.Base, v.BaseKind
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
		if (baseHeader.Version == 8 || baseHeader.Version == 10) {
			baseKind = "dependency"
		}
	}
	if !supportedBase(baseKind, baseHeader.Version) {
		return Snapshot{}, fmt.Errorf("unsupported %s base snapshot version %d", baseKind, baseHeader.Version)
	}
	if err := decodeBase(baseKind, slices.Clone(base)); err != nil {
		return Snapshot{}, err
	}
	if compacted != nil {
		var err error
		state, err = restoreCheckpoint(*compacted, records, awaiting)
		if err != nil {
			return Snapshot{}, err
		}
	}
	for _, record := range records {
		e, err := DecodeEvent(record.Payload)
		if err != nil {
			return Snapshot{}, err
		}
		if compacted == nil {
			state, err = Apply(state, record.ID, e)
			if err != nil {
				return Snapshot{}, err
			}
		}
		if l, ok := e.(Landing); ok {
			for _, check := range validate {
				if err := check(record.ID, l); err != nil {
					return Snapshot{}, err
				}
			}
		}
		_, canonical, err := EncodeEvent(e)
		if err != nil || !bytes.Equal(record.Payload, canonical) {
			return Snapshot{}, ErrMalformed
		}
	}
	return Snapshot{BaseKind: baseKind, Base: slices.Clone(base), Provenance: state}, nil
}

func supportedBase(kind string, version int) bool {
	switch kind {
	case "queue":
		return slices.Contains([]int{2, 3, 4, 5, 6, 7}, version)
	case "dependency":
		return version == 8 || version == 10
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
	if err := state.fenceTableCapacity(); err != nil {
		return nil, err
	}
	records := state.Records()
	var lastID uint64
	if len(records) > 0 {
		lastID = records[len(records)-1].ID
	}
	var value any = snapshot{SnapshotVersion, kind, base, lastID, records}
	if state.compacted() {
		value = compactSnapshot{SnapshotVersion, kind, base, state.lastID, records, state.checkpoint()}
	}
	if len(state.retired)+len(state.settled) >= 128 || len(state.awaiting) != 0 {
		table := state.packedTable
		if table == nil || state.packedAt != state.fenceBytes {
			table = packFences(state.checkpoint())
		}
		if state.bytes+state.awaitingBytes+4*((len(table)+2)/3)+130 > maxSnapshotBytes/2 {
			return nil, RetentionLimitError{Records: len(records), Bytes: state.bytes + state.awaitingBytes + 4*((len(table)+2)/3) + 130}
		}
		value = tableSnapshot{SnapshotVersion, kind, base, state.lastID, records, base64.StdEncoding.EncodeToString(table)}
		if len(state.awaiting) != 0 {
			value = recoverySnapshot{SnapshotVersion, kind, base, state.lastID, records, base64.StdEncoding.EncodeToString(table), sortedFences(state.awaiting)}
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSnapshotBytes {
		return nil, ErrLimit
	}
	return raw, nil
}
