package queue

import (
	"fmt"
	"path"
	"slices"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func classificationPath(p string) bool {
	return p != "" && forge.ValidDiffPath(p) && p != "." && p != ".." && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && !strings.ContainsRune(p, 0) && path.Clean(p) == p
}
func classificationObject(id string) bool { return id == "" || forge.ValidDiffObjectID(id) }
func classificationMode(mode uint32) bool {
	return mode == 0 || mode == 0100644 || mode == 0100755 || mode == 0120000 || mode == 0160000 || mode == 0040000
}
func classificationReason(r forge.DiffFailureReason) bool {
	switch r {
	case forge.DiffInvalidObjectID, forge.DiffMissingObject, forge.DiffReadFailure, forge.DiffNoMergeBase, forge.DiffMultipleMergeBases, forge.DiffShallowHistory, forge.DiffAlteredHistory, forge.DiffAncestryLimit, forge.DiffFileLimit, forge.DiffByteLimit, forge.DiffUnpairedRename, forge.DiffHunksNotCollected, forge.DiffHunkLineLimit, forge.DiffHunkWorkLimit, forge.DiffHunkByteLimit, forge.DiffHunkCountLimit, forge.DiffOmittedRangeLimit, forge.DiffNonText, forge.DiffSpecialMode, forge.DiffInvalidUTF8, forge.DiffInventoryIncomplete:
		return true
	}
	return false
}
func classificationCoverage(c forge.DiffCoverage, allowHunkCount bool) bool {
	if c.Completeness != "" && c.Completeness != forge.DiffComplete && c.Completeness != forge.DiffIncomplete {
		return false
	}
	for _, r := range c.Reasons {
		if !classificationReason(r) || r == forge.DiffHunkCountLimit && !allowHunkCount {
			return false
		}
	}
	return c.Completeness != forge.DiffComplete || len(c.Reasons) == 0
}
func classificationFacts(facts map[string]cp.Truth, reasons []cp.LabelReasons) bool {
	for key, value := range facts {
		if !cp.ValidClassificationLabel(key) || value > cp.True {
			return false
		}
	}
	for _, r := range reasons {
		if !cp.ValidClassificationLabel(r.Label) {
			return false
		}
	}
	return true
}
func classificationRange(a, b, c, d int) bool {
	// Also prevent overflow when consumers compute the exclusive range end.
	return a >= 0 && b >= 0 && c >= 0 && d >= 0 && a <= int(^uint(0)>>1)-b && c <= int(^uint(0)>>1)-d
}

// Version-keyed replay rules pin recorded meaning independently of current derivation.
var classificationVersions = map[string]struct {
	diffSemantics     string
	pathDerivedRename bool
}{
	classification.HistoricalVersion:       {"git-evidence-v7-bounded-omissions", false},
	classification.PreviousVersion:         {"git-evidence-v7-bounded-omissions", true},
	classification.RetainedVersion:         {"git-evidence-v7-bounded-omissions", true},
	classification.Version:                 {forge.DiffSemantics, true},
	classification.UnboundedPatternVersion: {forge.DiffSemantics, true},
}

// Historical versions remain explicit: facets-v1 could record the old rename
// flag for a single-sided unpaired rename. Never reinterpret stored derivation.
func validateClassificationValues(e classification.Evidence) error {
	c, g := e.Classification, e.Governing
	invalid := func() error { return fmt.Errorf("invalid nested classification evidence") }
	version, ok := classificationVersions[c.Inputs.ClassifierVersion]
	if !ok {
		return invalid()
	}
	semantics := version.diffSemantics
	allowHunkCount := semantics == forge.DiffSemantics
	if semantics == forge.DiffSemantics {
		if err := validateRetainedHunks(c); err != nil {
			return err
		}
	}
	if g.Semantics != semantics || !classificationCoverage(e.Inventory, allowHunkCount) || !classificationCoverage(e.Hunks, allowHunkCount) || !classificationCoverage(g.Coverage, allowHunkCount) {
		return invalid()
	}
	if !classificationObject(c.Inputs.ConfigBlob) || !classificationObject(c.Inputs.OwnersBlob) {
		return invalid()
	}
	for p, id := range c.Inputs.AttributeBlobs {
		if !classificationPath(p) || !classificationObject(id) {
			return invalid()
		}
	}
	if !classificationFacts(c.Facts, c.Reasons) {
		return invalid()
	}
	if g.SelectedCodeowners != "" && !slices.Contains([]string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}, g.SelectedCodeowners) {
		return invalid()
	}
	for kind, files := range [][]forge.GoverningFile{g.Config, g.Codeowners, g.Attributes} {
		for _, f := range files {
			if !classificationPath(f.Path) || !classificationObject(f.ObjectID) || !classificationCoverage(f.Coverage, allowHunkCount) {
				return invalid()
			}
			switch kind {
			case 0:
				if !slices.Contains([]string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml"}, f.Path) {
					return invalid()
				}
			case 1:
				if !slices.Contains([]string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}, f.Path) {
					return invalid()
				}
			case 2:
				if path.Base(f.Path) != ".gitattributes" {
					return invalid()
				}
			}
			if !slices.Contains([]string{"", "100644", "100755", "120000", "160000", "040000"}, f.Mode) {
				return invalid()
			}
			if !f.Present && (f.ObjectID != "" || f.Mode != "" || f.Content != "") {
				return invalid()
			}
			if f.Present && f.Coverage.Completeness == forge.DiffComplete && (!forge.ValidDiffObjectID(f.ObjectID) || f.Mode == "") {
				return invalid()
			}
		}
	}
	paths := map[string]bool{}
	hunksComplete := c.InventoryComplete && e.Hunks.Completeness == forge.DiffComplete
	for _, f := range c.Files {
		if !classificationFacts(f.Facts, f.Reasons) || !classificationObject(f.OldBlob) || !classificationObject(f.NewBlob) || !classificationMode(f.OldMode) || !classificationMode(f.NewMode) {
			return invalid()
		}
		if f.OldPath == "" && f.NewPath == "" {
			return invalid()
		}
		for _, side := range []struct {
			p, id string
			mode  uint32
		}{{f.OldPath, f.OldBlob, f.OldMode}, {f.NewPath, f.NewBlob, f.NewMode}} {
			if side.p == "" {
				if side.id != "" || side.mode != 0 {
					return invalid()
				}
				continue
			}
			if !classificationPath(side.p) {
				return invalid()
			}
			paths[side.p] = true
		}
		renamed := f.OldPath != "" && f.NewPath != "" && f.OldPath != f.NewPath
		if version.pathDerivedRename && (f.Renamed != renamed || f.RenamePaired && !renamed) {
			return invalid()
		}
		hunksComplete = hunksComplete && f.Hunks.Complete
		if f.Hunks.Complete && len(f.Hunks.Omitted) != 0 {
			return invalid()
		}
		for _, h := range f.Hunks.Hunks {
			if !classificationRange(h.OldStart, h.OldLines, h.NewStart, h.NewLines) || h.OldLines == 0 && h.NewLines == 0 || h.OldLines > 0 && h.OldStart == 0 || h.NewLines > 0 && h.NewStart == 0 {
				return invalid()
			}
		}
		for _, h := range f.Hunks.Omitted {
			if !classificationRange(h.OldStart, h.OldLines, h.NewStart, h.NewLines) {
				return invalid()
			}
			switch forge.DiffFailureReason(h.Reason) {
			case forge.DiffOmittedRangeLimit:
				if h.Count <= 0 {
					return invalid()
				}
			case forge.DiffHunkLineLimit, forge.DiffHunkByteLimit:
				if h.Count != 0 || h.OldLines == 0 && h.NewLines == 0 || h.OldLines > 0 && h.OldStart == 0 || h.NewLines > 0 && h.NewStart == 0 {
					return invalid()
				}
			case forge.DiffHunkCountLimit:
				if !allowHunkCount || h.Count != 0 || h.OldLines == 0 && h.NewLines == 0 || h.OldLines > 0 && h.OldStart == 0 || h.NewLines > 0 && h.NewStart == 0 {
					return invalid()
				}
			case forge.DiffSpecialMode, forge.DiffNonText, forge.DiffReadFailure, forge.DiffInvalidUTF8, forge.DiffFileLimit, forge.DiffHunkWorkLimit:
				if h.Count != 0 || h.OldStart != 0 || h.OldLines != 0 || h.NewStart != 0 || h.NewLines != 0 {
					return invalid()
				}
			default:
				return invalid()
			}
		}
	}
	if c.HunksComplete != hunksComplete {
		return invalid()
	}
	if len(paths) != len(c.Paths) {
		return invalid()
	}
	for _, p := range c.Paths {
		if !paths[p] {
			return invalid()
		}
		delete(paths, p)
	}
	for _, owner := range c.CodeOwners {
		if !classificationPath(owner.Path) || owner.Side != cp.OldSide && owner.Side != cp.NewSide {
			return invalid()
		}
		switch owner.Status {
		case cp.OwnersUnresolved, cp.OwnersUncovered, cp.OwnersOwnerless, cp.OwnersResolved:
		default:
			return invalid()
		}
		for _, id := range owner.Eligible.IDs {
			if strings.TrimSpace(id) == "" {
				return invalid()
			}
		}
	}
	for _, owner := range c.PolicyOwners {
		if !classificationPath(owner.Path) {
			return invalid()
		}
		for _, id := range owner.Eligible.IDs {
			if strings.TrimSpace(id) == "" {
				return invalid()
			}
		}
	}
	return nil
}
