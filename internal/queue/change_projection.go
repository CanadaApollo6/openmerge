package queue

import "maps"

type revisionShards [observationShardCount]map[int]RevisionRecord
type changeRevisions struct{ shards *revisionShards }

func (r changeRevisions) get(pr int) (RevisionRecord, bool) {
	if r.shards == nil {
		return RevisionRecord{}, false
	}
	v, ok := r.shards[uint(pr)%observationShardCount][pr]
	return v, ok
}

func (r *changeRevisions) put(pr int, v RevisionRecord) {
	next := new(revisionShards)
	if r.shards != nil {
		*next = *r.shards
	}
	i := uint(pr) % observationShardCount
	shard := maps.Clone(next[i])
	if shard == nil {
		shard = make(map[int]RevisionRecord)
	}
	shard[pr] = v
	next[i] = shard
	r.shards = next
}

func (r changeRevisions) snapshot() map[int]RevisionRecord {
	result := make(map[int]RevisionRecord)
	if r.shards != nil {
		for _, shard := range r.shards {
			maps.Copy(result, shard)
		}
	}
	return result
}

func restoreChangeRevisions(values map[int]RevisionRecord) changeRevisions {
	var r changeRevisions
	if len(values) == 0 {
		return r
	}
	r.shards = new(revisionShards)
	for pr, v := range values {
		i := uint(pr) % observationShardCount
		if r.shards[i] == nil {
			r.shards[i] = make(map[int]RevisionRecord)
		}
		r.shards[i][pr] = v
	}
	return r
}

func (s *State) observeLegacyRevision(e PRObserved) {
	if e.PR <= 0 || e.HeadSHA == "" {
		return
	}
	current, _ := s.PRRevision(e.PR)
	if current.HeadSHA == e.HeadSHA {
		return
	}
	s.changeRevisions.put(e.PR, RevisionRecord{ChangeID: current.ChangeID, Revision: current.Revision + 1, HeadSHA: e.HeadSHA,
		Basis: RevisionObservationBasis{BaseSHA: s.BaseSHA}, LogPosition: s.revision})
}

func (s *State) observeChangeRevision(e ChangeRevisionObserved) {
	current, _ := s.PRRevision(e.PR)
	position := s.revision
	if current.Revision == e.Revision {
		position = current.LogPosition
	}
	s.changeRevisions.put(e.PR, RevisionRecord{ChangeID: e.ChangeID, Revision: e.Revision, HeadSHA: e.HeadSHA, Basis: e.Basis, LogPosition: position})
	// Conflict grace is a later integration: Stage A retains HeadChanged
	// removal, with no grace deadline, retry budget, or position retention.
	if entry := s.entry(e.PR); entry != nil && entry.HeadSHA != e.HeadSHA {
		s.requestDequeue(e.PR, HeadChanged)
	}
}
