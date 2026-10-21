package changepolicy

import "testing"

func authorityInput() Input {
	in := currentInput()
	in.Classification.Files = []FileEvidence{{NewPath: "docs/readme.md", Facts: map[string]Truth{"policy": False}}}
	in.Classification.CodeOwnersKnown = true
	in.Classification.CodeOwners = []CodeOwner{{Path: "docs/readme.md", Side: NewSide, Status: OwnersUncovered}}
	return in
}

func TestAuthorityWithoutApprovals(t *testing.T) {
	p := Policy{RequirePolicyOwners: true}
	in := authorityInput()
	got := Evaluate(p, in)
	if !got.Allowed || !got.OptedIn || len(got.Requirements) != 0 {
		t.Fatalf("ordinary reviewer floor: %+v", got)
	}
	in.Classification.InventoryComplete = false
	if Evaluate(p, in).Allowed {
		t.Fatal("incomplete inventory skipped policy owners")
	}
	in = authorityInput()
	in.Classification.Facts["policy"] = True
	in.Classification.Files[0].Facts["policy"] = True
	in.Classification.CodeOwners[0] = CodeOwner{Path: "docs/readme.md", Side: NewSide, Status: OwnersResolved, LastMatchingEntry: "* @owner", Eligible: IdentitySet{IDs: []string{"owner"}, Resolved: true, Fresh: true}}
	in.Classification.PolicyOwnersKnown = true
	in.Classification.PolicyOwners = []PolicyOwner{{Path: "docs/readme.md", Eligible: IdentitySet{IDs: []string{"owner"}, Resolved: true, Fresh: true}}}
	if Evaluate(p, in).Allowed {
		t.Fatal("policy edit without approval accepted")
	}
	addReviewer(&in, "other", HumanAccount, false)
	if Evaluate(p, in).Allowed {
		t.Fatal("non-owner approval accepted")
	}
	addReviewer(&in, "owner", HumanAccount, false)
	if got := Evaluate(p, in); !got.Allowed || len(got.Requirements) != 1 || got.Requirements[0].Cause != CausePolicyOwner {
		t.Fatalf("exact owner rejected: %+v", got)
	}
	for _, mutate := range []func(*Input){
		func(i *Input) { s := i.Stances["owner"]; s.Revision++; i.Stances["owner"] = s },
		func(i *Input) { s := i.Stances["owner"]; s.Head = "new-head"; i.Stances["owner"] = s },
		func(i *Input) { i.ExcludedIDs = append(i.ExcludedIDs, "owner") },
		func(i *Input) { a := i.Actors["owner"]; a.Grant.Fresh = false; i.Actors["owner"] = a },
	} {
		addReviewer(&in, "owner", HumanAccount, false)
		in.ExcludedIDs = nil
		mutate(&in)
		if Evaluate(p, in).Allowed {
			t.Fatal("stale/ineligible owner accepted")
		}
	}
	addReviewer(&in, "owner", HumanAccount, false)
	in.ExcludedIDs = nil
	p.Approvals = &Approvals{Default: Requirement{Selector: AnyWriter, Count: 1}, Rules: []Rule{{When: Match{Labels: []string{"policy"}}, Require: Requirement{Selector: None}}}}
	delete(in.Stances, "owner")
	if Evaluate(p, in).Allowed {
		t.Fatal("none rule waived mandatory owner")
	}
	addReviewer(&in, "owner", HumanAccount, false)
	if !Evaluate(p, in).Allowed {
		t.Fatal("owner under none rule rejected")
	}
	if !Evaluate(Policy{}, Input{}).Allowed {
		t.Fatal("legacy nil policy changed")
	}
}

func TestAuthorityOnlyPolicyDigest(t *testing.T) {
	in := DigestInputs{RequirePolicyOwners: true, Owners: GoverningContent{Path: "CODEOWNERS", Known: true, Present: true, Content: "* @alice\n"}, ClassifierVersion: "classifier"}
	first, err := PolicyDigest(in)
	if err != nil || first == "" {
		t.Fatal(first, err)
	}
	in.Owners.Content = "* @bob\n"
	second, err := PolicyDigest(in)
	if err != nil || first == second {
		t.Fatal("owners absent from digest", err)
	}
	in.Owners.Known = false
	if _, err := PolicyDigest(in); err == nil {
		t.Fatal("unknown owners hashed")
	}
	in.RequirePolicyOwners = false
	if digest, err := PolicyDigest(in); err != nil || digest != "" {
		t.Fatal("legacy nil digest changed", digest, err)
	}
}
