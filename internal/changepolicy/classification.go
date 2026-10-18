// Package changepolicy evaluates approval policy over supplied, recorded facts.
// It performs no forge, filesystem, database, or clock reads.
package changepolicy

// Truth retains uncertainty; its zero value never authorizes a decision.
type Truth uint8

// Truth values use three-valued conjunction.
const (
	Unknown Truth = iota
	False
	True
)

func and(a, b Truth) Truth {
	if a == False || b == False {
		return False
	}
	if a != True || b != True {
		return Unknown
	}
	return True
}

// ClassificationInputs identifies immutable evidence. ConfigBlob and
// GoverningCommit are provenance, not semantic policy invalidation inputs.
type ClassificationInputs struct {
	ChangeID                               string
	Revision                               uint64
	Base, Head, MergeBase, GoverningCommit string
	ConfigBlob, OwnersBlob                 string
	AttributeBlobs                         map[string]string
	ClassifierVersion, PolicyDigest        string
	Generation                             uint64
}

// Recorded reports whether the classification basis is fully identified.
// Revisions and classification generations start at 1; zero is unresolved.
func (i ClassificationInputs) Recorded() bool {
	return i.ChangeID != "" && i.Revision > 0 && i.Generation > 0 &&
		i.Base != "" && i.Head != "" && i.MergeBase != "" &&
		i.PolicyDigest != "" && i.ClassifierVersion != ""
}

// LabelReasons binds each explanation to the classification label it supports.
type LabelReasons struct {
	Label   string
	Reasons []string
}

// Hunk records bounded UTF-8 diff text and its ranges in the immutable blobs.
type Hunk struct {
	OldStart, OldLines, NewStart, NewLines int
	Text                                   string
}

// HunkRange records an omitted or truncated range without claiming completeness.
type HunkRange struct {
	OldStart, OldLines, NewStart, NewLines int
	Reason                                 string
}

// HunkEvidence is populated by PRs 2b/7a. Complete is independent of inventory
// completeness; a missing or truncated diff never implies a complete brief.
type HunkEvidence struct {
	Hunks    []Hunk
	Omitted  []HunkRange
	Complete bool
}

// FileEvidence is PR 4a's per-file inventory projection, without derivation.
// Empty paths/blobs/modes denote an absent side for an addition or deletion.
// RenamePaired records whether Renamed has a proven old/new pairing. Binary and
// Submodule retain uncertainty. Facts and Reasons describe this file's facets.
type FileEvidence struct {
	OldPath, NewPath, OldBlob, NewBlob string
	OldMode, NewMode                   uint32
	Renamed, RenamePaired              bool
	Binary, Submodule                  Truth
	Facts                              map[string]Truth
	Reasons                            []LabelReasons
	Hunks                              HunkEvidence
}

// IdentitySet distinguishes a resolved empty pool from unreadable membership.
// Fresh means the supplied observation was revalidated for this evaluation.
type IdentitySet struct {
	IDs             []string
	Resolved, Fresh bool
	Reason          string
}

// PolicyOwner is the union of effective CODEOWNERS for one policy path.
// Renames supply an entry for each relevant side. Groups do not own paths.
type PolicyOwner struct {
	Path     string
	Eligible IdentitySet
}

// Classification is PR 4's output contract. Facts contains classification
// labels only, never forge labels. Missing facts are unknown, not false.
// Paths includes both sides of renames. PathsSafe concerns path matching;
// unreadable attributes may leave PathsSafe true and generated unknown.
// Every-file facets remain unknown while InventoryComplete is false, even
// if every observed file qualifies (or an observed file fails).
type Classification struct {
	Inputs                       ClassificationInputs
	InventoryComplete, PathsSafe bool
	Paths                        []string
	Files                        []FileEvidence
	HunksComplete                bool
	Facts                        map[string]Truth
	Reasons                      []LabelReasons
	PolicyOwnersKnown            bool
	PolicyOwners                 []PolicyOwner
}

// Label returns a recorded facet with inventory completeness applied.
func (c Classification) Label(name string) Truth {
	v := c.Facts[name]
	if (name == "docs" || name == "generated") && !c.InventoryComplete {
		return Unknown
	}
	if !c.InventoryComplete && v != True {
		return Unknown
	}
	return v
}
