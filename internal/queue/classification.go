package queue

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"

	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ChangeClassified is additive wire-v1 evidence. No production emitter exists
// before 11b. Evidence.Classification.Files[].Hunks carries bounded hunk text,
// ranges, omitted-range summaries and Count; there is no separate hunk event.
// Generation is in Classification.Inputs and advances on semantic refreshes,
// including equal-diff base rewrites. Fingerprint includes provenance; receipts
// bind semantic inputs/generation, never this provenance fingerprint alone.
type ChangeClassified struct {
	Evidence classification.Evidence `json:"evidence"`
}

func (ChangeClassified) queueEvent() {}

// Validate checks durable bindings; derivation is performed before recording.
func (e ChangeClassified) Validate() error {
	c := e.Evidence.Classification
	if err := validateClassificationValues(e.Evidence); err != nil {
		return err
	}
	for _, commit := range []string{c.Inputs.Base, c.Inputs.Head, c.Inputs.MergeBase, c.Inputs.GoverningCommit} {
		if !forge.ValidDiffObjectID(commit) {
			return fmt.Errorf("classification requires full commit IDs")
		}
	}
	if ChangeID(c.Inputs.ChangeID).Validate() != nil || !c.Inputs.Recorded() || c.Inputs.GoverningCommit == "" || c.Inputs.GoverningCommit != e.Evidence.Governing.CommitSHA || len(e.Evidence.InputFingerprint) != 64 {
		return fmt.Errorf("invalid classification bindings")
	}
	if _, err := hex.DecodeString(e.Evidence.InputFingerprint); err != nil {
		return fmt.Errorf("invalid classification fingerprint")
	}
	for _, file := range c.Files {
		if file.Binary > 2 || file.Submodule > 2 {
			return fmt.Errorf("invalid file observation truth")
		}

		for _, name := range []string{"docs", "generated", "policy"} {
			value, ok := file.Facts[name]
			if !ok || value > 2 {
				return fmt.Errorf("missing/invalid file facet %s", name)
			}
		}
	}
	for _, name := range []string{"docs", "generated", "policy", "unknown"} {
		value, ok := c.Facts[name]
		if !ok || value > 2 {
			return fmt.Errorf("missing/invalid classification facet %s", name)
		}
	}
	if c.InventoryComplete != (e.Evidence.Inventory.Completeness == "complete") || c.HunksComplete && e.Evidence.Hunks.Completeness != "complete" {
		return fmt.Errorf("classification coverage mismatch")
	}
	if !c.InventoryComplete && (c.PathsSafe || c.HunksComplete || c.PolicyOwnersKnown || c.CodeOwnersKnown) {
		return fmt.Errorf("incomplete classification claims complete evidence")
	}
	return nil
}

type classifications map[ChangeID]map[ChangeRevision]ChangeClassified

func detachedClassification(e ChangeClassified) ChangeClassified {
	raw, _ := json.Marshal(e)
	var out ChangeClassified
	_ = json.Unmarshal(raw, &out)
	return out
}

// Classification returns detached recorded evidence; absence stays unrecorded.
func (s State) Classification(id ChangeID, revision ChangeRevision) (ChangeClassified, bool) {
	e, ok := s.classifications[id][revision]
	if !ok {
		return ChangeClassified{}, false
	}
	return detachedClassification(e), true
}

func (s State) classify(e ChangeClassified) State {
	if e.Validate() != nil {
		return s
	}
	inputs := e.Evidence.Classification.Inputs
	id, revision := ChangeID(inputs.ChangeID), ChangeRevision(inputs.Revision)
	previous, exists := s.classifications[id][revision]
	if exists {
		old := previous.Evidence.Classification.Inputs
		// Equal generation may refresh provenance only. Duplicate/stale semantic
		// records cannot resurrect an older receipt binding.
		if inputs.Head != old.Head || inputs.Generation < old.Generation || (inputs.Generation > old.Generation && inputs.Generation-old.Generation != 1) || (inputs.Generation == old.Generation && !sameClassificationMeaning(previous, e)) {
			return s
		}
	}
	if !exists && inputs.Generation != 1 {
		return s
	}
	// Build metadata pins labels but not generation. A semantic refresh must
	// therefore revoke every affected not-yet-landing composition explicitly.
	if !exists || inputs.Generation > previous.Evidence.Classification.Inputs.Generation {
		affected := ""
		for _, b := range s.Batches() {
			if b.Phase == Landing {
				continue
			}
			for _, p := range b.PRs {
				r, _ := s.PRRevision(p.PR)
				if r.ChangeID == id && r.Revision == revision && p.HeadSHA == inputs.Head {
					affected = b.ID
					break
				}
			}
			if affected != "" {
				break
			}
		}
		if affected != "" {
			s = cloneState(s)
			s.invalidateFrom(s.Batch(affected), "classification-changed")
		}
	}
	s.classifications = maps.Clone(s.classifications)
	if s.classifications == nil {
		s.classifications = make(classifications)
	}
	revisions := maps.Clone(s.classifications[id])
	if revisions == nil {
		revisions = make(map[ChangeRevision]ChangeClassified)
	}
	revisions[revision] = detachedClassification(e)
	s.classifications[id] = revisions
	return s
}

// BoundPRRevision resolves the legacy empty ChangeID at the read boundary.
// canonicalID must come from PRChangeID using the stored canonical queue key.
// Revision zero (including unrecorded batch bindings) is never synthesized.
func BoundPRRevision(record RevisionRecord, canonicalID ChangeID) (RevisionRecord, error) {
	if canonicalID.Validate() != nil || record.Revision == 0 || record.HeadSHA == "" || (record.ChangeID != "" && record.ChangeID != canonicalID) {
		return RevisionRecord{}, fmt.Errorf("unrecorded or mismatched revision binding")
	}
	record.ChangeID = canonicalID
	return record, nil
}

// BoundBatchRevision derives identity without rebinding to the latest PR head.
func BoundBatchRevision(record BatchRevision, canonicalID ChangeID) (RevisionRecord, error) {
	return BoundPRRevision(RevisionRecord{Revision: record.Revision, HeadSHA: record.HeadSHA}, canonicalID)
}

func upgradeClassificationSnapshot(data []byte, version int) ([]byte, error) {
	data, err := upgradeChangeSnapshot(data, version)
	if err != nil {
		return nil, err
	}
	if version >= 4 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields["Classifications"]; ok {
		return nil, fmt.Errorf("old snapshot cannot contain classifications")
	}
	fields["Classifications"] = json.RawMessage(`null`)
	return json.Marshal(fields)
}

func validateClassifications(values classifications) error {
	count := 0
	for id, revisions := range values {
		for revision, e := range revisions {
			count++
			if count > maxSnapshotEntries || e.Validate() != nil || e.Evidence.Classification.Inputs.ChangeID != string(id) || e.Evidence.Classification.Inputs.Revision != uint64(revision) {
				return fmt.Errorf("invalid snapshot classification")
			}
		}
	}
	return nil
}

// Shared DTOs use Go field names. Wire collections are always non-null. This new envelope
// uses the same strict recursive presence checker as snapshots, then the wire
// decoder's unknown-field/trailing-data rejection. Existing wire codecs remain
// byte-identical. Golden fixtures pin this shared value shape for wire v1.
func readClassificationWire(data json.RawMessage) (ChangeClassified, error) {
	var e ChangeClassified
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return e, err
	}
	if hasNull(tree) {
		return e, fmt.Errorf("classification wire fields must not be null")
	}
	if err := requireSnapshotFields(data, reflect.TypeFor[ChangeClassified](), "change_classified"); err != nil {
		return e, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e, fmt.Errorf("trailing classification data")
	}
	return e, nil
}

// nonNullClassification copies shared DTOs for the wire boundary. Shared types
// keep their ordinary nil zero values; persisted v1 collections use [] and {}.
func nonNullClassification(e ChangeClassified) ChangeClassified {
	return nonNullValue(reflect.ValueOf(e)).Interface().(ChangeClassified)
}
func nonNullValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			out.Field(i).Set(nonNullValue(v.Field(i)))
		}
		return out
	case reflect.Slice:
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(nonNullValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), nonNullValue(iter.Value()))
		}
		return out
	default:
		return v
	}
}
func hasNull(v any) bool {
	if v == nil {
		return true
	}
	switch v := v.(type) {
	case map[string]any:
		for _, child := range v {
			if hasNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasNull(child) {
				return true
			}
		}
	}
	return false
}

// Provenance may be refreshed at the same semantic generation. Changed facts,
// endpoints or explanations require a new generation as well as changed inputs.
func sameClassificationMeaning(a, b ChangeClassified) bool {
	left, right := nonNullClassification(a).Evidence.Classification, nonNullClassification(b).Evidence.Classification
	left.Inputs.ConfigBlob, right.Inputs.ConfigBlob = "", ""
	left.Inputs.GoverningCommit, right.Inputs.GoverningCommit = "", ""
	return reflect.DeepEqual(left, right) && len(classification.RefreshReasons(classification.SemanticInputs(a.Evidence), classification.SemanticInputs(b.Evidence), false, true)) == 0
}
