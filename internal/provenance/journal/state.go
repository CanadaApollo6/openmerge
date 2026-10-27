package journal

import (
	"encoding/json"
	"maps"
	"slices"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
)

// Record retains a journal position and exact canonical new-event payload.
// Positions may have gaps occupied by queue/dependency events.
type Record struct {
	ID      uint64          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

type group struct {
	header  Observation
	commits []p.Commit
	ids     []uint64
}

// State is a retained-evidence fold, not a queue authorization API.
// It keeps bounded active records; audit selection uses immutable journal rows.
type State struct {
	records       []Record
	groups        map[string]group
	latest        map[string]string
	landings      map[string]Landing
	bytes         int
	lastID        uint64
	retired       map[string]uint64 // generation key -> original chunk zero
	settled       map[string]uint64 // attempt -> landed or terminal tested companion ID
	landingIDs    map[string]uint64
	fenceBytes    int
	packedAt      int               // uncompressed fence bytes at the last capacity measurement
	packedTable   []byte            // immutable cached compressed table at packedAt
	packedBytes   int               // measured JSON/base64 table bytes
	awaiting      map[string]uint64 // attempt -> accepted Landed outcome awaiting companion
	awaitingBytes int
	packingWork   uint64 // entries visited by capacity measurements
}

func changeKey(r p.InventoryRequest) string {
	return stringJSON([]string{r.Repository, r.Queue, r.ChangeID})
}
func generationKey(r p.InventoryRequest) string {
	return stringJSON([]string{r.Repository, r.Queue, r.ChangeID, r.Generation})
}
func stringJSON(v any) string     { b, _ := json.Marshal(v); return string(b) }
func attemptKey(e Landing) string { return stringJSON([]string{e.AttemptID, e.Stage}) }

// Records returns detached active records. It is not an audit-export source;
// settled and superseded original evidence remains in the immutable journal.
func (s State) Records() []Record {
	out := make([]Record, len(s.records))
	for i, r := range s.records {
		out[i] = Record{r.ID, slices.Clone(r.Payload)}
	}
	return out
}

// Inventory returns detached, fully assembled evidence for an exact generation.
// The bool is false for missing chunks and pending/terminal acquisition failures.
func (s State) Inventory(r p.InventoryRequest) (Inventory, bool) {
	g, ok := s.groups[generationKey(r)]
	if !ok || !same(g.header.Request, r) {
		return Inventory{}, false
	}
	var v Inventory
	raw, _ := json.Marshal(Inventory{g.header, g.commits})
	_ = json.Unmarshal(raw, &v)
	return v, len(g.ids) == g.header.Chunks && g.header.Complete
}

// Apply resolves all inventory chunks and exact member references with no I/O.
// New generations compare against PreviousObservationID (the previous chunk 0),
// including revisions/re-pushes, base rewrites and incomplete observations.
// A rejected event leaves the old state byte-for-byte unchanged.
// DecisionID and layer References are only fenced to earlier positions here;
// slice 3c's composed reader must resolve their queue/dependency event targets.
func Apply(s State, id uint64, e Event) (State, error) {
	s.retired = maps.Clone(s.retired)
	s.settled = maps.Clone(s.settled)
	s.latest = maps.Clone(s.latest)
	s.awaiting = maps.Clone(s.awaiting)
	return ApplyOwned(s, id, e)
}

// ApplyOwned transfers ownership of s to the result on success. A failed apply
// leaves s unchanged. Replay readers own their fold and avoid copying lifetime
// fences per event; callers retaining old states must use Apply instead.
func ApplyOwned(s State, id uint64, e Event) (_ State, applyErr error) {
	_, raw, err := EncodeEvent(e)
	if err != nil {
		return s, err
	}
	if id == 0 || id <= s.lastID {
		return s, ErrBinding
	}
	// Detach all caller-owned slices, including nested gate inputs and mappings.
	e, err = DecodeEvent(raw)
	if err != nil {
		return s, err
	}
	next := s
	next.awaiting = maps.Clone(s.awaiting)
	if next.retired == nil {
		next.retired = map[string]uint64{}
	}
	next.landingIDs = maps.Clone(s.landingIDs)
	if next.landingIDs == nil {
		next.landingIDs = map[string]uint64{}
	}
	if next.settled == nil {
		next.settled = map[string]uint64{}
	}
	next.records = slices.Clone(s.records)
	next.groups = maps.Clone(s.groups)
	if next.groups == nil {
		next.groups = map[string]group{}
	}
	if next.latest == nil {
		next.latest = map[string]string{}
	}
	next.landings = maps.Clone(s.landings)
	if next.landings == nil {
		next.landings = map[string]Landing{}
	}
	var undo []func()
	defer func() {
		if applyErr != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	put := func(m map[string]uint64, key string, value uint64) {
		old, exists := m[key]
		undo = append(undo, func() {
			if exists {
				m[key] = old
			} else {
				delete(m, key)
			}
		})
		m[key] = value
	}
	switch v := e.(type) {
	case Observation:
		key, ck := generationKey(v.Request), changeKey(v.Request)
		if _, retired := s.retired[key]; retired {
			return s, ErrBinding
		}
		g, exists := s.groups[key]
		if !exists {
			if v.Index != 0 {
				return s, ErrIncomplete
			}
			prior := s.retired[s.latest[ck]]
			if old, ok := s.groups[s.latest[ck]]; ok {
				prior = old.ids[0]
			}
			if v.PreviousObservationID != prior {
				return s, ErrBinding
			}
			g = group{header: v, commits: []p.Commit{}, ids: []uint64{}}
			g.header.Commits = []p.Commit{}
			g.header.Index = 0
			old, exists := next.latest[ck]
			undo = append(undo, func() {
				if exists {
					next.latest[ck] = old
				} else {
					delete(next.latest, ck)
				}
			})
			next.latest[ck] = key
		} else {
			header := v
			header.Commits = []p.Commit{}
			header.Index = 0
			if s.latest[ck] != key || !same(header, g.header) || v.Index != len(g.ids) {
				return s, ErrBinding
			}
		}
		g.commits = append(slices.Clone(g.commits), v.Commits...)
		g.ids = append(slices.Clone(g.ids), id)
		if len(g.commits) > v.Limits.SourceCommits {
			return s, ErrLimit
		}
		if len(g.ids) == v.Chunks {
			if err := validateInventory(g); err != nil {
				return s, err
			}
		}
		next.groups[key] = g
	case Landing:
		if count := ReferenceCount(v); count > MaxLandingReferences {
			return s, RetentionLimitError{Records: len(s.records) + 1, Bytes: s.bytes + len(raw), References: count}
		}
		if _, settled := s.settled[v.AttemptID]; settled {
			return s, ErrBinding
		}
		if v.DecisionID >= id {
			return s, ErrBinding
		}
		for _, m := range v.Members {
			for _, l := range m.Layers {
				for _, ref := range l.References {
					if ref >= id {
						return s, ErrBinding
					}
				}
			}
		}
		if _, exists := s.landings[attemptKey(v)]; exists {
			return s, ErrBinding
		}
		if _, tested := s.landings[stringJSON([]string{v.AttemptID, "tested"})]; v.Stage == "landed" && !tested {
			return s, ErrBinding
		}
		for _, m := range v.Members {
			key := generationKey(m.Request)
			if _, retired := s.retired[key]; retired {
				return s, ErrBinding
			}
			g, exists := s.groups[key]
			if !exists || s.latest[changeKey(m.Request)] != key || !same(g.header.Request, m.Request) || !g.header.Complete || len(g.ids) != g.header.Chunks {
				return s, ErrIncomplete
			}
			if !slices.Equal(g.ids, m.ObservationIDs) || m.InventoryDigest != g.header.Digest || m.DeclarationDigest != g.header.DeclarationDigest || m.Mapping.PolicyID != g.header.ParserID {
				return s, ErrBinding
			}
			if m.Mapping.Complete {
				if len(m.Mapping.Commits) != len(g.commits) {
					return s, ErrBinding
				}
				for i, c := range g.commits {
					if m.Mapping.Commits[i].Original != c.ObjectID || m.Mapping.Commits[i].Build == m.Mapping.Build.Marker {
						return s, ErrBinding
					}
				}
			}
			if tested, ok := s.landings[stringJSON([]string{v.AttemptID, "tested"})]; ok && v.Stage == "landed" {
				if tested.BatchID != v.BatchID || len(tested.Members) != len(v.Members) {
					return s, ErrBinding
				}
				matched := false
				for _, tm := range tested.Members {
					if changeKey(tm.Request) == changeKey(m.Request) {
						matched = true
						if tm.Request.Revision != m.Request.Revision || tm.Request.Head != m.Request.Head || tm.Mapping.Build.ID == m.Mapping.Build.ID {
							return s, ErrBinding
						}
					}
				}
				if !matched {
					return s, ErrBinding
				}
			}
		}
		next.landings[attemptKey(v)] = v
		next.landingIDs[attemptKey(v)] = id
	}
	next.records = append(next.records, Record{id, raw})
	next.bytes += len(raw)
	next.lastID = id
	next = compact(next, put)
	if err := next.capacity(false); err != nil {
		return s, err
	}
	return next, nil
}

func validateInventory(g group) error {
	h := g.header
	if InventoryDigest(h, g.commits) != h.Digest {
		return ErrBinding
	}
	if h.Complete {
		declarations, err := p.DeclarationDigest(g.commits, true, nil)
		if err != nil || h.DeclarationDigest != declarations {
			return ErrBinding
		}
	}
	originals := map[string]bool{}
	excluded := map[string]bool{}
	for _, id := range h.Request.Excluded {
		excluded[id] = true
	}
	for i, c := range g.commits {
		if originals[c.ObjectID] || excluded[c.ObjectID] || i > 0 && g.commits[i-1].ObjectID >= c.ObjectID {
			return ErrBinding
		}
		originals[c.ObjectID] = true
	}
	union := map[string]bool{}
	for _, id := range append(slices.Clone(h.Stable), h.Additional...) {
		if union[id] || !originals[id] {
			return ErrBinding
		}
		union[id] = true
	}
	if len(union) != len(originals) {
		return ErrBinding
	}
	return nil
}

// SettledAttempt returns the immutable completion/terminal fence position.
func (s State) SettledAttempt(attempt string) (uint64, bool) {
	id, ok := s.settled[attempt]
	return id, ok
}

// AwaitLandedOwned retains the tested evidence after an accepted queue outcome.
// The marker is checkpointed until its landed companion completes the attempt.
func AwaitLandedOwned(s State, id uint64, batch, attempt string) State {
	l, ok := s.landings[stringJSON([]string{attempt, "tested"})]
	if !ok || l.BatchID != batch {
		return s
	}
	if _, exists := s.awaiting[attempt]; exists {
		return s
	}
	if s.awaiting == nil {
		s.awaiting = map[string]uint64{}
	}
	s.awaiting[attempt] = id
	s.awaitingBytes += fenceSize(attempt, id)
	s.lastID = id
	return s
}

// pendingGeneration protects exact inventories awaiting a landed companion.
func (s State) pendingGeneration(key string) bool {
	for _, l := range s.landings {
		if _, pending := s.awaiting[l.AttemptID]; !pending {
			continue
		}
		for _, m := range l.Members {
			if generationKey(m.Request) == key {
				return true
			}
		}
	}
	return false
}
