package dependency

import (
	"encoding/json"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// SnapshotVersion is the v8 dependency wrapper, nested in composed audit cursors.
// queue.SnapshotVersion and legacy-only cursor writes are unchanged.
const SnapshotVersion = 8

type snapshot struct {
	Version        int                     `json:"Version"`
	Queue          json.RawMessage         `json:"Queue"`
	Initialization JournalInitialized      `json:"Initialization"`
	LastID         uint64                  `json:"LastID"`
	LastHash       string                  `json:"LastHash"`
	Declarations   []declarationProjection `json:"Declarations"`
	Policies       []policyProjection      `json:"Policies"`
	Observations   []observationProjection `json:"Observations"`
}

// EncodeSnapshot delegates byte-for-byte to the existing codec in the absence
// of dependency history. Initialized journals use v8, even with no active edges.
// It preserves #163 HeldRequests and cached PRObserved.PendingConditions inside
// the unchanged queue snapshot; v8 is not permission to erase either boundary.
func encodeSnapshotBase(q queue.State, s State) ([]byte, error) {
	legacy, err := queue.EncodeSnapshot(q)
	if err != nil {
		return nil, err
	}
	if s.initialization == nil {
		return legacy, nil
	}
	v := snapshot{SnapshotVersion, legacy, *s.initialization, s.lastID, s.lastHash, []declarationProjection{}, []policyProjection{}, []observationProjection{}}
	for _, d := range s.declarations {
		v.Declarations = append(v.Declarations, cloneDeclaration(d))
	}
	for _, p := range s.policies {
		v.Policies = append(v.Policies, p)
	}
	for _, o := range s.observations {
		v.Observations = append(v.Observations, o)
	}
	slices.SortFunc(v.Declarations, func(a, b declarationProjection) int { return compareID(a.EventID, b.EventID) })
	slices.SortFunc(v.Policies, func(a, b policyProjection) int { return compareID(a.EventID, b.EventID) })
	slices.SortFunc(v.Observations, func(a, b observationProjection) int { return compareID(a.EventID, b.EventID) })
	if _, err := v.state(*s.initialization); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSnapshotBytes {
		return nil, ErrLimit
	}
	return raw, nil
}
func compareID(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// DecodeSnapshot is composed by journalreader for compatible readers.
// Dependency state requires the caller's independently verified binding. Legacy
// bytes continue through the deployed reader, with no new identity read needed.
func DecodeSnapshot(raw []byte, expected JournalInitialized) (queue.State, State, error) {
	if len(raw) > maxSnapshotBytes {
		return queue.State{}, State{}, ErrLimit
	}
	var header struct{ Version int }
	if err := json.Unmarshal(raw, &header); err != nil {
		return queue.State{}, State{}, err
	}
	if header.Version == CompositionSnapshotVersion {
		var v compositionSnapshot
		if err := strictJSON(raw, &v); err != nil {
			return queue.State{}, State{}, err
		}
		q, s, err := DecodeSnapshot(v.Base, expected)
		if err != nil {
			return q, s, err
		}
		if v.LastID != s.lastID || v.Initialization != *s.initialization || len(v.Compositions) > 32 {
			return queue.State{}, State{}, ErrMalformed
		}
		s.compositions = map[string]compositionProjection{}
		for i, c := range v.Compositions {
			if c.Event.Validate() != nil || c.Event.Namespace != expected.Namespace || c.EventID == 0 || c.EventID > s.lastID || c.Event.StartEventID >= c.EventID || i > 0 && v.Compositions[i-1].EventID >= c.EventID {
				return queue.State{}, State{}, ErrMalformed
			}
			if _, exists := s.compositions[c.Event.BatchID]; exists {
				return queue.State{}, State{}, ErrConflict
			}
			b := q.Batch(c.Event.BatchID)
			if b == nil || b.BaseSHA != c.Event.BaseSHA || len(b.PRs) != len(c.Event.Members) {
				return queue.State{}, State{}, ErrStale
			}
			for j, m := range c.Event.Members {
				if m.Alias.validate(expected.scope()) != nil || m.Alias.Number != uint64(b.PRs[j].PR) || m.Head != b.PRs[j].HeadSHA {
					return queue.State{}, State{}, ErrStale
				}
			}
			s.compositions[c.Event.BatchID] = compositionProjection{c.EventID, cloneComposition(c.Event)}
		}
		return q, s, nil
	}
	if header.Version != SnapshotVersion {
		q, err := queue.DecodeSnapshot(raw)
		return q, State{}, err
	}
	var v snapshot
	if err := strictJSON(raw, &v); err != nil {
		return queue.State{}, State{}, err
	}
	q, err := queue.DecodeSnapshot(v.Queue)
	if err != nil {
		return queue.State{}, State{}, err
	}
	s, err := v.state(expected)
	if err != nil {
		return queue.State{}, State{}, err
	}
	return q, s, nil
}
func (v snapshot) state(expected JournalInitialized) (State, error) {
	if err := v.Initialization.VerifyBinding(expected); err != nil {
		return State{}, err
	}
	if v.Version != SnapshotVersion || v.LastID == 0 || !hexDigest(v.LastHash) || len(v.Declarations) > maxProjectionEntries || len(v.Policies) > maxProjectionEntries || len(v.Observations) > maxProjectionEntries*MaxDirectEdges {
		return State{}, ErrMalformed
	}
	s := State{initialization: &v.Initialization, lastID: v.LastID, lastHash: v.LastHash}.clone()
	used := map[uint64]bool{}
	checkID := func(id uint64) bool {
		if id == 0 || id > v.LastID || used[id] {
			return false
		}
		used[id] = true
		return true
	}
	for _, p := range v.Policies {
		if !checkID(p.EventID) || p.Event.Validate() != nil || p.Event.Namespace != v.Initialization.Namespace || p.Event.Alias.validate(v.Initialization.scope()) != nil || p.Event.PriorEventID >= p.EventID {
			return State{}, ErrMalformed
		}
		if _, ok := s.policies[p.Event.key()]; ok {
			return State{}, ErrConflict
		}
		s.policies[p.Event.key()] = p
	}
	changes := map[string]bool{}
	for i, d := range v.Declarations {
		if i > 0 && v.Declarations[i-1].EventID >= d.EventID {
			return State{}, ErrMalformed
		}
		if !checkID(d.EventID) || d.Event.Validate() != nil || d.Event.Namespace != v.Initialization.Namespace || d.Event.Alias.validate(v.Initialization.scope()) != nil || !sortedIDs(d.Editors) || d.Event.Prior.EventID >= d.EventID || d.Unknown && !d.Held || changes[d.Event.ChangeID] {
			return State{}, ErrMalformed
		}
		changes[d.Event.ChangeID] = true
		for _, edge := range d.Event.Edges {
			if _, err := ParseReference(edge.Reference, v.Initialization.scope()); err != nil {
				return State{}, err
			}
		}
		if _, ok := s.declarations[d.Event.Alias.key()]; ok {
			return State{}, ErrConflict
		}
		expectedHeld := d.Event.Hold.Held
		if d.Unknown != d.Event.Hold.Unknown || !slices.Equal(d.Editors, d.Event.Hold.Editors) {
			return State{}, ErrHeld
		}
		if a := d.Ack; a != nil {
			if !checkID(a.EventID) || a.Event.Validate() != nil || a.Governing.Validate() != nil || a.Event.Namespace != v.Initialization.Namespace || a.Governing.Namespace != v.Initialization.Namespace || a.Governing.Alias.RepositoryID != d.Event.Alias.RepositoryID || a.Event.GoverningCommit != a.Governing.Commit || a.Event.PolicyDigest != a.Governing.Digest || a.Event.Alias != d.Event.Alias || a.Event.Declaration.EventID >= a.EventID || a.Event.PolicyEventID >= a.EventID {
				return State{}, ErrMalformed
			}
			switch a.Kind {
			case "dependency_declaration_acknowledged":
				if a.Revoked || len(a.Overridden) > 0 || slices.Contains(a.Event.Exclusions, a.Event.Writer.SubjectID) {
					return State{}, ErrHeld
				}
			case "dependency_declaration_self_acknowledged":
				if !a.Governing.selfAckEnabled() || (SelfAcknowledged{a.Event, a.Overridden}).Validate() != nil {
					return State{}, ErrHeld
				}
				if !a.Revoked && !s.policy(d.Event).Event.selfAckEnabled() {
					return State{}, ErrHeld
				}
			default:
				return State{}, ErrMalformed
			}
			if a.EventID > d.EventID {
				if a.Event.Declaration != (Basis{d.EventID, d.Event.Digest}) || d.Unknown || !slices.Equal(a.Event.Exclusions, exclusions(d)) {
					return State{}, ErrHeld
				}
				expectedHeld = false
			}
			if a.Revoked {
				expectedHeld = true
			}
		}
		if d.Held != expectedHeld {
			return State{}, ErrHeld
		}
		s.declarations[d.Event.Alias.key()] = cloneDeclaration(d)
	}
	for _, o := range v.Observations {
		e := o.Event
		d, ok := s.declarations[e.Alias.key()]
		if !checkID(o.EventID) || !ok || e.Validate() != nil || e.Namespace != v.Initialization.Namespace || e.ExpectedPrior >= o.EventID || e.Declaration != s.Prior(e.Alias) || e.ChangeID != d.Event.ChangeID || e.Revision != d.Event.Revision || !slices.ContainsFunc(d.Event.Edges, func(edge DeclaredEdge) bool { return edge.Reference == e.Source }) {
			return State{}, ErrStale
		}
		if _, ok := s.observations[e.key()]; ok {
			return State{}, ErrConflict
		}
		s.observations[e.key()] = o
	}
	return s, nil
}

// CompositionSnapshotVersion extends derived dependency state without changing
// deployed v8 bytes. Once composition history exists the base remains v10, even
// after retirement; compatible rollback cannot be inferred from an empty map.
const CompositionSnapshotVersion = 10

type compositionSnapshot struct {
	Version        int                     `json:"Version"`
	Base           json.RawMessage         `json:"Base"`
	Initialization JournalInitialized      `json:"Initialization"`
	LastID         uint64                  `json:"LastID"`
	Compositions   []compositionProjection `json:"Compositions"`
}

func EncodeSnapshot(q queue.State, s State) ([]byte, error) {
	base, err := encodeSnapshotBase(q, s)
	if err != nil || s.compositions == nil {
		return base, err
	}
	v := compositionSnapshot{CompositionSnapshotVersion, base, *s.initialization, s.lastID, []compositionProjection{}}
	for _, c := range s.compositions {
		v.Compositions = append(v.Compositions, compositionProjection{c.EventID, cloneComposition(c.Event)})
	}
	slices.SortFunc(v.Compositions, func(a, b compositionProjection) int { return compareID(a.EventID, b.EventID) })
	raw, err := json.Marshal(v)
	if len(raw) > maxSnapshotBytes {
		return nil, ErrLimit
	}
	return raw, err
}
