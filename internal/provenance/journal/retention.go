package journal

import (
	"encoding/json"
	"slices"
)

// MaxPendingLandings bounds v9 reload reference validation independently of the
// lifetime journal length. Completed attempts are retained in the journal only.
const MaxPendingLandings = 16

// MaxRetainedReferences caps the sum of per-companion distinct external targets.
// The composed validator caches targets within each companion, so this is also
// the maximum number of exact-ID DB queries on a compact v9 reload.
const MaxRetainedReferences = 256

// ReferenceCount counts distinct targets using the composed validator's cache scope.
func ReferenceCount(l Landing) int {
	refs := map[uint64]bool{l.DecisionID: true}
	for _, m := range l.Members {
		for _, layer := range m.Layers {
			for _, id := range layer.References {
				refs[id] = true
			}
		}
	}
	return len(refs)
}

type fence struct {
	Key string `json:"Key"`
	ID  uint64 `json:"ID"`
}

// A second, explicitly discriminated v9 shape retains compact replay fences.
// The original v9 shape remains readable; legacy queue bytes never use this.
type checkpoint struct {
	Through     uint64  `json:"Through"`
	Generations []fence `json:"Generations"`
	Settled     []fence `json:"Settled"`
}
type compactSnapshot struct {
	Version    int             `json:"Version"`
	BaseKind   string          `json:"BaseKind"`
	Base       json.RawMessage `json:"Base"`
	LastID     uint64          `json:"LastID"`
	Records    []Record        `json:"Records"`
	Compaction checkpoint      `json:"Compaction"`
}

func sortedFences(m map[string]uint64) []fence {
	out := make([]fence, 0, len(m))
	for key, id := range m {
		out = append(out, fence{key, id})
	}
	slices.SortFunc(out, func(a, b fence) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return out
}
func (s State) checkpoint() checkpoint {
	return checkpoint{s.lastID, sortedFences(s.retired), sortedFences(s.settled)}
}
func (s State) compacted() bool { return len(s.retired) != 0 || len(s.settled) != 0 }

// Through is the latest folded side-event ID, including retired records.
func (s State) Through() uint64 { return s.lastID }

// Preflight leaves headroom below both caps for a transaction's complete bundle.
func (s State) Preflight() error { return s.capacity(true) }
func (s State) capacity(reserve bool) error {
	metadata := 0
	if s.compacted() {
		metadata = s.fenceBytes + 128
	}
	pending, references := 0, 0
	for _, l := range s.landings {
		if l.Stage == "tested" {
			pending++
			references += ReferenceCount(l)
		}
	}
	records, bytes := len(s.records), s.bytes+metadata
	if reserve {
		records++
		bytes++
	}
	if records > maxRecords || bytes > maxSnapshotBytes/2 || pending > MaxPendingLandings || references > MaxRetainedReferences {
		return RetentionLimitError{Records: records, Bytes: bytes, Pending: pending, References: references}
	}
	return nil
}

func compact(s State) State {
	// Retire a completed pair, keeping a small immutable duplicate fence. A fresh
	// inventory generation is required before reusing a settled member for work.
	settledGroups := map[string]bool{}
	for key, l := range s.landings {
		if l.Stage != "landed" {
			continue
		}
		s.settled[l.AttemptID] = s.lastID
		s.fenceBytes += fenceSize(l.AttemptID, s.lastID)
		for _, m := range l.Members {
			settledGroups[generationKey(m.Request)] = true
		}
		delete(s.landingIDs, key)
		delete(s.landingIDs, stringJSON([]string{l.AttemptID, "tested"}))
		delete(s.landings, key)
		delete(s.landings, stringJSON([]string{l.AttemptID, "tested"}))
	}
	needed := map[string]bool{}
	for _, l := range s.landings {
		for _, m := range l.Members {
			needed[generationKey(m.Request)] = true
		}
	}
	keep := map[uint64]bool{}
	for key, g := range s.groups {
		if !needed[key] && (settledGroups[key] || s.latest[changeKey(g.header.Request)] != key) {
			s.retired[key] = g.ids[0]
			s.fenceBytes += fenceSize(key, g.ids[0])
			delete(s.groups, key)
		} else {
			for _, id := range g.ids {
				keep[id] = true
			}
		}
	}
	for _, id := range s.landingIDs {
		keep[id] = true
	}
	records := make([]Record, 0, len(s.records))
	s.bytes = 0
	for _, r := range s.records {
		if keep[r.ID] {
			records = append(records, r)
			s.bytes += len(r.Payload)
		}
	}
	s.records = records
	return s
}

// restoreCheckpoint reconstructs active inventories independently, then validates
// pending attempts against them. Retired fences certify previously folded facts;
// like dependency's v8 projection they are structurally checked, not DB reread.
func restoreCheckpoint(c checkpoint, records []Record) (State, error) {
	s := State{groups: map[string]group{}, latest: map[string]string{}, landings: map[string]Landing{}, retired: map[string]uint64{}, settled: map[string]uint64{}, landingIDs: map[string]uint64{}}
	latestID := map[string]uint64{}
	history := map[string][]uint64{}
	setLatest := func(key, change string, id uint64) {
		if id > latestID[change] {
			latestID[change], s.latest[change] = id, key
		}
	}
	seenIDs := map[uint64]bool{}
	for i, f := range c.Generations {
		var parts []string
		if json.Unmarshal([]byte(f.Key), &parts) != nil || len(parts) != 4 || stringJSON(parts) != f.Key || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" || f.ID == 0 || f.ID > c.Through || seenIDs[f.ID] || i > 0 && c.Generations[i-1].Key >= f.Key {
			return State{}, ErrBinding
		}
		seenIDs[f.ID] = true
		s.retired[f.Key] = f.ID
		s.fenceBytes += fenceSize(f.Key, f.ID)
		setLatest(f.Key, stringJSON(parts[:3]), f.ID)
		history[stringJSON(parts[:3])] = append(history[stringJSON(parts[:3])], f.ID)
	}
	for i, f := range c.Settled {
		if f.Key == "" || f.ID == 0 || f.ID > c.Through || seenIDs[f.ID] || i > 0 && c.Settled[i-1].Key >= f.Key {
			return State{}, ErrBinding
		}
		seenIDs[f.ID] = true
		s.settled[f.Key] = f.ID
		s.fenceBytes += fenceSize(f.Key, f.ID)
	}
	var pending []Record
	var previous uint64
	for _, r := range records {
		if r.ID <= previous || r.ID > c.Through || seenIDs[r.ID] {
			return State{}, ErrBinding
		}
		previous = r.ID
		e, err := DecodeEvent(r.Payload)
		if err != nil {
			return State{}, err
		}
		_, canonical, err := EncodeEvent(e)
		if err != nil || string(canonical) != string(r.Payload) {
			return State{}, ErrMalformed
		}
		if h, ok := e.(Observation); ok {
			key := generationKey(h.Request)
			if _, exists := s.retired[key]; exists {
				return State{}, ErrBinding
			}
			// Apply the exact chunk/inventory validation with an isolated CAS basis.
			temp := State{groups: map[string]group{}, latest: map[string]string{}, retired: map[string]uint64{}}
			if g, exists := s.groups[key]; exists {
				temp.groups[key], temp.latest[changeKey(h.Request)] = g, key
			} else if h.PreviousObservationID != 0 {
				if h.PreviousObservationID >= r.ID {
					return State{}, ErrBinding
				}
				temp.latest[changeKey(h.Request)] = "prior"
				temp.retired["prior"] = h.PreviousObservationID
			}
			next, err := Apply(temp, r.ID, h)
			if err != nil {
				return State{}, err
			}
			if _, exists := s.groups[key]; !exists {
				history[changeKey(h.Request)] = append(history[changeKey(h.Request)], r.ID)
			}
			s.groups[key] = next.groups[key]
			setLatest(key, changeKey(h.Request), s.groups[key].ids[0])
		} else {
			pending = append(pending, r)
		}
		s.records = append(s.records, Record{r.ID, slices.Clone(r.Payload)})
		s.bytes += len(r.Payload)
	}
	for _, ids := range history {
		slices.Sort(ids)
	}
	for _, g := range s.groups {
		ids := history[changeKey(g.header.Request)]
		index, found := slices.BinarySearch(ids, g.ids[0])
		var prior uint64
		if index > 0 {
			prior = ids[index-1]
		}
		if !found || g.header.PreviousObservationID != prior {
			return State{}, ErrBinding
		}
	}
	for _, r := range pending {
		e, _ := DecodeEvent(r.Payload)
		l := e.(Landing)
		if l.Stage != "tested" {
			return State{}, ErrBinding
		}
		temp := s
		temp.lastID, temp.records, temp.bytes = 0, nil, 0
		temp.latest = map[string]string{}
		// A retained tested record may predate a now-superseding observation.
		for _, m := range l.Members {
			key := generationKey(m.Request)
			g, ok := s.groups[key]
			if !ok || len(g.ids) == 0 || g.ids[len(g.ids)-1] >= r.ID {
				return State{}, ErrBinding
			}
			ids := history[changeKey(m.Request)]
			index, _ := slices.BinarySearch(ids, r.ID)
			if index == 0 || ids[index-1] != g.ids[0] {
				return State{}, ErrBinding
			}
			temp.latest[changeKey(m.Request)] = key
		}
		next, err := Apply(temp, r.ID, l)
		if err != nil {
			return State{}, err
		}
		s.landings[attemptKey(l)] = next.landings[attemptKey(l)]
		s.landingIDs[attemptKey(l)] = r.ID
	}
	s.lastID = c.Through
	if err := s.capacity(false); err != nil {
		return State{}, err
	}
	return s, nil
}

func fenceSize(key string, id uint64) int { b, _ := json.Marshal(fence{key, id}); return len(b) + 1 }
