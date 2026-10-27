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

// State is an immutable retained-evidence fold, not a queue authorization API.
// It keeps bounded records so later audit selection can follow exact references.
type State struct {
	records  []Record
	groups   map[string]group
	latest   map[string]string
	landings map[string]Landing
	bytes    int
}

func changeKey(r p.InventoryRequest) string {
	return stringJSON([]string{r.Repository, r.Queue, r.ChangeID})
}
func generationKey(r p.InventoryRequest) string {
	return stringJSON([]string{r.Repository, r.Queue, r.ChangeID, r.Generation})
}
func stringJSON(v any) string     { b, _ := json.Marshal(v); return string(b) }
func attemptKey(e Landing) string { return stringJSON([]string{e.AttemptID, e.Stage}) }

// Records returns detached records, suitable for an explicit future snapshot
// cursor encoder. Side-event and snapshot-table writers remain unwired.
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
	_, raw, err := EncodeEvent(e)
	if err != nil {
		return s, err
	}
	if id == 0 || len(s.records) > 0 && id <= s.records[len(s.records)-1].ID {
		return s, ErrBinding
	}
	if len(s.records) >= maxRecords || s.bytes+len(raw) > maxSnapshotBytes/2 {
		return s, RetentionLimitError{Records: len(s.records) + 1, Bytes: s.bytes + len(raw)}
	}
	// Detach all caller-owned slices, including nested gate inputs and mappings.
	e, err = DecodeEvent(raw)
	if err != nil {
		return s, err
	}
	next := s
	next.records = slices.Clone(s.records)
	next.groups = maps.Clone(s.groups)
	if next.groups == nil {
		next.groups = map[string]group{}
	}
	next.latest = maps.Clone(s.latest)
	if next.latest == nil {
		next.latest = map[string]string{}
	}
	next.landings = maps.Clone(s.landings)
	if next.landings == nil {
		next.landings = map[string]Landing{}
	}
	switch v := e.(type) {
	case Observation:
		key, ck := generationKey(v.Request), changeKey(v.Request)
		g, exists := s.groups[key]
		if !exists {
			if v.Index != 0 {
				return s, ErrIncomplete
			}
			var prior uint64
			if old, ok := s.groups[s.latest[ck]]; ok {
				prior = old.ids[0]
			}
			if v.PreviousObservationID != prior {
				return s, ErrBinding
			}
			g = group{header: v, commits: []p.Commit{}, ids: []uint64{}}
			g.header.Commits = []p.Commit{}
			g.header.Index = 0
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
	}
	next.records = append(next.records, Record{id, raw})
	next.bytes += len(raw)
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
