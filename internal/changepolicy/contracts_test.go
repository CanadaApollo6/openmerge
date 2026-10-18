package changepolicy

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClassificationBindingsBeforeExemptions(t *testing.T) {
	stringsToBind := []struct {
		name   string
		fields func(*Input) (*string, *string)
	}{
		{"change", func(in *Input) (*string, *string) { return &in.Classification.Inputs.ChangeID, &in.Current.ChangeID }},
		{"base", func(in *Input) (*string, *string) { return &in.Classification.Inputs.Base, &in.Current.Base }},
		{"head", func(in *Input) (*string, *string) { return &in.Classification.Inputs.Head, &in.Current.Head }},
		{"merge-base", func(in *Input) (*string, *string) { return &in.Classification.Inputs.MergeBase, &in.Current.MergeBase }},
		{"policy", func(in *Input) (*string, *string) {
			return &in.Classification.Inputs.PolicyDigest, &in.Current.PolicyDigest
		}},
	}
	var edits []struct {
		name string
		edit func(*Input)
	}
	for _, binding := range stringsToBind {
		for _, state := range []string{"missing-classification", "missing-current", "both-empty", "stale"} {
			edits = append(edits, struct {
				name string
				edit func(*Input)
			}{binding.name + "/" + state, func(in *Input) {
				classification, current := binding.fields(in)
				switch state {
				case "missing-classification":
					*classification = ""
				case "missing-current":
					*current = ""
				case "both-empty":
					*classification, *current = "", ""
				case "stale":
					*classification = "old"
				}
			}})
		}
	}
	for _, binding := range []struct {
		name   string
		fields func(*Input) (*uint64, *uint64)
	}{
		{"revision", func(in *Input) (*uint64, *uint64) { return &in.Classification.Inputs.Revision, &in.Current.Revision }},
		{"generation", func(in *Input) (*uint64, *uint64) {
			return &in.Classification.Inputs.Generation, &in.Current.Generation
		}},
	} {
		for _, state := range []string{"missing-classification", "missing-current", "both-zero", "stale"} {
			edits = append(edits, struct {
				name string
				edit func(*Input)
			}{binding.name + "/" + state, func(in *Input) {
				classification, current := binding.fields(in)
				switch state {
				case "missing-classification":
					*classification = 0
				case "missing-current":
					*current = 0
				case "both-zero":
					*classification, *current = 0, 0
				case "stale":
					*current = 3
				}
			}})
		}
	}
	edits = append(edits, struct {
		name string
		edit func(*Input)
	}{"empty-classification", func(in *Input) { in.Classification = Classification{} }})
	for _, tc := range edits {
		t.Run(tc.name, func(t *testing.T) {
			in := currentInput()
			tc.edit(&in)
			p := writerPolicy()
			p.Approvals.Default = Requirement{Selector: Human, Count: 2}
			p.Approvals.Rules = []Rule{{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}}}
			got := Evaluate(p, in)
			if got.Allowed || got.ReviewsKnown || len(got.Requirements) != 0 || !strings.Contains(strings.Join(got.Reasons, " "), "classification basis") {
				t.Fatalf("unbound classification reached exemption evaluation: %+v", got)
			}
		})
	}
}

func TestOnlyRelevantActorsAffectRequirements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Input, *Policy)
		known bool
	}{
		{"unrelated-stale-cache", func(in *Input, _ *Policy) { in.Actors["other"] = Actor{} }, true},
		{"missing-actor-old-revision", func(in *Input, _ *Policy) {
			in.Stances["other"] = Stance{UserID: "other", Revision: 9, Head: in.Current.Head, Kind: Approve}
		}, true},
		{"missing-actor-old-head", func(in *Input, _ *Policy) {
			in.Stances["other"] = Stance{UserID: "other", Revision: 1, Head: "old", Kind: RequestChanges}
		}, true},
		{"missing-actor-withdrawal", func(in *Input, _ *Policy) {
			in.Stances["other"] = Stance{UserID: "other", Revision: 1, Head: in.Current.Head, Kind: Withdraw}
		}, true},
		{"missing-current-actor", func(in *Input, _ *Policy) {
			in.Stances["other"] = Stance{UserID: "other", Revision: 1, Head: in.Current.Head, Kind: RequestChanges}
		}, false},
		{"outside-known-pool", func(in *Input, p *Policy) {
			in.Stances["other"] = Stance{UserID: "other", Revision: 1, Head: in.Current.Head, Kind: RequestChanges}
			p.Approvals.Default = Requirement{Selector: Group, Group: "g", Count: 1}
			p.Groups = map[string]IdentitySet{"g": {IDs: []string{"a"}, Resolved: true, Fresh: true}}
		}, true},
		{"unobserved-pool-member-without-stance", func(_ *Input, p *Policy) {
			p.Approvals.Default = Requirement{Selector: Group, Group: "g", Count: 1}
			p.Groups = map[string]IdentitySet{"g": {IDs: []string{"a", "other"}, Resolved: true, Fresh: true}}
		}, true},
		{"observed-unresolved-pool-member", func(in *Input, p *Policy) {
			in.Actors["other"] = Actor{}
			p.Approvals.Default = Requirement{Selector: Group, Group: "g", Count: 1}
			p.Groups = map[string]IdentitySet{"g": {IDs: []string{"a", "other"}, Resolved: true, Fresh: true}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, p := currentInput(), writerPolicy()
			addReviewer(&in, "a", HumanAccount, false)
			tc.edit(&in, &p)
			got := Evaluate(p, in)
			if got.Allowed != tc.known || got.ReviewsKnown != tc.known {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRequirementRetainsStanceEvidence(t *testing.T) {
	in, p := currentInput(), groupPolicy()
	addReviewer(&in, "b", HumanAccount, false)
	addReviewer(&in, "a", HumanAccount, true)
	addReviewer(&in, "c", HumanAccount, false)
	block := in.Stances["c"]
	block.Kind = RequestChanges
	block.Source.Sequence = 5
	block.Source.Time = time.Unix(100, 0).UTC()
	block.Source.Precision = ExactTime
	block.Source.ActingUserID = "c"
	in.Stances["c"] = block
	pool := p.Groups["platform"]
	pool.IDs = append(pool.IDs, "c")
	p.Groups["platform"] = pool
	got := Evaluate(p, in)
	if got.Allowed || !reflect.DeepEqual(got.BlockingIDs, []string{"c"}) {
		t.Fatalf("%+v", got)
	}
	for i, want := range [][]Stance{{in.Stances["a"], in.Stances["b"]}, {in.Stances["a"]}} {
		r := got.Requirements[i]
		if !reflect.DeepEqual(r.ReliedOn, want) || !reflect.DeepEqual(r.BlockingStances, []Stance{block}) {
			t.Fatalf("requirement %d lost counted or blocking provenance: %+v", i, r)
		}
	}
	in.Stances["a"].Receipt.BriefDigest = "mutated"
	if got.Requirements[0].ReliedOn[0].Receipt.BriefDigest != "brief" {
		t.Fatal("decision evidence aliases input receipt")
	}
}

func TestEmptyPathsNeverAuthorizePathExemption(t *testing.T) {
	for _, complete := range []bool{false, true} {
		in, p := currentInput(), writerPolicy()
		in.Classification.Paths = nil
		in.Classification.InventoryComplete = complete
		p.Approvals.Rules = []Rule{{When: Match{Paths: []string{"docs/**"}}, Require: Requirement{Selector: None}}}
		match, err := MatchRule(p.Approvals.Rules[0].When, in.Classification, in.AuthorKind)
		if err != nil || match != Unknown || Evaluate(p, in).Allowed {
			t.Fatalf("complete=%v match=%v err=%v", complete, match, err)
		}
	}
}

func TestReceiptCompleteness(t *testing.T) {
	binding := currentInput().Current
	if !binding.Recorded() || !currentInput().Classification.Inputs.Recorded() {
		t.Fatal("complete binding rejected")
	}
	// Every binding component must be present, including components that would
	// otherwise compare equal to an empty latest receipt.
	for _, field := range []string{"ChangeID", "Base", "Head", "MergeBase", "IntentDigest", "PolicyDigest", "BriefDigest", "Revision", "Generation"} {
		t.Run(field, func(t *testing.T) {
			in, p := currentInput(), writerPolicy()
			p.Approvals.Default.Receipt = true
			addReviewer(&in, "a", HumanAccount, true)
			reflect.ValueOf(&in.Current).Elem().FieldByName(field).SetZero()
			*in.Stances["a"].Receipt = in.Current
			if in.Current.Recorded() || Evaluate(p, in).Allowed {
				t.Fatal("incomplete receipt authorized approval")
			}
		})
	}
}

func TestPaddedGroupNamesRejected(t *testing.T) {
	for _, name := range []string{" platform", "platform ", "\tplatform", " "} {
		if (Requirement{Selector: Group, Group: name, Count: 1}).Validate() == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, source := range []string{
		"groups: {' platform': [alice]}\nqueues: [{name: main, approvals: {default: any_writer}}]",
		"groups: {platform: [alice]}\nqueues: [{name: main, approvals: {default: {group: ' platform'}}}]",
	} {
		if _, _, err := ParseCandidate([]byte(source)); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}
