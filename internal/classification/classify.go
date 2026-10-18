// Package classification derives dormant Stage A evidence without I/O.
package classification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// HistoricalVersion pins the original durable derivation, independently of future forge changes.
const HistoricalVersion = "classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v1"

// Version includes acquisition semantics and the classifier facet/path rules.
const Version = "classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v3"

// PreviousVersion retains the 4a facet/path semantics for replay.
const PreviousVersion = "classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v2"

// Settings is component-only; it does not extend the public config loader.
type Settings struct{ Docs, Generated, Sensitive []string }

// Deriver is 4b's seam. Implementations parse only the supplied governing tree.
// Generated resolves the attribute half only after Classify checks ancestor coverage.
// Owners fills the shared owner evidence and owned: facts, retaining uncertainty.
// Owners must record explicit False for every absent governing owner token.
// Classify enforces unknown ownership and rejects protected evidence mutations.
// Missing owned: facts remain unknown even on complete inventories.
// It must not change inventory, inputs, paths, hunks or non-owner facts.
type Deriver interface {
	Generated(path string, governing forge.GoverningFiles) cp.Truth
	Owners(classification *cp.Classification, governing forge.GoverningFiles)
}

// Evidence retains bounded source DTOs without expanding omitted ranges.
// Classification.Files[].Hunks is the durable bounded hunk evidence subrecord.
// Governing preserves explicit checked absences and incomplete probes.
type Evidence struct {
	Inventory, Hunks forge.DiffCoverage
	InputFingerprint string
	Classification   cp.Classification
	Governing        forge.GoverningFiles
}

// Classify requires an explicit default-branch governing commit, including for
// release queues. Callers pin Base at first observation, never a build base.
func Classify(inputs cp.ClassificationInputs, diff forge.RevisionDiff, governing forge.GoverningFiles, settings Settings, derived Deriver) (Evidence, error) {
	inputs.ClassifierVersion = Version
	if !inputs.Recorded() || inputs.GoverningCommit == "" || inputs.Base != diff.BaseSHA || inputs.Head != diff.HeadSHA || inputs.MergeBase != diff.MergeBaseSHA || inputs.GoverningCommit != governing.CommitSHA {
		return Evidence{}, fmt.Errorf("classification requires matching pinned revision and governing inputs")
	}
	for _, commit := range []string{inputs.Base, inputs.Head, inputs.MergeBase, inputs.GoverningCommit} {
		if !forge.ValidDiffObjectID(commit) {
			return Evidence{}, fmt.Errorf("classification requires full commit IDs")
		}
	}
	if diff.Semantics != forge.DiffSemantics || governing.Semantics != forge.DiffSemantics {
		return Evidence{}, fmt.Errorf("unsupported evidence semantics")
	}
	// A call-local cache avoids reparsing ancestor files for each path/side.
	switch d := derived.(type) {
	case TreeDeriver:
		derived = d.prepared(governing)
	case *TreeDeriver:
		if d != nil {
			derived = d.prepared(governing)
		}
	}
	// Detach all mutable governing collections before calling an injected Deriver.
	derivedGoverning := cloneGoverning(governing)
	governingFingerprint := fingerprint(derivedGoverning)
	docs, err := compile(settings.Docs)
	if err != nil {
		return Evidence{}, err
	}
	generated, err := compile(settings.Generated)
	if err != nil {
		return Evidence{}, err
	}
	sensitive, err := compile(settings.Sensitive)
	if err != nil {
		return Evidence{}, err
	}
	// Derive provenance from probes, never from caller claims. Empty object IDs
	// alone do not prove absence; that proof remains in Evidence.Governing.
	inputs.ConfigBlob, inputs.OwnersBlob = "", ""
	inputs.AttributeBlobs = make(map[string]string)
	configID, configKnown := configBlob(governing)
	inputs.ConfigBlob = configID
	for _, f := range governing.Codeowners {
		if f.Path == governing.SelectedCodeowners && f.Present {
			inputs.OwnersBlob = f.ObjectID
		}
	}
	for _, f := range governing.Attributes {
		if f.Present {
			inputs.AttributeBlobs[f.Path] = f.ObjectID
		}
	}
	c := cp.Classification{Inputs: inputs, InventoryComplete: diff.Inventory.Completeness == forge.DiffComplete, PathsSafe: diff.Inventory.Completeness == forge.DiffComplete, HunksComplete: diff.Hunks.Completeness == forge.DiffComplete, Facts: map[string]cp.Truth{"docs": cp.True, "generated": cp.True, "policy": cp.False, "unknown": cp.False}}
	reason := func(label, why string) {
		c.Reasons = append(c.Reasons, cp.LabelReasons{Label: label, Reasons: []string{why}})
	}
	uncertain := func(why string) { c.Facts["unknown"] = cp.True; reason("unknown", why) }
	if !configKnown {
		uncertain("config probes incomplete or multiple configuration files present")
	}
	if !c.InventoryComplete {
		uncertain("inventory incomplete: split the change")
	}
	for _, r := range diff.Inventory.Reasons {
		uncertain(string(r))
	}
	for _, r := range diff.Hunks.Reasons {
		if r == forge.DiffReadFailure {
			uncertain("hunk read failure")
		}
	}
	if governing.Coverage.Completeness != forge.DiffComplete {
		uncertain("governing files incomplete")
	}
	for _, f := range diff.Files {
		oldMode, oldErr := strconv.ParseUint(f.OldMode, 8, 32)
		newMode, newErr := strconv.ParseUint(f.NewMode, 8, 32)
		safe := f.BinaryKnown && !f.Binary && !f.Submodule && f.Rename != forge.DiffRenameUnpaired
		for _, mode := range []string{f.OldMode, f.NewMode} {
			if mode != "" && mode != "100644" && mode != "100755" {
				safe = false
			}
		}
		if f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode {
			safe = false
		}
		if (f.OldPath != "" && oldErr != nil) || (f.NewPath != "" && newErr != nil) {
			safe = false
		}
		file := cp.FileEvidence{OldPath: f.OldPath, NewPath: f.NewPath, OldBlob: f.OldObjectID, NewBlob: f.NewObjectID, OldMode: uint32(oldMode), NewMode: uint32(newMode), Renamed: f.OldPath != "" && f.NewPath != "" && f.OldPath != f.NewPath, RenamePaired: f.Rename == forge.DiffExactRename, Binary: cp.Unknown, Submodule: truth(f.Submodule), Facts: map[string]cp.Truth{"docs": cp.True, "generated": cp.True, "policy": cp.False}, Hunks: cp.HunkEvidence{Complete: f.HunkCoverage.Completeness == forge.DiffComplete}}
		if f.BinaryKnown {
			file.Binary = truth(f.Binary)
		}
		paths := []string{f.OldPath}
		if f.NewPath != f.OldPath {
			paths = append(paths, f.NewPath)
		}
		for _, p := range paths {
			if p == "" {
				continue
			}
			c.Paths = append(c.Paths, p)
			if !matches(docs, p) {
				file.Facts["docs"] = cp.False
			}
			g := cp.Unknown
			if derived != nil && safe && attributesCovered(p, governing) {
				g = derived.Generated(p, derivedGoverning)
			}
			if !attributesCovered(p, governing) {
				uncertain("attribute probe coverage incomplete: " + p)
			}
			if matches(generated, p) {
				g = cp.True
			}
			file.Facts["generated"] = all(file.Facts["generated"], g)
			if policyPath(p) || matches(sensitive, p) {
				file.Facts["policy"] = cp.True
				file.Reasons = append(file.Reasons, cp.LabelReasons{Label: "policy", Reasons: []string{p}})
			}
		}
		if !safe {
			uncertain("unsafe file: " + firstPath(f))
			file.Reasons = append(file.Reasons, cp.LabelReasons{Label: "unknown", Reasons: []string{"binary, unreadable, mode, submodule or unpaired rename"}})
		}
		for _, label := range []string{"docs", "generated"} {
			c.Facts[label] = all(c.Facts[label], file.Facts[label])
		}
		if file.Facts["policy"] == cp.True {
			c.Facts["policy"] = cp.True
			for _, r := range file.Reasons {
				if r.Label == "policy" {
					for _, p := range r.Reasons {
						reason("policy", p)
					}
				}
			}
		}
		for _, h := range f.Hunks {
			file.Hunks.Hunks = append(file.Hunks.Hunks, cp.Hunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Text: h.Text})
		}
		for _, h := range f.Omitted {
			file.Hunks.Omitted = append(file.Hunks.Omitted, cp.HunkRange{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Reason: string(h.Reason), Count: h.Count})
		}
		c.HunksComplete = c.HunksComplete && file.Hunks.Complete
		c.Files = append(c.Files, file)
	}
	slices.Sort(c.Paths)
	c.Paths = slices.Compact(c.Paths)
	if !c.InventoryComplete {
		c.Facts["docs"], c.Facts["generated"] = cp.Unknown, cp.Unknown
		c.HunksComplete = false
		if c.Facts["policy"] != cp.True {
			c.Facts["policy"] = cp.Unknown
		}
	}
	if len(c.Files) == 0 {
		c.Facts["docs"], c.Facts["generated"] = cp.Unknown, cp.Unknown
	}
	if derived != nil {
		before := protectedClassification(c)
		beforeUnknown := c.Facts["unknown"]
		beforeReasons := protectedReasons(c)
		derived.Owners(&c, derivedGoverning)
		if !reasonsPreserved(beforeReasons, c) || before != protectedClassification(c) || beforeUnknown != c.Facts["unknown"] && c.Facts["unknown"] != cp.True || fingerprint(derivedGoverning) != governingFingerprint {
			return Evidence{}, fmt.Errorf("Deriver changed protected classification or governing evidence")
		}
	} else {
		uncertain("owners not yet derived (Stage A 4b)")
	}
	if _, ok := selectedOwners(governing); !ok {
		c.CodeOwnersKnown, c.PolicyOwnersKnown = false, false
		for label := range c.Facts {
			if strings.HasPrefix(label, "owned:") {
				c.Facts[label] = cp.Unknown
			}
		}

		for i := range c.Files {
			for label := range c.Files[i].Facts {
				if strings.HasPrefix(label, "owned:") {
					c.Files[i].Facts[label] = cp.Unknown
				}
			}
		}
		for i := range c.CodeOwners {
			c.CodeOwners[i].Status = cp.OwnersUnresolved
			c.CodeOwners[i].Eligible.Resolved = false
		}
		for i := range c.PolicyOwners {
			c.PolicyOwners[i].Eligible.Resolved = false
		}
		uncertain("CODEOWNERS probe sequence incomplete")
	}
	if !c.InventoryComplete {
		c.CodeOwnersKnown, c.PolicyOwnersKnown = false, false
	}
	if !c.CodeOwnersKnown || c.Facts["policy"] != cp.False && !c.PolicyOwnersKnown {
		uncertain("effective owners unresolved")
	}
	if c.Facts["generated"] == cp.Unknown {
		uncertain("generated attributes unavailable, unsupported or unreadable")
	}
	for _, label := range []string{"docs", "generated"} {
		reason(label, map[cp.Truth]string{cp.True: "every changed path qualifies", cp.False: "a changed path does not qualify", cp.Unknown: "qualification is unresolved"}[c.Facts[label]])
	}
	evidence := Evidence{Classification: c, Governing: governing, Inventory: diff.Inventory, Hunks: diff.Hunks}
	// This fingerprint records provenance as well as semantic inputs. It is not a
	// brief/policy digest: provenance-only refreshes must not invalidate receipts.
	evidence.InputFingerprint = fingerprint(struct {
		Inputs    cp.ClassificationInputs
		Diff      forge.RevisionDiff
		Governing forge.GoverningFiles
		Settings  Settings
	}{inputs, diff, governing, settings})
	return evidence, nil
}

func fingerprint(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func truth(b bool) cp.Truth {
	if b {
		return cp.True
	}
	return cp.False
}
func all(a, b cp.Truth) cp.Truth {
	if a == cp.False || b == cp.False {
		return cp.False
	}
	if a == cp.Unknown || b == cp.Unknown {
		return cp.Unknown
	}
	return cp.True
}
func compile(patterns []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range patterns {
		r, err := condition.CompileGlob(p)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func matches(patterns []*regexp.Regexp, p string) bool {
	for _, r := range patterns {
		if r.MatchString(p) {
			return true
		}
	}
	return false
}
func firstPath(f forge.DiffFile) string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}
func policyPath(p string) bool {
	switch p {
	case ".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml", ".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS":
		return true
	}
	return path.Base(p) == ".gitattributes" || (path.Dir(p) == ".github/workflows" && (path.Ext(p) == ".yml" || path.Ext(p) == ".yaml"))
}
