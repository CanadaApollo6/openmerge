package executor

import (
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// BuildChangeKey derives the canonical identity used by a recorded queue
// classification. store.QueueKey satisfies this interface without making the
// executor package depend on store (whose package tests exercise executor).
type BuildChangeKey interface {
	ChangeID(int) (queue.ChangeID, error)
}

// BuildMetadataLoader supplies facts that were recorded before a test build.
// It is intentionally a pure seam: production leaves the Executor hook nil
// until Stage A activation wires the recorded queue snapshot into it.
type BuildMetadataLoader func(queue.State, BuildChangeKey, queue.Batch, string) ([]*forge.ChangeMetadata, error)

// LoadBuildMetadata resolves the classification for the exact revisions pinned
// by batch. It never consults the latest PR head to select a revision. The
// policyDigest is the semantic policy digest pinned by the build configuration;
// state.ConfigDigest and batch.ConfigDigest must both carry the exact effective
// queue-config pin.
func LoadBuildMetadata(state queue.State, key BuildChangeKey, batch queue.Batch, policyDigest string) ([]*forge.ChangeMetadata, error) {
	if key == nil || batch.ID == "" || batch.BaseSHA == "" || len(batch.PRs) == 0 {
		return nil, fmt.Errorf("build metadata requires a pinned batch")
	}
	if state.BaseBranch == "" || state.BaseSHA == "" {
		return nil, fmt.Errorf("build metadata requires the recorded queue base")
	}
	if policyDigest == "" {
		return nil, fmt.Errorf("build metadata requires a semantic policy digest")
	}
	if state.ConfigDigest == "" || batch.ConfigDigest == "" || state.ConfigDigest != batch.ConfigDigest {
		return nil, fmt.Errorf("build metadata requires an exact pinned config digest")
	}

	bindings := state.BatchRevisions(batch.ID)
	if len(bindings) < len(batch.PRs) {
		return nil, fmt.Errorf("build metadata has no pinned revision for every batch member")
	}
	metadata := make([]*forge.ChangeMetadata, len(batch.PRs))
	seen := make(map[int]bool, len(batch.PRs))
	for i, pin := range batch.PRs {
		if pin.PR <= 0 || pin.HeadSHA == "" || seen[pin.PR] {
			return nil, fmt.Errorf("invalid pinned batch member %d", pin.PR)
		}
		seen[pin.PR] = true
		binding := bindings[i]
		if binding.PR != pin.PR || binding.HeadSHA != pin.HeadSHA {
			return nil, fmt.Errorf("batch revision binding does not match pinned member %d", pin.PR)
		}
		id, err := key.ChangeID(pin.PR)
		if err != nil {
			return nil, fmt.Errorf("derive change identity for PR %d: %w", pin.PR, err)
		}
		bound, err := queue.BoundBatchRevision(binding, id)
		if err != nil {
			return nil, fmt.Errorf("bind revision for PR %d: %w", pin.PR, err)
		}
		recorded, ok := state.Classification(id, bound.Revision)
		if !ok {
			return nil, fmt.Errorf("classification for %s@%d is not recorded", id, bound.Revision)
		}
		if err := recorded.Validate(); err != nil {
			return nil, fmt.Errorf("classification for %s@%d is invalid: %w", id, bound.Revision, err)
		}
		inputs := recorded.Evidence.Classification.Inputs
		if inputs.ChangeID != string(id) || inputs.Revision != uint64(bound.Revision) {
			return nil, fmt.Errorf("classification identity for PR %d does not match its pinned revision", pin.PR)
		}
		if inputs.Head != pin.HeadSHA || bound.HeadSHA != pin.HeadSHA {
			return nil, fmt.Errorf("classification head for PR %d does not match its pinned head", pin.PR)
		}
		// Classification is based on the recorded queue-base observation, not
		// a speculative predecessor batch's build base.
		if inputs.Base != state.BaseSHA {
			return nil, fmt.Errorf("classification base for PR %d does not match the recorded queue base", pin.PR)
		}
		if inputs.PolicyDigest != policyDigest {
			return nil, fmt.Errorf("classification policy digest for PR %d does not match the pinned policy", pin.PR)
		}
		metadata[i] = &forge.ChangeMetadata{
			ChangeID: string(id),
			Revision: uint64(bound.Revision),
			Labels:   classificationLabels(recorded.Evidence.Classification),
		}
	}
	return metadata, nil
}

func classificationLabels(c changepolicy.Classification) []string {
	labels := make([]string, 0, len(c.Facts))
	for label := range c.Facts {
		if c.Label(label) == changepolicy.True {
			labels = append(labels, label)
		}
	}
	slices.Sort(labels)
	return slices.Compact(labels)
}

func (x *Executor) buildMetadataBatch(a queue.BuildTestRef) queue.Batch {
	batch := queue.Batch{
		ID:           a.BatchID,
		BaseSHA:      a.BaseSHA,
		ConfigDigest: x.BuildMetadataState.ConfigDigest,
		PRs:          slices.Clone(a.PRs),
		Method:       a.Method,
		TestRef:      a.Ref,
	}
	if recorded := x.BuildMetadataState.Batch(a.BatchID); recorded != nil {
		batch.ConfigDigest = recorded.ConfigDigest
	}
	return batch
}

func (x *Executor) buildPRs(a queue.BuildTestRef) ([]forge.BuildPR, error) {
	var metadata []*forge.ChangeMetadata
	if x.BuildMetadata != nil {
		var err error
		metadata, err = x.BuildMetadata(x.BuildMetadataState, x.BuildMetadataKey, x.buildMetadataBatch(a), x.BuildPolicyDigest)
		if err != nil {
			return nil, err
		}
		if len(metadata) != len(a.PRs) {
			return nil, fmt.Errorf("build metadata returned %d members for %d pinned PRs", len(metadata), len(a.PRs))
		}
		for i, value := range metadata {
			if value == nil {
				return nil, fmt.Errorf("build metadata is missing for PR %d", a.PRs[i].PR)
			}
		}
	}
	prs := make([]forge.BuildPR, len(a.PRs))
	for i, pin := range a.PRs {
		prs[i] = forge.BuildPR{Number: pin.PR, HeadSHA: pin.HeadSHA}
		if metadata != nil {
			detached := *metadata[i]
			detached.Labels = slices.Clone(metadata[i].Labels)
			prs[i].Metadata = &detached
		}
	}
	return prs, nil
}
