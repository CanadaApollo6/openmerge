package changepolicy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func currentInput() Input {
	return Input{
		Classification: Classification{Inputs: ClassificationInputs{ChangeID: "change-1", Revision: 1, Base: "base", MergeBase: "merge-base", Head: "head", PolicyDigest: "policy", Generation: 1, ClassifierVersion: "classifier-v1"}, InventoryComplete: true, PathsSafe: true, Paths: []string{"docs/readme.md"}, Facts: map[string]Truth{"docs": True, "generated": False, "policy": False, "unknown": False}},
		AuthorKind:     HumanAccount,
		Current:        ReceiptIdentity{Base: "base", MergeBase: "merge-base", ChangeID: "change-1", Revision: 1, Generation: 1, Head: "head", IntentDigest: "intent", PolicyDigest: "policy", BriefDigest: "brief"},
		ExcludedIDs:    []string{"author", "submitter", "accountable"},
		Actors:         map[string]Actor{}, Stances: map[string]Stance{},
	}
}

func addReviewer(in *Input, id string, kind AccountKind, receipt bool) {
	in.Actors[id] = Actor{Identity: Identity{ID: id, Login: "login-" + id, Kind: kind}, Grant: WriteGrant{SubjectID: id, ReturnedID: id, Fresh: true, Write: True}}
	stance := Stance{UserID: id, Kind: Approve, Head: in.Current.Head, Revision: in.Current.Revision, Source: StanceSource{Channel: NativeReview, ID: "review-" + id}}
	if receipt {
		binding := in.Current
		stance.Receipt = &binding
		stance.Source.Channel = CommentCommand
	}
	in.Stances[id] = stance
}

func writerPolicy() Policy {
	return Policy{Approvals: &Approvals{Default: Requirement{Selector: AnyWriter, Count: 1}}}
}
func groupPolicy() Policy {
	return Policy{Approvals: &Approvals{Default: Requirement{Selector: AnyWriter, Count: 1}, Rules: []Rule{
		{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "platform", Count: 2}},
		{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "platform", Count: 1, Receipt: true}},
	}}, Groups: map[string]IdentitySet{"platform": {IDs: []string{"a", "b"}, Resolved: true, Fresh: true}}}
}

func TestFacetTruthTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		match  Match
		edit   func(*Classification)
		author AccountKind
		want   Truth
	}{
		{"docs-true", Match{Labels: []string{"docs"}}, nil, HumanAccount, True},
		{"docs-false", Match{Labels: []string{"docs"}}, func(c *Classification) { c.Facts["docs"] = False }, HumanAccount, False},
		{"docs-unknown", Match{Labels: []string{"docs"}}, func(c *Classification) { c.Facts["docs"] = Unknown }, HumanAccount, Unknown},
		{"generated-unreadable", Match{Labels: []string{"generated"}}, func(c *Classification) { c.Facts["generated"] = Unknown }, HumanAccount, Unknown},
		{"paths-true", Match{Paths: []string{"docs/**"}}, nil, HumanAccount, True},
		{"paths-false", Match{Paths: []string{"infra/**"}}, nil, HumanAccount, False},
		{"paths-truncated-known-failure", Match{Paths: []string{"infra/**"}}, func(c *Classification) { c.InventoryComplete = false }, HumanAccount, Unknown},
		{"touches-hit", Match{Touches: []string{"docs/**"}}, func(c *Classification) { c.InventoryComplete = false }, HumanAccount, True},
		{"touches-miss-complete", Match{Touches: []string{"infra/**"}}, nil, HumanAccount, False},
		{"touches-miss-truncated", Match{Touches: []string{"infra/**"}}, func(c *Classification) { c.InventoryComplete = false }, HumanAccount, Unknown},
		{"owned-known-positive", Match{Labels: []string{"owned:user:alice"}}, func(c *Classification) { c.InventoryComplete = false; c.Facts["owned:user:alice"] = True }, HumanAccount, True},
		{"owned-absence-truncated", Match{Labels: []string{"owned:user:alice"}}, func(c *Classification) { c.InventoryComplete = false; c.Facts["owned:user:alice"] = False }, HumanAccount, Unknown},
		{"policy-known-positive", Match{Labels: []string{"policy"}}, func(c *Classification) { c.InventoryComplete = false; c.Facts["policy"] = True }, HumanAccount, True},
		{"authors-true", Match{Authors: []AccountKind{HumanAccount, AgentAccount}}, nil, AgentAccount, True},
		{"authors-false", Match{Authors: []AccountKind{AgentAccount}}, nil, HumanAccount, False},
		{"authors-unknown", Match{Authors: []AccountKind{HumanAccount}}, nil, "", Unknown},
		{"and-false-beats-unknown", Match{Labels: []string{"generated", "docs"}}, func(c *Classification) { c.Facts["generated"] = Unknown; c.Facts["docs"] = False }, HumanAccount, False},
		{"and-true-unknown", Match{Labels: []string{"docs", "generated"}}, func(c *Classification) { c.Facts["generated"] = Unknown }, HumanAccount, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := currentInput().Classification
			if tc.edit != nil {
				tc.edit(&c)
			}
			got, err := MatchRule(tc.match, c, tc.author)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	for _, match := range []Match{{}, {Labels: []string{"forge-label"}}, {Paths: []string{"bad\\"}}, {Authors: []AccountKind{"bot"}}} {
		if _, err := MatchRule(match, Classification{}, ""); err == nil {
			t.Fatalf("accepted invalid match: %+v", match)
		}
	}
}

func TestPolicySafetyCases(t *testing.T) {
	t.Run("two-platform-one-receipt", func(t *testing.T) {
		in, policy := currentInput(), groupPolicy()
		addReviewer(&in, "a", HumanAccount, true)
		if Evaluate(policy, in).Allowed {
			t.Fatal("one ID counted twice")
		}
		addReviewer(&in, "b", AgentAccount, false)
		got := Evaluate(policy, in)
		if !got.Allowed || len(got.Requirements) != 2 || len(got.Requirements[0].ApprovedIDs) != 2 || len(got.Requirements[1].ApprovedIDs) != 1 {
			t.Fatalf("%+v", got)
		}
		stance := in.Stances["a"]
		stance.Receipt = nil
		stance.Source.Channel = NativeReview
		in.Stances["a"] = stance
		got = Evaluate(policy, in)
		if got.Allowed || len(got.ReviewLogins) != 2 {
			t.Fatalf("native review must satisfy conditions but not receipt requirement: %+v", got)
		}
	})
	t.Run("unknown-never-lowers", func(t *testing.T) {
		in, policy := currentInput(), groupPolicy()
		in.Classification.Facts["docs"] = Unknown
		addReviewer(&in, "c", HumanAccount, true)
		got := Evaluate(policy, in)
		if got.Allowed || len(got.Requirements) != 3 || got.Requirements[2].Origin != "default" {
			t.Fatalf("unknown rules and default must all apply: %+v", got)
		}
	})
	t.Run("require-none-only-on-a-definite-match", func(t *testing.T) {
		in, policy := currentInput(), writerPolicy()
		policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
		if !Evaluate(policy, in).Allowed {
			t.Fatal("definite none should bypass default")
		}
		for _, truth := range []Truth{False, Unknown} {
			in.Classification.Facts["docs"] = truth
			if Evaluate(policy, in).Allowed {
				t.Fatalf("%v lowered default", truth)
			}
		}
		in.Classification.Facts["docs"] = True
		policy.Approvals.Rules = append(policy.Approvals.Rules, Rule{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Human, Count: 1}})
		if Evaluate(policy, in).Allowed {
			t.Fatal("none canceled overlapping requirement")
		}
	})
	t.Run("docs-codeowners-owner-required", func(t *testing.T) {
		in, policy := currentInput(), writerPolicy()
		in.Classification.Paths = []string{"docs/CODEOWNERS"}
		in.Classification.Files = []FileEvidence{{NewPath: "docs/CODEOWNERS", Facts: map[string]Truth{"policy": True}}}
		in.Classification.Facts["policy"] = True
		in.Classification.PolicyOwnersKnown = true
		in.Classification.PolicyOwners = []PolicyOwner{{Path: "docs/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"a", "b"}, Resolved: true, Fresh: true}}}
		policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
		if Evaluate(policy, in).Allowed {
			t.Fatal("docs exemption canceled owner requirement")
		}
		addReviewer(&in, "a", HumanAccount, false)
		if !Evaluate(policy, in).Allowed {
			t.Fatal("owner union not satisfied")
		}
		in.Classification.PolicyOwners = append(in.Classification.PolicyOwners, PolicyOwner{Path: "old/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"a"}, Resolved: true, Fresh: true}})
		in.Classification.Paths = append(in.Classification.Paths, "old/CODEOWNERS")
		in.Classification.Files = append(in.Classification.Files, FileEvidence{OldPath: "old/CODEOWNERS", Facts: map[string]Truth{"policy": True}})
		if !Evaluate(policy, in).Allowed {
			t.Fatal("one owner cannot satisfy two policy paths")
		}
		in.Classification.PolicyOwners[1].Eligible = IdentitySet{Resolved: true, Fresh: true}
		if got := Evaluate(policy, in); got.Allowed || !got.Requirements[1].Unresolved {
			t.Fatal("uncovered or email-only policy path passed")
		}
	})
	t.Run("truncated-all-docs", func(t *testing.T) {
		in, policy := currentInput(), writerPolicy()
		in.Classification.InventoryComplete = false
		policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
		addReviewer(&in, "a", HumanAccount, true)
		got := Evaluate(policy, in)
		if got.Allowed || !strings.Contains(strings.Join(got.Reasons, " "), "split the change") || got.ReviewsKnown {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("unreadable-generated-never-relaxes", func(t *testing.T) {
		in, policy := currentInput(), writerPolicy()
		in.Classification.Facts["generated"] = Unknown
		policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"generated"}}, Require: Requirement{Selector: None}}}
		if Evaluate(policy, in).Allowed {
			t.Fatal("unreadable attribute lowered default")
		}
	})
}

func TestFreshEligibilityAndStances(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Input, *Policy)
		allowed bool
	}{
		{"stale-classification-head", func(in *Input, _ *Policy) { in.Classification.Inputs.Head = "old" }, false},
		{"stale-classification-policy", func(in *Input, _ *Policy) { in.Classification.Inputs.PolicyDigest = "old" }, false},
		{"missing-classification-version", func(in *Input, _ *Policy) { in.Classification.Inputs.ClassifierVersion = "" }, false},
		{"writer-can-be-agent", func(in *Input, _ *Policy) { addReviewer(in, "a", AgentAccount, false) }, true},
		{"human-filter", func(in *Input, p *Policy) {
			addReviewer(in, "a", AgentAccount, false)
			p.Approvals.Default = Requirement{Selector: Human, Count: 1}
		}, false},
		{"unknown-kind", func(in *Input, p *Policy) {
			addReviewer(in, "a", "", false)
			p.Approvals.Default = Requirement{Selector: Human, Count: 1}
		}, false},
		{"missing-actor", func(in *Input, _ *Policy) { delete(in.Actors, "a") }, false},
		{"missing-source", func(in *Input, _ *Policy) { s := in.Stances["a"]; s.Source = StanceSource{}; in.Stances["a"] = s }, false},
		{"comment-without-receipt", func(in *Input, _ *Policy) {
			s := in.Stances["a"]
			s.Source.Channel = CommentCommand
			in.Stances["a"] = s
		}, false},
		{"stale-grant", func(in *Input, _ *Policy) { a := in.Actors["a"]; a.Grant.Fresh = false; in.Actors["a"] = a }, false},
		{"reassigned-login", func(in *Input, _ *Policy) { a := in.Actors["a"]; a.Grant.ReturnedID = "new-id"; in.Actors["a"] = a }, false},
		{"invalid-grant", func(in *Input, _ *Policy) { a := in.Actors["a"]; a.Grant.Write = Truth(99); in.Actors["a"] = a }, false},
		{"missing-response-id", func(in *Input, _ *Policy) { a := in.Actors["a"]; a.Grant.ReturnedID = ""; in.Actors["a"] = a }, false},
		{"revoked-writer", func(in *Input, _ *Policy) { a := in.Actors["a"]; a.Grant.Write = False; in.Actors["a"] = a }, false},
		{"wrong-head", func(in *Input, _ *Policy) { s := in.Stances["a"]; s.Head = "old"; in.Stances["a"] = s }, false},
		{"old-revision-same-head", func(in *Input, _ *Policy) { in.Current.Revision = 3 }, false},
		{"author", func(in *Input, _ *Policy) { in.ExcludedIDs = append(in.ExcludedIDs, "a") }, false},
		{"submitter", func(in *Input, _ *Policy) { addReviewer(in, "submitter", HumanAccount, true); delete(in.Stances, "a") }, false},
		{"accountable", func(in *Input, _ *Policy) {
			addReviewer(in, "accountable", HumanAccount, true)
			delete(in.Stances, "a")
		}, false},
		{"withdrawn", func(in *Input, _ *Policy) { s := in.Stances["a"]; s.Kind = Withdraw; in.Stances["a"] = s }, false},
		{"eligible-request-changes", func(in *Input, _ *Policy) {
			addReviewer(in, "b", HumanAccount, false)
			s := in.Stances["b"]
			s.Kind = RequestChanges
			in.Stances["b"] = s
		}, false},
		{"ineligible-request-changes", func(in *Input, _ *Policy) {
			addReviewer(in, "author", HumanAccount, false)
			s := in.Stances["author"]
			s.Kind = RequestChanges
			in.Stances["author"] = s
		}, true},
		{"intent-edit", func(in *Input, _ *Policy) { addReviewer(in, "a", HumanAccount, true); in.Current.IntentDigest = "new" }, false},
		{"policy-edit", func(in *Input, _ *Policy) { addReviewer(in, "a", HumanAccount, true); in.Current.PolicyDigest = "new" }, false},
		{"base-rewrite", func(in *Input, _ *Policy) { addReviewer(in, "a", HumanAccount, true); in.Current.Generation++ }, false},
		{"native-ignores-intent", func(in *Input, _ *Policy) { in.Current.IntentDigest = "new" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, policy := currentInput(), writerPolicy()
			addReviewer(&in, "a", HumanAccount, false)
			tc.edit(&in, &policy)
			before, _ := json.Marshal(in)
			got := Evaluate(policy, in)
			after, _ := json.Marshal(in)
			if got.Allowed != tc.allowed {
				t.Fatalf("%+v", got)
			}
			if string(before) != string(after) || !reflect.DeepEqual(got, Evaluate(policy, in)) {
				t.Fatal("evaluation mutated inputs or was nondeterministic")
			}
		})
	}
}

func TestKnownEmptyVersusUnresolvedUnderNegation(t *testing.T) {
	in, policy := currentInput(), writerPolicy()
	policy.Approvals.Default = Requirement{Selector: Group, Group: "empty", Count: 1}
	policy.Groups = map[string]IdentitySet{"empty": {Resolved: true, Fresh: true}}
	addReviewer(&in, "a", HumanAccount, false)
	negative, _ := condition.Parse("-approved-reviews-by = login-a")
	positive, _ := condition.Parse("#approved-reviews-by >= 1")
	for _, unresolved := range []bool{false, true} {
		pool := policy.Groups["empty"]
		pool.Resolved = !unresolved
		policy.Groups["empty"] = pool
		got := Evaluate(policy, in)
		if got.Allowed {
			t.Fatal("positive approval with no eligible reviewers passed")
		}
		snapshot := condition.Snapshot{"approved-reviews-by": got.ReviewAttribute()}
		if ok, _ := positive.Evaluate(snapshot); ok {
			t.Fatal("positive condition passed")
		}
		if ok, _ := negative.Evaluate(snapshot); ok == unresolved {
			t.Fatalf("unresolved=%v, negative passed=%v", unresolved, ok)
		}
		if ok, _ := condition.Not(condition.Any(positive, condition.Not(negative))).Evaluate(snapshot); ok == unresolved {
			t.Fatal("nested grammar lost unknown")
		}
	}
	policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
	if got := Evaluate(policy, in); !got.Allowed || !got.ReviewsKnown || len(got.ReviewLogins) != 0 {
		t.Fatal("none invented a review pool")
	}
}

func TestRequirementTypedCauses(t *testing.T) {
	in, policy := currentInput(), groupPolicy()
	got := Evaluate(policy, in)
	if len(got.Requirements) != 2 {
		t.Fatalf("requirements: %+v", got)
	}
	for i, result := range got.Requirements {
		if result.Cause != CauseRule || result.RuleIndex == nil || *result.RuleIndex != i {
			t.Fatalf("rule %d lost typed provenance: %+v", i, result)
		}
	}
	in, policy = currentInput(), writerPolicy()
	in.Classification.InventoryComplete = false
	got = Evaluate(policy, in)
	if len(got.Requirements) != 2 || got.Requirements[0].Cause != CauseDefault || got.Requirements[0].InventoryAffected || got.Requirements[1].Cause != CausePolicyOwnerEvidence || got.Requirements[1].UnresolvedCause != UnresolvedInventory {
		t.Fatalf("inventory cause was not isolated to its requirement: %+v", got)
	}
	in, policy = currentInput(), writerPolicy()
	in.Classification.InventoryComplete, in.Classification.PathsSafe = false, false
	policy.Approvals.Rules = []Rule{{When: Match{Paths: []string{"docs/**"}}, Require: Requirement{Selector: Human, Count: 1}}}
	got = Evaluate(policy, in)
	matched := false
	for _, result := range got.Requirements {
		if result.Cause == CauseRule {
			matched = result.InventoryAffected && result.UnresolvedCause == UnresolvedInventory
		}
	}
	if !matched {
		t.Fatalf("incomplete unsafe path rule lost inventory cause: %+v", got)
	}
	in, policy = currentInput(), writerPolicy()
	in.Classification.Files = []FileEvidence{{NewPath: "docs/readme.md", Facts: map[string]Truth{"policy": False}}}
	in.Classification.CodeOwnersKnown = true
	in.Classification.CodeOwners = []CodeOwner{{Path: "docs/readme.md", Side: NewSide, Status: OwnersResolved, LastMatchingEntry: "* @owner", Eligible: IdentitySet{IDs: []string{"owner"}, Resolved: true, Fresh: true}}}
	in.Actors["owner"] = Actor{Identity: Identity{ID: "owner", Login: "owner", Kind: HumanAccount}, Grant: WriteGrant{SubjectID: "owner", ReturnedID: "owner", Fresh: true, Write: Unknown}}
	policy.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: CodeOwners}}}
	got = Evaluate(policy, in)
	if len(got.Requirements) != 1 || got.Requirements[0].Cause != CauseRuleCodeOwners || got.Requirements[0].UnresolvedCause != UnresolvedEligibility {
		t.Fatalf("unknown writer eligibility was inferred as owner uncertainty: %+v", got)
	}
}
