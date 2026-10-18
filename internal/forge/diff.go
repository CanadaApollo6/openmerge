package forge

import (
	"context"
	"encoding/hex"
	"slices"
	"time"
)

// RevisionDiffReader observes immutable git inputs, independently of PR metadata.
// BaseSHA is the pinned queue-base tip, not a speculative build base. Callers
// retain it across forward landings and supply a new pin after a base rewrite.
// A nil error never implies completeness: inspect Inventory and BinaryKnown.
// Invalid IDs, missing objects and read failures return a non-nil diagnostic
// error. Other limitations return nil errors with incomplete coverage. Typed
// Reasons remain suitable for evidence; complete inventory has no Reasons.
type RevisionDiffReader interface {
	RevisionDiff(context.Context, RepoRef, RevisionDiffRequest) (RevisionDiff, error)
}

// RevisionDiffRequest names full immutable commit IDs, never refs or tags.
type RevisionDiffRequest struct{ BaseSHA, HeadSHA string }

// DiffSemantics fixes exact-object rename pairing and binary detection (NUL in
// either blob). No heuristic similarity, external diff, textconv or attributes
// participate. Complete inventories require unaltered, verified divergence ancestry.
// Consumers include this version in their classifier version.
const DiffSemantics = "git-evidence-v6-unified-hunks"

// Inventory resource limits are part of DiffSemantics.
const (
	MaxDiffFiles = 1000
	// MaxAncestryCommits allows unusually large PR divergences while bounding
	// verification work independently of repository age.
	MaxAncestryCommits = 100_000
	// MaxAncestryDuration bounds merge-base discovery and verification together,
	// including traversal work that emits no commits.
	MaxAncestryDuration = 30 * time.Second
	// MaxDiffBytes bounds raw inventory plus all inspected blob bytes per read.
	MaxDiffBytes = 2 << 20
	// Hunk text has a separate budget from inspected inventory blobs.
	MaxDiffHunkBytes        = 2 << 20
	MaxDiffHunkLinesPerFile = 200
	DiffHunkContext         = 3
	MaxDiffHunkInputBytes   = 2 << 20
	MaxDiffHunkOutputBytes  = 8*MaxDiffHunkInputBytes + 4096
	MaxDiffHunkDuration     = 2 * time.Second
	MaxDiffHunksDuration    = 30 * time.Second
)

// DiffCompleteness distinguishes full evidence from partial or unknown reads.
type DiffCompleteness string

// Coverage states; the zero value is unknown.
const (
	DiffComplete   DiffCompleteness = "complete"
	DiffIncomplete DiffCompleteness = "incomplete"
)

// DiffFailureReason is a stable machine-readable observation limitation.
type DiffFailureReason string

// Observation failure and uncertainty reasons.
const (
	DiffInvalidObjectID     DiffFailureReason = "invalid_object_id"
	DiffMissingObject       DiffFailureReason = "missing_object"
	DiffReadFailure         DiffFailureReason = "read_failure"
	DiffNoMergeBase         DiffFailureReason = "no_merge_base"
	DiffMultipleMergeBases  DiffFailureReason = "multiple_merge_bases"
	DiffShallowHistory      DiffFailureReason = "shallow_history"
	DiffAlteredHistory      DiffFailureReason = "altered_history"
	DiffAncestryLimit       DiffFailureReason = "ancestry_limit"
	DiffFileLimit           DiffFailureReason = "file_limit"
	DiffByteLimit           DiffFailureReason = "byte_limit"
	DiffUnpairedRename      DiffFailureReason = "unpaired_rename"
	DiffHunksNotCollected   DiffFailureReason = "hunks_not_collected"
	DiffHunkLineLimit       DiffFailureReason = "hunk_line_limit"
	DiffHunkWorkLimit       DiffFailureReason = "hunk_work_limit"
	DiffHunkByteLimit       DiffFailureReason = "hunk_byte_limit"
	DiffNonText             DiffFailureReason = "non_text"
	DiffSpecialMode         DiffFailureReason = "special_mode"
	DiffInvalidUTF8         DiffFailureReason = "invalid_utf8"
	DiffInventoryIncomplete DiffFailureReason = "inventory_incomplete"
)

// DiffCoverage is deliberately not a bool: its zero value is unknown.
type DiffCoverage struct {
	Completeness DiffCompleteness
	Reasons      []DiffFailureReason
}

// RevisionDiff retains requested endpoints even on failure. MergeBaseSHA is
// populated only after proving a unique merge base. Files are in deterministic
// path order. Partial inventory is evidence of observed paths, never absence.
// Hunks has independent coverage, so a complete inventory is not a full brief.
type RevisionDiff struct {
	BaseSHA, HeadSHA, MergeBaseSHA string
	Semantics                      string
	Files                          []DiffFile
	Inventory, Hunks               DiffCoverage
}

// DiffRename records pairing evidence without guessing similarity.
type DiffRename string

// Rename evidence states.
const (
	DiffNotRenamed     DiffRename = "none"
	DiffExactRename    DiffRename = "exact"
	DiffRenameUnpaired DiffRename = "unpaired"
)

// DiffFile uses empty path/mode/object ID for an absent side. Modes are Git's
// six-digit octal strings, including 120000 (symlink) and 160000 (gitlink).
// Object IDs pin future hunk reads without consulting branch names again.
type DiffFile struct {
	OldPath, NewPath               string
	OldMode, NewMode               string
	OldObjectID, NewObjectID       string
	Binary, BinaryKnown, Submodule bool
	Rename                         DiffRename
	Hunks                          []DiffHunk
	HunkCoverage                   DiffCoverage
	Omitted                        []DiffHunkRange
}

// DiffHunk contains UTF-8 unified body text (no file headers) and blob ranges.
type DiffHunk struct {
	OldStart, OldLines, NewStart, NewLines int
	Text                                   string
}

// DiffHunkRange explains an omitted span. Zero ranges mean unavailable, not empty.
type DiffHunkRange struct {
	OldStart, OldLines, NewStart, NewLines int
	Reason                                 DiffFailureReason
}

// NewRevisionDiff starts with unknown inventory and explicitly absent hunks.
func NewRevisionDiff(req RevisionDiffRequest) RevisionDiff {
	return RevisionDiff{BaseSHA: req.BaseSHA, HeadSHA: req.HeadSHA, Semantics: DiffSemantics,
		Inventory: DiffCoverage{Completeness: DiffIncomplete},
		Hunks:     DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{DiffHunksNotCollected}}}
}

// ValidDiffObjectID accepts full SHA-1 and SHA-256 hexadecimal object IDs.
func ValidDiffObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// PairExactDiffRenames pairs only unique equal-object add/delete candidates.
// Remaining add/delete pairs are explicitly uncertain; similarity is not guessed.
func PairExactDiffRenames(files []DiffFile) []DiffFile {
	adds, deletes := map[string][]int{}, map[string][]int{}
	for i, f := range files {
		if f.OldPath == "" {
			adds[f.NewObjectID] = append(adds[f.NewObjectID], i)
		}
		if f.NewPath == "" {
			deletes[f.OldObjectID] = append(deletes[f.OldObjectID], i)
		}
	}
	removed := map[int]bool{}
	for id, aa := range adds {
		dd := deletes[id]
		if len(aa) != 1 || len(dd) != 1 {
			continue
		}
		a, d := aa[0], dd[0]
		files[a].OldPath, files[a].OldMode, files[a].OldObjectID = files[d].OldPath, files[d].OldMode, files[d].OldObjectID
		files[a].Rename = DiffExactRename
		files[a].Submodule = files[a].Submodule || files[d].Submodule
		removed[d] = true
	}
	result := make([]DiffFile, 0, len(files))
	hasAdd, hasDelete := false, false
	for i, f := range files {
		if removed[i] {
			continue
		}
		hasAdd = hasAdd || f.OldPath == ""
		hasDelete = hasDelete || f.NewPath == ""
		result = append(result, f)
	}
	if hasAdd && hasDelete {
		for i := range result {
			if result[i].OldPath == "" || result[i].NewPath == "" {
				result[i].Rename = DiffRenameUnpaired
			}
		}
	}
	slices.SortFunc(result, func(a, b DiffFile) int {
		key := func(f DiffFile) string {
			if f.NewPath != "" {
				return f.NewPath
			}
			return f.OldPath
		}
		if key(a) < key(b) {
			return -1
		}
		if key(a) > key(b) {
			return 1
		}
		return 0
	})
	return result
}
