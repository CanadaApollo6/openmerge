package authority

import "testing"

func TestEffectiveDigestPreservesGitHubBytes(t *testing.T) {
	const legacy = "legacy effective digest is opaque here"
	for _, policy := range []Policy{{}, {Mode: GitHub}} {
		got, err := EffectiveDigest(DigestInputs{LegacyDigest: legacy, Policy: policy})
		if err != nil || got != legacy {
			t.Fatalf("github digest = %q, %v; want original bytes", got, err)
		}
	}
	if _, err := EffectiveDigest(DigestInputs{
		LegacyDigest: legacy,
		Policy:       Policy{Mode: GitHub, BreakGlass: validBinding()},
	}); err == nil {
		t.Fatal("github policy accepted dormant break-glass actor")
	}
}

func TestEffectiveDigestBindsAuthorityPolicy(t *testing.T) {
	base := DigestInputs{
		LegacyDigest:  "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa",
		Policy:        Policy{Mode: OpenMerge, BreakGlass: validBinding()},
		DefaultBranch: "main",
		MergeMethod:   Squash,
		Selectors: []BaseSelector{
			{DefaultBranch: true},
			{Pattern: "release/**"},
		},
	}
	want, err := EffectiveDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if want == base.LegacyDigest {
		t.Fatal("openmerge digest retained legacy policy identity")
	}
	reordered := cloneDigestInputs(base)
	reordered.Selectors = []BaseSelector{{Pattern: "release/**"}, {DefaultBranch: true}}
	if got, err := EffectiveDigest(reordered); err != nil || got != want {
		t.Fatalf("selector order changed digest: %q, %v", got, err)
	}
	duplicates := cloneDigestInputs(base)
	duplicates.Selectors = append(duplicates.Selectors, BaseSelector{Pattern: "release/**"})
	if got, err := EffectiveDigest(duplicates); err != nil || got != want {
		t.Fatalf("duplicate selector changed coverage digest: %q, %v", got, err)
	}
	for name, changed := range map[string]DigestInputs{
		"declared actor": func() DigestInputs {
			in := cloneDigestInputs(base)
			in.Policy.BreakGlass.Declared.Team += "-rotated"
			return in
		}(),
		"actor": func() DigestInputs {
			in := cloneDigestInputs(base)
			in.Policy.BreakGlass.Resolved.Team.TeamID = "43"
			return in
		}(),
		"default branch": func() DigestInputs { in := cloneDigestInputs(base); in.DefaultBranch = "trunk"; return in }(),
		"merge method":   func() DigestInputs { in := cloneDigestInputs(base); in.MergeMethod = Rebase; return in }(),
		"selector": func() DigestInputs {
			in := cloneDigestInputs(base)
			in.Selectors[1].Pattern = "release/*"
			return in
		}(),
	} {
		if got, err := EffectiveDigest(changed); err != nil || got == want {
			t.Fatalf("%s did not change authority digest: %q, %v", name, got, err)
		}
	}
	if !(Policy{Mode: OpenMerge}).RequiresPolicyOwnerApproval() || (Policy{Mode: GitHub}).RequiresPolicyOwnerApproval() {
		t.Fatal("authority owner semantics changed")
	}
	if _, err := EffectiveDigest(DigestInputs{LegacyDigest: base.LegacyDigest, Policy: base.Policy, DefaultBranch: "main", MergeMethod: Squash, Selectors: []BaseSelector{{Pattern: ""}}}); err == nil {
		t.Fatal("empty explicit selector accepted")
	}
}

func cloneDigestInputs(in DigestInputs) DigestInputs {
	result := in
	result.Selectors = append([]BaseSelector{}, in.Selectors...)
	if team := in.Policy.BreakGlass.Resolved.Team; team != nil {
		cloned := *team
		result.Policy.BreakGlass.Resolved.Team = &cloned
	}
	if role := in.Policy.BreakGlass.Resolved.RepositoryAdmin; role != nil {
		cloned := *role
		result.Policy.BreakGlass.Resolved.RepositoryAdmin = &cloned
	}
	return result
}

func validBinding() ActorBinding {
	return ActorBinding{
		Declared: Actor{Team: "Smart-Data-Ohio/break-glass"},
		Resolved: ResolvedActor{Team: &ResolvedTeam{OrganizationID: "1301", TeamID: "42"}},
	}
}

func TestAuthorityDigestGoldenVectors(t *testing.T) {
	in := DigestInputs{LegacyDigest: "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa", Policy: Policy{Mode: OpenMerge, BreakGlass: validBinding()}, DefaultBranch: "main", MergeMethod: Squash, Selectors: []BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}}
	got, err := CoverageDigest(in.DefaultBranch, in.Selectors)
	if err != nil || got != "fd80dfd6b30d4f40b38c658f474a22271971a3061b61039405843279e3120b11" {
		t.Fatal("coverage digest vector", got, err)
	}
	got, err = EffectiveDigest(in)
	if err != nil || got != "77572cd15720ef8ecca45bcacf40ac250b2ae5d0a33271e23ed23e8e3d2c3633" {
		t.Fatal("team digest vector", got, err)
	}
	in.Policy.BreakGlass = ActorBinding{Declared: Actor{RepositoryAdmin: true}, Resolved: ResolvedActor{RepositoryAdmin: &ResolvedRepositoryAdmin{RoleID: "adapter-resolved-admin-role"}}}
	got, err = EffectiveDigest(in)
	if err != nil || got != "cca675071fe1f13b3b97fae09fcbb4972159377f62a00622ed74f1b8e568e9fc" {
		t.Fatal("admin digest vector", got, err)
	}
}
