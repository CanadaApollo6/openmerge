package changepolicy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func reviewInput() Input {
	return Input{
		Classification: Classification{
			Inputs:            ClassificationInputs{ChangeID: "c", Revision: 1, Base: "base", MergeBase: "merge-base", Head: "h", PolicyDigest: "p", Generation: 1, ClassifierVersion: "v1"},
			InventoryComplete: true, PathsSafe: true, Paths: []string{"docs/readme.md"},
			Facts: map[string]Truth{"docs": True, "generated": False, "policy": False, "unknown": False},
		},
		AuthorKind:  HumanAccount,
		Current:     ReceiptIdentity{Base: "base", MergeBase: "merge-base", ChangeID: "c", Revision: 1, Generation: 1, Head: "h", IntentDigest: "i", PolicyDigest: "p", BriefDigest: "b"},
		ExcludedIDs: []string{"author", "submitter", "accountable"},
		Actors:      map[string]Actor{}, Stances: map[string]Stance{},
	}
}

func reviewActor(in *Input, id string, kind AccountKind, state int) {
	in.Actors[id] = Actor{Identity: Identity{ID: id, Login: id, Kind: kind}, Grant: WriteGrant{SubjectID: id, ReturnedID: id, Fresh: true, Write: True}}
	if state == 0 {
		return
	}
	s := Stance{UserID: id, Kind: Approve, Revision: in.Current.Revision, Head: in.Current.Head, Source: StanceSource{Channel: NativeReview, ID: "s-" + id}}
	if state == 2 {
		r := in.Current
		s.Receipt = &r
		s.Source.Channel = CommentCommand
	}
	if state == 3 {
		s.Kind = Withdraw
	}
	if state == 4 {
		s.Kind = RequestChanges
	}
	in.Stances[id] = s
}

func TestReviewAdversarialFacets(t *testing.T) {
	cases := []struct {
		name    string
		match   Match
		edit    func(*Input)
		want    Truth
		blocked bool
	}{
		{"truncated-all-docs", Match{Labels: []string{"docs"}}, func(in *Input) { in.Classification.InventoryComplete = false }, Unknown, true},
		{"truncated-docs-observed-negative", Match{Labels: []string{"docs"}}, func(in *Input) { in.Classification.InventoryComplete = false; in.Classification.Facts["docs"] = False }, Unknown, true},
		{"truncated-paths-hit", Match{Paths: []string{"docs/**"}}, func(in *Input) { in.Classification.InventoryComplete = false }, Unknown, true},
		{"truncated-paths-miss", Match{Paths: []string{"src/**"}}, func(in *Input) { in.Classification.InventoryComplete = false }, Unknown, true},
		{"truncated-touches-hit", Match{Touches: []string{"docs/**"}}, func(in *Input) { in.Classification.InventoryComplete = false }, True, true},
		{"truncated-touches-miss", Match{Touches: []string{"src/**"}}, func(in *Input) { in.Classification.InventoryComplete = false }, Unknown, true},
		{"unsafe-paths-hit", Match{Paths: []string{"docs/**"}}, func(in *Input) { in.Classification.PathsSafe = false }, Unknown, true},
		{"unknown-author-human", Match{Authors: []AccountKind{HumanAccount}}, func(in *Input) { in.AuthorKind = "" }, Unknown, true},
		{"unknown-author-agent", Match{Authors: []AccountKind{AgentAccount}}, func(in *Input) { in.AuthorKind = "" }, Unknown, true},
		{"unknown-author-both", Match{Authors: []AccountKind{HumanAccount, AgentAccount}}, func(in *Input) { in.AuthorKind = "" }, Unknown, true},
		{"unknown-docs", Match{Labels: []string{"docs"}}, func(in *Input) { in.Classification.Facts["unknown"] = True; in.Classification.Facts["docs"] = Unknown }, Unknown, true},
		{"unknown-generated", Match{Labels: []string{"generated"}}, func(in *Input) {
			in.Classification.Facts["unknown"] = True
			in.Classification.Facts["generated"] = Unknown
		}, Unknown, true},
		{"missing-owned-label", Match{Labels: []string{"owned:user:owner"}}, func(in *Input) { in.Classification.Facts["unknown"] = True }, Unknown, true},
		{"known-docs-despite-unrelated-unknown", Match{Labels: []string{"docs"}}, func(in *Input) { in.Classification.Facts["unknown"] = True }, True, false},
		{"explicit-unknown-label", Match{Labels: []string{"unknown"}}, func(in *Input) { in.Classification.Facts["unknown"] = True }, True, false},
		{"unknown-and-false", Match{Labels: []string{"docs", "generated"}}, func(in *Input) { in.Classification.Facts["docs"] = Unknown }, False, true},
		{"unknown-and-true", Match{Labels: []string{"docs", "generated"}}, func(in *Input) { in.Classification.Facts["generated"] = Unknown }, Unknown, true},
		{"both-rename-sides", Match{Paths: []string{"docs/**"}}, func(in *Input) { in.Classification.Paths = []string{"docs/old.md", "src/new.go"} }, False, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewInput()
			tc.edit(&in)
			got, err := MatchRule(tc.match, in.Classification, in.AuthorKind)
			if err != nil || got != tc.want {
				t.Fatalf("match=%v err=%v want=%v", got, err, tc.want)
			}
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: AnyWriter, Count: 2}, Rules: []Rule{{When: tc.match, Require: Requirement{Selector: None}}}}}
			e := Evaluate(p, in)
			if e.Allowed == tc.blocked {
				t.Fatalf("none allowed=%v want blocked=%v: %+v", e.Allowed, tc.blocked, e)
			}
			// Unknown restrictive rules must survive even when a separate definite none suppresses the default.
			p.Approvals.Rules = []Rule{
				{When: Match{Authors: []AccountKind{HumanAccount, AgentAccount}}, Require: Requirement{Selector: None}},
				{When: tc.match, Require: Requirement{Selector: AnyWriter, Count: 3}},
			}
			e = Evaluate(p, in)
			if tc.want != False {
				found := false
				for _, r := range e.Requirements {
					if r.Origin == "rules[1]" && r.Requirement.Count == 3 {
						found = true
					}
				}
				if !found || e.Allowed {
					t.Fatalf("restrictive rule lost: %+v", e)
				}
			}
		})
	}
}

func TestReviewExhaustiveComposition(t *testing.T) {
	// Five independent actors, five stances each: 3,125 combinations.
	ids := []string{"alice", "bob", "carol", "bot", "author"}
	pools := []map[string]bool{
		{"alice": true, "bob": true, "bot": true, "author": true},
		{"bob": true, "carol": true, "bot": true, "author": true},
		{"alice": true, "bob": true, "carol": true, "bot": true, "author": true},
		{"alice": true, "carol": true},
		{"bob": true, "carol": true},
	}
	counts := []int{2, 1, 2, 1, 1}
	humans := []bool{false, true, true, false, false}
	receipts := []bool{false, true, false, false, false}
	for code := 0; code < 3125; code++ {
		in := reviewInput()
		state := map[string]int{}
		n := code
		for _, id := range ids {
			state[id] = n % 5
			n /= 5
			kind := HumanAccount
			if id == "bot" {
				kind = AgentAccount
			}
			reviewActor(&in, id, kind, state[id])
		}
		in.Classification.Facts["policy"] = True
		in.Classification.PolicyOwnersKnown = true
		in.Classification.PolicyOwners = []PolicyOwner{
			{Path: "docs/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"alice", "carol"}, Resolved: true, Fresh: true}},
			{Path: "old/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"bob", "carol"}, Resolved: true, Fresh: true}},
		}
		p := Policy{Approvals: &Approvals{Default: Requirement{Selector: Human, Count: 9}, Rules: []Rule{
			{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}},
			{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "platform", Count: 2}},
			{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "security", Count: 1, Human: true, Receipt: true}},
			{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Human, Count: 2}},
		}}, Groups: map[string]IdentitySet{
			"platform": {IDs: []string{"alice", "bob", "bot", "author"}, Resolved: true, Fresh: true},
			"security": {IDs: []string{"bob", "carol", "bot", "author"}, Resolved: true, Fresh: true},
		}}
		expected := true
		for i, pool := range pools {
			approvals := 0
			for _, id := range ids {
				if id == "author" || !pool[id] || (humans[i] && id == "bot") {
					continue
				}
				if state[id] == 4 {
					expected = false
				}
				if state[id] == 2 || (state[id] == 1 && !receipts[i]) {
					approvals++
				}
			}
			if approvals < counts[i] {
				expected = false
			}
		}
		e := Evaluate(p, in)
		if e.Allowed != expected || len(e.Requirements) != 5 {
			t.Fatalf("states=%v got=%+v wantAllowed=%v", state, e, expected)
		}
	}
}

func TestReviewHumanFreshnessAndExclusions(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Input)
		want bool
	}{
		{"fresh-human", func(*Input) {}, true},
		{"unknown-kind", func(in *Input) { a := in.Actors["alice"]; a.Identity.Kind = ""; in.Actors["alice"] = a }, false},
		{"writer-agent", func(in *Input) { a := in.Actors["alice"]; a.Identity.Kind = AgentAccount; in.Actors["alice"] = a }, false},
		{"mapped-human-account", func(in *Input) {
			a := in.Actors["alice"]
			a.Identity.Kind = EffectiveKind(a.Identity, []string{"alice"})
			in.Actors["alice"] = a
		}, false},
		{"stale-write", func(in *Input) { a := in.Actors["alice"]; a.Grant.Fresh = false; in.Actors["alice"] = a }, false},
		{"unknown-write", func(in *Input) { a := in.Actors["alice"]; a.Grant.Write = Unknown; in.Actors["alice"] = a }, false},
		{"nonwriter-human", func(in *Input) { a := in.Actors["alice"]; a.Grant.Write = False; in.Actors["alice"] = a }, false},
		{"wrong-subject", func(in *Input) { a := in.Actors["alice"]; a.Grant.SubjectID = "other"; in.Actors["alice"] = a }, false},
		{"reassigned-login", func(in *Input) { a := in.Actors["alice"]; a.Grant.ReturnedID = "other"; in.Actors["alice"] = a }, false},
		{"missing-identity", func(in *Input) { delete(in.Actors, "alice") }, false},
		{"author", func(in *Input) { in.ExcludedIDs = []string{"alice"} }, false},
		{"submitter", func(in *Input) { in.ExcludedIDs = []string{"author", "alice"} }, false},
		{"accountable", func(in *Input) { in.ExcludedIDs = []string{"author", "submitter", "alice"} }, false},
		{"stale-receipt", func(in *Input) { s := in.Stances["alice"]; s.Receipt.PolicyDigest = "old"; in.Stances["alice"] = s }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewInput()
			reviewActor(&in, "alice", HumanAccount, 2)
			tc.edit(&in)
			for _, r := range []Requirement{{Selector: Human, Count: 1}, {Selector: AnyWriter, Human: true, Count: 1, Receipt: true}, {Selector: Group, Group: "g", Human: true, Count: 1}} {
				p := Policy{Approvals: &Approvals{Default: r}, Groups: map[string]IdentitySet{"g": {IDs: []string{"alice"}, Resolved: true, Fresh: true}}}
				e := Evaluate(p, in)
				if e.Allowed != tc.want {
					t.Fatalf("requirement=%+v got=%+v", r, e)
				}
			}
		})
	}
}

func TestReviewQ5UnknownAndKnownEmpty(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Input, *Policy)
		known bool
	}{
		{"known-empty-membership", func(_ *Input, p *Policy) { p.Groups["g"] = IdentitySet{Resolved: true, Fresh: true} }, true},
		{"unresolved-membership", func(_ *Input, p *Policy) { p.Groups["g"] = IdentitySet{Fresh: true} }, false},
		{"stale-membership", func(_ *Input, p *Policy) { p.Groups["g"] = IdentitySet{Resolved: true} }, false},
		{"unresolved-writer", func(in *Input, _ *Policy) { a := in.Actors["alice"]; a.Grant.Write = Unknown; in.Actors["alice"] = a }, false},
		{"missing-reviewer-identity", func(in *Input, _ *Policy) { delete(in.Actors, "alice") }, false},
		{"known-excluded", func(in *Input, _ *Policy) { in.ExcludedIDs = append(in.ExcludedIDs, "alice") }, true},
	}
	negative, _ := condition.Parse("-approved-reviews-by = alice")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: Group, Group: "g", Count: 1}}, Groups: map[string]IdentitySet{"g": {IDs: []string{"alice"}, Resolved: true, Fresh: true}}}
			tc.edit(&in, &p)
			e := Evaluate(p, in)
			pass, _ := negative.Evaluate(condition.Snapshot{"approved-reviews-by": e.ReviewAttribute()})
			if e.ReviewsKnown != tc.known || pass != tc.known || e.Allowed {
				t.Fatalf("got=%+v negation=%v wantKnown=%v", e, pass, tc.known)
			}
		})
	}
}

func TestReviewPolicyOwnerFailures(t *testing.T) {
	for _, state := range []string{"unknown-inventory", "unknown-paths", "no-entry", "email-only", "unreadable-team", "stale-team", "excluded-owner", "no-write"} {
		t.Run(state, func(t *testing.T) {
			in := reviewInput()
			reviewActor(&in, "alice", HumanAccount, 2)
			in.Classification.Paths = []string{"docs/CODEOWNERS"}
			in.Classification.Facts["policy"] = True
			in.Classification.PolicyOwnersKnown = true
			owner := PolicyOwner{Path: "docs/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"alice"}, Resolved: true, Fresh: true}}
			switch state {
			case "unknown-inventory":
				in.Classification.InventoryComplete = false
			case "unknown-paths":
				in.Classification.PolicyOwnersKnown = false
			case "no-entry", "email-only":
				owner.Eligible.IDs = nil
			case "unreadable-team":
				owner.Eligible.Resolved = false
			case "stale-team":
				owner.Eligible.Fresh = false
			case "excluded-owner":
				in.ExcludedIDs = append(in.ExcludedIDs, "alice")
			case "no-write":
				a := in.Actors["alice"]
				a.Grant.Write = False
				in.Actors["alice"] = a
			}
			in.Classification.PolicyOwners = []PolicyOwner{owner}
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: AnyWriter, Count: 1}, Rules: []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}}}
			e := Evaluate(p, in)
			if e.Allowed || len(e.Requirements) == 0 {
				t.Fatalf("owner obligation bypassed: %+v", e)
			}
			if state == "unknown-inventory" && !strings.Contains(strings.Join(e.Reasons, " "), "split") {
				t.Fatal("missing split explanation")
			}
		})
	}
}

func TestReviewIndependentCounts(t *testing.T) {
	for _, states := range [][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}, {0, 2}, {2, 0}} {
		t.Run(fmt.Sprint(states), func(t *testing.T) {
			in := reviewInput()
			reviewActor(&in, "alice", HumanAccount, states[0])
			reviewActor(&in, "bob", HumanAccount, states[1])
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: AnyWriter, Count: 9}, Rules: []Rule{
				{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "platform", Count: 2}},
				{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: Group, Group: "platform", Count: 1, Receipt: true}},
			}}, Groups: map[string]IdentitySet{"platform": {IDs: []string{"alice", "bob", "alice"}, Resolved: true, Fresh: true}}}
			expected := states[0] > 0 && states[1] > 0 && (states[0] == 2 || states[1] == 2)
			e := Evaluate(p, in)
			if e.Allowed != expected {
				t.Fatalf("%+v", e)
			}
			if !reflect.DeepEqual(e, Evaluate(p, in)) {
				t.Fatal("nondeterminism")
			}
		})
	}
}
