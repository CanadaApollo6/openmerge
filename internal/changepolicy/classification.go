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
	Base, Head, MergeBase, GoverningCommit string
	ConfigBlob, OwnersBlob                 string
	AttributeBlobs                         map[string]string
	ClassifierVersion, PolicyDigest        string
	Generation                             uint64
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
	Facts                        map[string]Truth
	Reasons                      []string
	PolicyOwnersKnown            bool
	PolicyOwners                 []PolicyOwner
}

func (c Classification) label(name string) Truth {
	v := c.Facts[name]
	if (name == "docs" || name == "generated") && !c.InventoryComplete {
		return Unknown
	}
	if !c.InventoryComplete && v != True {
		return Unknown
	}
	return v
}
