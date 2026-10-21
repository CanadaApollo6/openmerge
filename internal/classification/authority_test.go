package classification

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestAuthorityGoverningOwnerCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, owners string
		mutate       func(*forge.GoverningFiles)
		valid        bool
	}{
		{name: "covered", owners: "* @alice\n", valid: true},
		{name: "specific config misses owners", owners: "/.openmerge.yml @alice\n"},
		{name: "owner removal", owners: "* @alice\n/.github/CODEOWNERS\n"},
		{name: "unresolved", owners: "* @missing\n"},
		{name: "email only", owners: "* owner@example.com\n"},
		{name: "invalid pattern", owners: "[bad @alice\n"},
		{name: "hidden probe", owners: "* @alice\n", mutate: func(g *forge.GoverningFiles) { g.Codeowners[0].Coverage = forge.DiffCoverage{} }},
		{name: "incomplete", owners: "* @alice\n", mutate: func(g *forge.GoverningFiles) { g.Coverage = forge.DiffCoverage{} }},
		{name: "wrong commit", owners: "* @alice\n", mutate: func(g *forge.GoverningFiles) { g.CommitSHA = "wrong" }},
		{name: "ambiguous config", owners: "* @alice\n", mutate: func(g *forge.GoverningFiles) { put(&g.Config, ".openmerge.yaml", "version: 1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, _, g := basis(t, ".openmerge.yml")
			put(&g.Config, ".openmerge.yml", "version: 1")
			ownersSource(&g, ".github/CODEOWNERS", tc.owners)
			if tc.mutate != nil {
				tc.mutate(&g)
			}
			err := ValidateAuthorityOwners(i.GoverningCommit, g, observedResolver)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorityPolicyRemovalUsesGoverningOwners(t *testing.T) {
	i, d, g := basis(t, ".github/CODEOWNERS")
	ownersSource(&g, ".github/CODEOWNERS", "* @alice\n")
	// The PR's new blob can remove all owners; only the default-branch content
	// above is supplied to classification and fresh eligibility resolution.
	e := derive(t, i, d, g, Settings{})
	owners := ResolveOwnerEligibility(e, observedResolver)
	if !owners.PolicyOwnersKnown || len(owners.PolicyOwners) != 1 || len(owners.PolicyOwners[0].Eligible.IDs) != 1 || owners.PolicyOwners[0].Eligible.IDs[0] != GitHubUserID(1) {
		t.Fatalf("governing owners lost: %+v", owners)
	}
}
