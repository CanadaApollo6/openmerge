package journal

import p "github.com/CanadaApollo6/openmerge/internal/provenance"

// RetireOwned folds durable queue outcomes into the owned projection. The
// original evidence stays in the journal. Batch retirement releases companions;
// change retirement also releases every generation and any attempt pinning it.
func RetireOwned(s State, id uint64, batch string, drop func(p.InventoryRequest) bool) State {
	if len(s.groups) == 0 && len(s.landings) == 0 {
		return s
	}
	changed := false
	for key, l := range s.landings {
		retire := batch != "" && l.BatchID == batch
		for _, m := range l.Members {
			retire = retire || drop != nil && drop(m.Request)
		}
		if !retire {
			continue
		}
		if s.settled == nil {
			s.settled = map[string]uint64{}
		}
		s.settled[l.AttemptID] = s.landingIDs[key]
		s.fenceBytes += fenceSize(l.AttemptID, s.landingIDs[key])
		delete(s.landings, key)
		delete(s.landingIDs, key)
		changed = true
	}
	for key, g := range s.groups {
		if drop == nil || !drop(g.header.Request) {
			continue
		}
		if s.retired == nil {
			s.retired = map[string]uint64{}
		}
		s.retired[key] = g.ids[0]
		s.fenceBytes += fenceSize(key, g.ids[0])
		delete(s.groups, key)
		changed = true
	}
	if !changed {
		return s
	}
	s.lastID = id
	return compact(s, func(m map[string]uint64, key string, value uint64) { m[key] = value })
}
