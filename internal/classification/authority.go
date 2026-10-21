package classification

import (
	"fmt"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ValidateAuthorityOwners requires governing owners for every supported config
// and CODEOWNERS location, including absent paths that could replace policy or
// precedence later. Only the pinned default-branch tree can authorize its edit.
func ValidateAuthorityOwners(commit string, g forge.GoverningFiles, resolve OwnerResolver) error {
	if !forge.ValidDiffObjectID(commit) || g.CommitSHA != commit || g.Semantics != forge.DiffSemantics || g.Coverage.Completeness != forge.DiffComplete || len(g.Coverage.Reasons) != 0 {
		return fmt.Errorf("authority governing evidence is incomplete or does not match commit")
	}
	if blob, known := configBlob(g); !known || blob == "" {
		return fmt.Errorf("authority requires exactly one complete governing config")
	}
	source, known := selectedOwners(g)
	if !known || !source.Present {
		return fmt.Errorf("authority requires complete governing CODEOWNERS")
	}
	paths := []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml", ".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
	c := cp.Classification{InventoryComplete: true, PathsSafe: true, Paths: paths, Facts: map[string]cp.Truth{"policy": cp.True}}
	for _, path := range paths {
		c.Files = append(c.Files, cp.FileEvidence{NewPath: path, Facts: map[string]cp.Truth{"policy": cp.True}})
	}
	TreeDeriver{Resolve: resolve}.Owners(&c, g)
	if !c.PolicyOwnersKnown || len(c.PolicyOwners) != len(paths) {
		return fmt.Errorf("authority governing policy owners are unresolved or incomplete")
	}
	if err := cp.ValidateOwnerEvidence(c); err != nil {
		return fmt.Errorf("authority governing owner evidence: %w", err)
	}
	for _, owner := range c.PolicyOwners {
		if !owner.Eligible.Resolved || !owner.Eligible.Fresh || len(owner.Eligible.IDs) == 0 {
			return fmt.Errorf("authority requires eligible governing owners for %s", owner.Path)
		}
	}
	return nil
}
