package changepolicy

import (
	"strings"
	"testing"
)

func requireIncoherentOwners(t *testing.T, got Evaluation, origin string) {
	t.Helper()
	if got.Allowed || got.ReviewsKnown {
		t.Fatalf("incoherent evidence authorized evaluation: %+v", got)
	}
	for _, r := range got.Requirements {
		if r.Origin == origin && r.Unresolved && !r.Met && strings.HasPrefix(r.Reason, "owner_evidence_incoherent:") {
			return
		}
	}
	t.Fatalf("missing distinct unresolved owner requirement: %+v", got)
}

func TestCodeOwnerEvidenceBeforeExemptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Classification)
	}{
		{"missing-all-rows", func(c *Classification) { c.CodeOwners = nil }},
		{"missing-file-inventory", func(c *Classification) { c.Files = nil }},
		{"missing-path-projection", func(c *Classification) { c.Paths = c.Paths[:1] }},
		{"extra-path-projection", func(c *Classification) { c.Paths = append(c.Paths, "hidden.go") }},
		{"extra-row", func(c *Classification) {
			c.CodeOwners = append(c.CodeOwners, ownedPath("outside.go", NewSide, "alice"))
		}},
		{"duplicate-inventory-file", func(c *Classification) { c.Files = append(c.Files, c.Files[0]) }},
		{"unpaired-rename", func(c *Classification) { c.Files[0].Renamed = true }},
		{"unrecorded-rename", func(c *Classification) { c.Files[0].OldPath = "old/a.go"; c.Paths = append(c.Paths, "old/a.go") }},
		{"unknown-status", func(c *Classification) { c.CodeOwners[1].Status = "future-status" }},
		{"known-unresolved-status", func(c *Classification) { c.CodeOwners[1].Status = OwnersUnresolved }},
		{"ownerless-mid-token-hash", func(c *Classification) {
			c.CodeOwners[1].Status = OwnersOwnerless
			c.CodeOwners[1].Eligible = IdentitySet{}
			c.CodeOwners[1].LastMatchingEntry = "src/b.go#fragment @alice"
		}},
		{"resolved-mid-token-hash", func(c *Classification) { c.CodeOwners[1].LastMatchingEntry = "src/b.go @alice#fragment" }},
		{"ownerless-bom", func(c *Classification) {
			c.CodeOwners[1].Status = OwnersOwnerless
			c.CodeOwners[1].Eligible = IdentitySet{}
			c.CodeOwners[1].LastMatchingEntry = "\ufeffsrc/b.go"
		}},
		{"resolved-without-owners", func(c *Classification) { c.CodeOwners[1].LastMatchingEntry = "src/b.go # comment" }},
		{"resolved-without-ids", func(c *Classification) { c.CodeOwners[1].Eligible.IDs = nil }},
		{"resolved-with-empty-id", func(c *Classification) { c.CodeOwners[1].Eligible.IDs = []string{""} }},
		{"known-unresolved-membership", func(c *Classification) { c.CodeOwners[1].Eligible.Resolved = false }},
		{"known-stale-membership", func(c *Classification) { c.CodeOwners[1].Eligible.Fresh = false }},
		{"ownerless-without-entry", func(c *Classification) {
			c.CodeOwners[1] = CodeOwner{Path: "src/b.go", Side: NewSide, Status: OwnersOwnerless}
		}},
		{"uncovered-with-owner-entry-only", func(c *Classification) {
			c.CodeOwners[1].Status = OwnersUncovered
			c.CodeOwners[1].Eligible = IdentitySet{}
		}},
		{"ownerless-with-owner-ids-only", func(c *Classification) {
			c.CodeOwners[1].Status = OwnersOwnerless
			c.CodeOwners[1].LastMatchingEntry = "src/b.go"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := round2OwnerInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			reviewActor(&in, "bob", HumanAccount, 1)
			tc.edit(&in.Classification)
			// Both a definite exemption and an inactive owner rule must still
			// preserve the evidence failure discovered before rule matching.
			p := round2OwnerPolicy(false)
			p.Approvals.Rules[0].When = Match{Authors: []AccountKind{AgentAccount}}
			p.Approvals.Rules = append(p.Approvals.Rules, Rule{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}})
			requireIncoherentOwners(t, Evaluate(p, in), "code owners")
		})
	}
}

func policyEvidenceInput() Input {
	in := reviewInput()
	in.Classification.Paths = []string{"policy/a", "policy/b"}
	in.Classification.Files = []FileEvidence{
		{OldPath: "policy/a", NewPath: "policy/a", Facts: map[string]Truth{"policy": True}},
		{OldPath: "policy/b", NewPath: "policy/b", Facts: map[string]Truth{"policy": True}},
	}
	in.Classification.Facts["policy"] = True
	in.Classification.PolicyOwnersKnown = true
	in.Classification.PolicyOwners = []PolicyOwner{
		{Path: "policy/a", Eligible: IdentitySet{IDs: []string{"alice"}, Resolved: true, Fresh: true}},
		{Path: "policy/b", Eligible: IdentitySet{IDs: []string{"bob"}, Resolved: true, Fresh: true}},
	}
	return in
}

func policyExemption() Policy {
	p := writerPolicy()
	p.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
	return p
}

func TestPolicyOwnerEvidenceCoverage(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Classification)
	}{
		{"no-owner-rows-for-nonempty-policy-diff", func(c *Classification) { c.PolicyOwners = nil }},
		{"missing-second-policy-file", func(c *Classification) { c.PolicyOwners = c.PolicyOwners[:1] }},
		{"duplicate-first-file-replaces-second", func(c *Classification) { c.PolicyOwners[1] = c.PolicyOwners[0] }},
		{"unrelated-owned-path", func(c *Classification) { c.PolicyOwners[1].Path = "unrelated.go" }},
		{"extra-row", func(c *Classification) { c.PolicyOwners = append(c.PolicyOwners, PolicyOwner{Path: "extra"}) }},
		{"rename-missing-old-side", func(c *Classification) {
			c.Files = []FileEvidence{{OldPath: "policy/b", NewPath: "policy/a", Renamed: true, RenamePaired: true, Facts: map[string]Truth{"policy": True}}}
			c.PolicyOwners = c.PolicyOwners[:1]
		}},
		{"deletion-recorded-as-new-path", func(c *Classification) {
			c.Paths = []string{"deleted-policy"}
			c.Files = []FileEvidence{{OldPath: "deleted-policy", Facts: map[string]Truth{"policy": True}}}
			c.PolicyOwners = c.PolicyOwners[:1]
		}},
		{"missing-file-inventory", func(c *Classification) { c.Files = nil }},
		{"missing-path-projection", func(c *Classification) { c.Paths = c.Paths[:1] }},
		{"extra-path-projection", func(c *Classification) { c.Paths = append(c.Paths, "extra") }},
		{"empty-path", func(c *Classification) { c.PolicyOwners[1].Path = "" }},
		{"empty-id", func(c *Classification) { c.PolicyOwners[1].Eligible.IDs = []string{""} }},
		{"missing-file-policy-fact", func(c *Classification) { delete(c.Files[1].Facts, "policy") }},
		{"unknown-file-policy-fact", func(c *Classification) { c.Files[1].Facts["policy"] = Unknown }},
		{"owner-row-for-nonpolicy-file", func(c *Classification) { c.Files[1].Facts["policy"] = False }},
		{"false-global-policy-label", func(c *Classification) { c.Facts["policy"] = False }},
		{"false-global-policy-label-with-unknown-owners", func(c *Classification) {
			c.Facts["policy"] = False
			c.PolicyOwnersKnown = false
			c.PolicyOwners = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := policyEvidenceInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			tc.edit(&in.Classification)
			requireIncoherentOwners(t, Evaluate(policyExemption(), in), "policy owners")
		})
	}
}

func TestPolicyOwnerExhaustivePools(t *testing.T) {
	ids := []string{"alice", "bob", "carol"}
	cases := 0
	for left := 1; left < 8; left++ {
		for right := 1; right < 8; right++ {
			for states := 0; states < 125; states++ {
				in := policyEvidenceInput()
				state := [3]int{}
				n := states
				for i, id := range ids {
					state[i] = n % 5
					n /= 5
					reviewActor(&in, id, HumanAccount, state[i])
				}
				want := true
				for file, mask := range []int{left, right} {
					in.Classification.PolicyOwners[file].Eligible.IDs = nil
					approvals := 0
					for i, id := range ids {
						if mask&(1<<i) == 0 {
							continue
						}
						in.Classification.PolicyOwners[file].Eligible.IDs = append(in.Classification.PolicyOwners[file].Eligible.IDs, id)
						if state[i] == 4 {
							want = false
						}
						if state[i] == 1 || state[i] == 2 {
							approvals++
						}
					}
					if approvals == 0 {
						want = false
					}
				}
				got := Evaluate(policyExemption(), in)
				cases++
				if got.Allowed != want {
					t.Fatalf("pools=%03b/%03b states=%v want=%v got=%+v", left, right, state, want, got)
				}
			}
		}
	}
	t.Logf("checked %d independent policy pool/stance combinations", cases)
}

func TestPolicyOwnerEvidencePools(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Input)
		allowed bool
	}{
		{"disjoint-only-alice", func(*Input) {}, false},
		{"disjoint-both-owners", func(in *Input) { reviewActor(in, "bob", HumanAccount, 1) }, true},
		{"nonowner-is-writer", func(in *Input) { delete(in.Stances, "alice"); reviewActor(in, "outsider", HumanAccount, 1) }, false},
		{"shared-owner", func(in *Input) { in.Classification.PolicyOwners[1].Eligible.IDs = []string{"alice"} }, true},
		{"ownerless-second-file", func(in *Input) { in.Classification.PolicyOwners[1].Eligible = IdentitySet{Resolved: true, Fresh: true} }, false},
		{"uncovered-second-file", func(in *Input) { in.Classification.PolicyOwners[1].Eligible = IdentitySet{} }, false},
		{"unknown-membership", func(in *Input) {
			in.Classification.PolicyOwners[1].Eligible.Resolved = false
			reviewActor(in, "bob", HumanAccount, 1)
		}, false},
		{"stale-membership", func(in *Input) {
			in.Classification.PolicyOwners[1].Eligible.Fresh = false
			reviewActor(in, "bob", HumanAccount, 1)
		}, false},
		{"unknown-owners", func(in *Input) { in.Classification.PolicyOwnersKnown = false; reviewActor(in, "bob", HumanAccount, 1) }, false},
		{"incomplete-inventory", func(in *Input) { in.Classification.InventoryComplete = false; reviewActor(in, "bob", HumanAccount, 1) }, false},
		{"writer-denied", func(in *Input) {
			reviewActor(in, "bob", HumanAccount, 1)
			a := in.Actors["bob"]
			a.Grant.Write = False
			in.Actors["bob"] = a
		}, false},
		{"reassigned-owner-login", func(in *Input) {
			reviewActor(in, "bob", HumanAccount, 1)
			a := in.Actors["bob"]
			a.Grant.ReturnedID = "other"
			in.Actors["bob"] = a
		}, false},
		{"excluded-sole-owner", func(in *Input) {
			reviewActor(in, "bob", HumanAccount, 1)
			in.ExcludedIDs = append(in.ExcludedIDs, "bob")
		}, false},
		{"eligible-owner-requests-changes", func(in *Input) { reviewActor(in, "bob", HumanAccount, 4) }, false},
		{"outsider-requests-changes", func(in *Input) { reviewActor(in, "bob", HumanAccount, 1); reviewActor(in, "outsider", HumanAccount, 4) }, true},
		{"owner-agent-is-valid-writer", func(in *Input) { reviewActor(in, "bob", AgentAccount, 1) }, true},
		{"rename-both-sides", func(in *Input) {
			in.Classification.Files = []FileEvidence{{OldPath: "policy/b", NewPath: "policy/a", Renamed: true, RenamePaired: true, Facts: map[string]Truth{"policy": True}}}
			reviewActor(in, "bob", HumanAccount, 1)
		}, true},
		{"deletion-old-path", func(in *Input) {
			in.Classification.Paths = []string{"policy/a"}
			in.Classification.Files = []FileEvidence{{OldPath: "policy/a", Facts: map[string]Truth{"policy": True}}}
			in.Classification.PolicyOwners = in.Classification.PolicyOwners[:1]
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := policyEvidenceInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			tc.edit(&in)
			if got := Evaluate(policyExemption(), in); got.Allowed != tc.allowed {
				t.Fatalf("allowed=%v: %+v", tc.allowed, got)
			}
		})
	}
}
