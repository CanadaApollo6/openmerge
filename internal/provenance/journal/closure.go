package journal

import (
	"encoding/json"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
)

// ClosureStore is private scratch storage for an audit fold, never a journal or
// live projection. Get returns nil for a missing key. Failed folds are discarded.
// Values are bounded by one inventory or landing, independent of journal age.
type ClosureStore interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
}

// ClosureFold validates a closed, ordered selection using Apply's exact rules.
// It loads only the groups/attempts needed by the current bounded event, rather
// than retaining raw records or copying a growing prefix on every Apply.
// The store belongs to one queue and must start empty. An error poisons the fold.
type ClosureFold struct {
	Store  ClosureStore
	lastID uint64
	failed bool
}

type closureGroup struct {
	Header  Observation
	Commits []p.Commit
	IDs     []uint64
}

func (f *ClosureFold) get(key string, value any) error {
	raw, err := f.Store.Get(key)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	return json.Unmarshal(raw, value)
}
func (f *ClosureFold) put(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return f.Store.Put(key, raw)
}

func (f *ClosureFold) loadGroup(s *State, key string) error {
	if key == "" {
		return nil
	}
	if _, ok := s.groups[key]; ok {
		return nil
	}
	var g *closureGroup
	if err := f.get("group:"+key, &g); err != nil {
		return err
	}
	if g != nil {
		s.groups[key] = group{g.Header, g.Commits, g.IDs}
	}
	return nil
}
func (f *ClosureFold) loadRequest(s *State, r p.InventoryRequest) error {
	ck := changeKey(r)
	var latest string
	if err := f.get("latest:"+ck, &latest); err != nil {
		return err
	}
	s.latest[ck] = latest
	if err := f.loadGroup(s, latest); err != nil {
		return err
	}
	return f.loadGroup(s, generationKey(r))
}
func (f *ClosureFold) loadLanding(s *State, key string) error {
	var v *Landing
	if err := f.get("landing:"+key, &v); err != nil {
		return err
	}
	if v != nil {
		s.landings[key] = *v
	}
	return nil
}

// Apply processes one selected raw-journal record. Only its small dependency
// neighborhood is materialized as State; retention of that live projection is
// deliberately separate from validation of historical audit evidence.
func (f *ClosureFold) Apply(id uint64, e Event) error {
	if f.failed || f.Store == nil || id == 0 || id <= f.lastID {
		f.failed = true
		return ErrBinding
	}
	f.failed = true
	s := State{groups: map[string]group{}, latest: map[string]string{}, landings: map[string]Landing{}}
	switch v := e.(type) {
	case Observation:
		if err := f.loadRequest(&s, v.Request); err != nil {
			return err
		}
	case Landing:
		for _, m := range v.Members {
			if err := f.loadRequest(&s, m.Request); err != nil {
				return err
			}
		}
		for _, stage := range []string{"tested", v.Stage} {
			if err := f.loadLanding(&s, stringJSON([]string{v.AttemptID, stage})); err != nil {
				return err
			}
		}
	default:
		return ErrMalformed
	}
	next, err := Apply(s, id, e)
	if err != nil {
		return err
	}
	switch v := e.(type) {
	case Observation:
		key := generationKey(v.Request)
		g := next.groups[key]
		if err := f.put("group:"+key, closureGroup{g.header, g.commits, g.ids}); err != nil {
			return err
		}
		if err := f.put("latest:"+changeKey(v.Request), key); err != nil {
			return err
		}
	case Landing:
		if err := f.put("landing:"+attemptKey(v), v); err != nil {
			return err
		}
	}
	f.lastID, f.failed = id, false
	return nil
}
