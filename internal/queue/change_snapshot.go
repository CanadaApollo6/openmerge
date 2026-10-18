package queue

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// BatchRevision pins the revision tested by an existing PR batch. It remains
// independent of the latest head, including during unsettled landing attempts.
type BatchRevision struct {
	PR       int
	Revision ChangeRevision
	HeadSHA  string
}

// BatchRevisions returns a detached view of a batch's revision bindings.
func (s State) BatchRevisions(batchID string) []BatchRevision {
	return slices.Clone(s.batchRevisions[batchID])
}

func (s *State) retainBatchRevisions() {
	changed := false
	for id := range s.batchRevisions {
		if s.Batch(id) == nil {
			changed = true
		}
	}
	for _, batch := range s.Batches() {
		if bindings, ok := s.batchRevisions[batch.ID]; !ok || !batchRevisionMembersEqual(bindings, batch.PRs) {
			changed = true
		}
	}
	if !changed {
		return
	}
	s.batchRevisions = maps.Clone(s.batchRevisions)
	if s.batchRevisions == nil {
		s.batchRevisions = make(map[string][]BatchRevision)
	}
	for id := range s.batchRevisions {
		if s.Batch(id) == nil {
			delete(s.batchRevisions, id)
		}
	}
	for _, batch := range s.Batches() {
		previous := s.batchRevisions[batch.ID]
		if batchRevisionMembersEqual(previous, batch.PRs) {
			continue
		}
		bindings := make([]BatchRevision, 0, len(batch.PRs))
		for _, pr := range batch.PRs {
			current, _ := s.PRRevision(pr.PR)
			revision := current.Revision
			if current.HeadSHA != pr.HeadSHA {
				revision = 0
			}
			// Fast-forward selection narrows a batch without rebinding its members.
			for _, pinned := range previous {
				if pinned.PR == pr.PR && pinned.HeadSHA == pr.HeadSHA {
					revision = pinned.Revision
				}
			}
			bindings = append(bindings, BatchRevision{pr.PR, revision, pr.HeadSHA})
		}
		s.batchRevisions[batch.ID] = bindings
	}
}

func batchRevisionMembersEqual(bindings []BatchRevision, prs []BatchPR) bool {
	if len(bindings) != len(prs) {
		return false
	}
	for i, pr := range prs {
		if bindings[i].PR != pr.PR || bindings[i].HeadSHA != pr.HeadSHA {
			return false
		}
	}
	return true
}

func validateChangeSnapshot(v stateSnapshot) error {
	if len(v.ChangeRevisions) > maxSnapshotEntries || len(v.BatchRevisions) > 32 {
		return fmt.Errorf("invalid change snapshot bounds")
	}
	for pr, record := range v.ChangeRevisions {
		if pr <= 0 || record.Revision == 0 || record.Revision > ChangeRevision(record.LogPosition) || record.HeadSHA == "" || record.LogPosition == 0 || record.LogPosition > v.Revision {
			return fmt.Errorf("invalid snapshot revision")
		}
		if !record.Basis.At.IsZero() && (record.ChangeID == "" || record.Basis.BaseSHA == "") {
			return fmt.Errorf("invalid snapshot observation basis")
		}
		if record.ChangeID != "" && record.ChangeID.Validate() != nil {
			return fmt.Errorf("invalid snapshot change ID")
		}
	}
	for id, bindings := range v.BatchRevisions {
		var batch *Batch
		for i := range v.Chain {
			if v.Chain[i].Batch.ID == id {
				batch = &v.Chain[i].Batch
			}
		}
		if batch == nil || len(bindings) != len(batch.PRs) {
			return fmt.Errorf("invalid snapshot batch revision bindings")
		}
		for i, binding := range bindings {
			current, ok := v.ChangeRevisions[binding.PR]
			if binding.PR != batch.PRs[i].PR || binding.HeadSHA != batch.PRs[i].HeadSHA || binding.Revision > current.Revision || (!ok && binding.Revision != 0) {
				return fmt.Errorf("invalid snapshot batch revision")
			}
		}
	}
	if v.Version == SnapshotVersion && len(v.BatchRevisions) != len(v.Chain) {
		return fmt.Errorf("missing snapshot batch revisions")
	}
	return nil
}

// Version 2 never persisted revision boundaries. Keep that absence explicit;
// only a full journal replay can recover its historical revision sequence.
func upgradeChangeSnapshot(data []byte, version int) ([]byte, error) {
	if version != 2 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"ChangeRevisions", "BatchRevisions"} {
		if _, ok := fields[key]; ok {
			return nil, fmt.Errorf("version 2 cannot contain Stage A state")
		}
		fields[key] = json.RawMessage(`null`)
	}
	return json.Marshal(fields)
}
