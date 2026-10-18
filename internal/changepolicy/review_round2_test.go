package changepolicy

import (
	"fmt"
	"testing"
)

// Saved Astra round-2 probes guard independent pools and malformed evidence.
func round2OwnerInput() Input {
	in := reviewInput()
	in.Classification.Paths = []string{"src/a.go", "src/b.go"}
	in.Classification.Files = []FileEvidence{
		{OldPath: "src/a.go", NewPath: "src/a.go", OldBlob: "a-old", NewBlob: "a-new"},
		{OldPath: "src/b.go", NewPath: "src/b.go", OldBlob: "b-old", NewBlob: "b-new"},
	}
	in.Classification.CodeOwnersKnown = true
	in.Classification.CodeOwners = []CodeOwner{
		{Path: "src/a.go", Side: NewSide, LastMatchingEntry: "src/a.go @alice", Status: OwnersResolved, Eligible: IdentitySet{IDs: []string{"alice"}, Resolved: true, Fresh: true}},
		{Path: "src/b.go", Side: NewSide, LastMatchingEntry: "src/b.go @bob", Status: OwnersResolved, Eligible: IdentitySet{IDs: []string{"bob"}, Resolved: true, Fresh: true}},
	}
	return in
}

func round2OwnerPolicy(receipt bool) Policy {
	return Policy{Approvals: &Approvals{
		Default: Requirement{Selector: AnyWriter, Count: 2},
		Rules:   []Rule{{When: Match{Touches: []string{"**"}}, Require: Requirement{Selector: CodeOwners, Receipt: receipt}}},
	}}
}

func TestRound2OwnerSafetyTable(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*Input)
		allowed bool
	}{
		{"disjoint-only-alice", func(*Input) {}, false},
		{"disjoint-both-owners", func(in *Input) { reviewActor(in, "bob", HumanAccount, 1) }, true},
		{"nonowner-is-writer", func(in *Input) { delete(in.Stances, "alice"); reviewActor(in, "outsider", HumanAccount, 1) }, false},
		{"shared-owner", func(in *Input) {
			in.Classification.CodeOwners[1].Eligible.IDs = []string{"alice"}
			in.Classification.CodeOwners[1].LastMatchingEntry = "src/b.go @alice"
		}, true},
		{"uncovered-second-file", func(in *Input) {
			in.Classification.CodeOwners[1] = CodeOwner{Path: "src/b.go", Side: NewSide, Status: OwnersUncovered}
		}, true},
		{"ownerless-second-file", func(in *Input) {
			in.Classification.CodeOwners[1] = CodeOwner{Path: "src/b.go", Side: NewSide, LastMatchingEntry: "src/b.go", Status: OwnersOwnerless}
		}, true},
		{"unresolved-with-known-approver", func(in *Input) {
			in.Classification.CodeOwners[1].Status = OwnersUnresolved
			reviewActor(in, "bob", HumanAccount, 1)
		}, false},
		{"unresolved-membership", func(in *Input) {
			in.Classification.CodeOwners[1].Eligible.Resolved = false
			reviewActor(in, "bob", HumanAccount, 1)
		}, false},
		{"stale-membership", func(in *Input) {
			in.Classification.CodeOwners[1].Eligible.Fresh = false
			reviewActor(in, "bob", HumanAccount, 1)
		}, false},
		{"global-unknown-even-with-approved-pools", func(in *Input) { in.Classification.CodeOwnersKnown = false; reviewActor(in, "bob", HumanAccount, 1) }, false},
		{"inventory-incomplete-even-with-approved-pools", func(in *Input) { in.Classification.InventoryComplete = false; reviewActor(in, "bob", HumanAccount, 1) }, false},
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
		{"unresolved-code-pool-does-not-infect-policy-only", func(in *Input) { in.Classification.CodeOwnersKnown = false }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := round2OwnerInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			tc.edit(&in)
			got := Evaluate(round2OwnerPolicy(false), in)
			if got.Allowed != tc.allowed {
				t.Fatalf("wantAllowed=%v got=%+v", tc.allowed, got)
			}
		})
	}
}

func TestRound2OwnerExhaustivePools(t *testing.T) {
	// Two independent nonempty pools over three actors; five stances each;
	// native-permitted and receipt-only requirements: 12,250 evaluations.
	ids := []string{"alice", "bob", "carol"}
	cases := 0
	for left := 1; left < 8; left++ {
		for right := 1; right < 8; right++ {
			for _, receipt := range []bool{false, true} {
				for states := 0; states < 125; states++ {
					in := round2OwnerInput()
					state := [3]int{}
					n := states
					for i, id := range ids {
						state[i] = n % 5
						n /= 5
						reviewActor(&in, id, HumanAccount, state[i])
					}
					want := true
					for file, mask := range []int{left, right} {
						in.Classification.CodeOwners[file].Eligible.IDs = nil
						approvals := 0
						for i, id := range ids {
							if mask&(1<<i) == 0 {
								continue
							}
							in.Classification.CodeOwners[file].Eligible.IDs = append(in.Classification.CodeOwners[file].Eligible.IDs, id)
							if state[i] == 4 {
								want = false
							}
							if state[i] == 2 || (!receipt && state[i] == 1) {
								approvals++
							}
						}
						if approvals == 0 {
							want = false
						}
					}
					got := Evaluate(round2OwnerPolicy(receipt), in)
					cases++
					if got.Allowed != want {
						t.Fatalf("pools=%03b/%03b states=%v receipt=%v want=%v got=%+v", left, right, state, receipt, want, got)
					}
				}
			}
		}
	}
	t.Logf("checked %d independent pool/stance/receipt combinations", cases)
}

func TestRound2MissingOwnerEvidenceFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Input)
	}{
		{"no-owner-rows-for-nonempty-diff", func(in *Input) { in.Classification.CodeOwners = nil; in.Stances = map[string]Stance{} }},
		{"missing-second-file", func(in *Input) { in.Classification.CodeOwners = in.Classification.CodeOwners[:1] }},
		{"duplicate-first-file-replaces-second", func(in *Input) { in.Classification.CodeOwners[1] = in.Classification.CodeOwners[0] }},
		{"unrelated-owned-path", func(in *Input) {
			in.Classification.CodeOwners = in.Classification.CodeOwners[:1]
			in.Classification.CodeOwners[0].Path = "unrelated.go"
		}},
		{"rename-missing-old-side", func(in *Input) {
			in.Classification.Paths = []string{"old/a.go", "src/a.go"}
			in.Classification.Files = []FileEvidence{{OldPath: "old/a.go", NewPath: "src/a.go", Renamed: true, RenamePaired: true}}
			in.Classification.CodeOwners = in.Classification.CodeOwners[:1]
		}},
		{"deletion-recorded-as-new-side", func(in *Input) {
			in.Classification.Paths = []string{"src/a.go"}
			in.Classification.Files = []FileEvidence{{OldPath: "src/a.go", OldBlob: "a-old"}}
			in.Classification.CodeOwners = in.Classification.CodeOwners[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := round2OwnerInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			tc.edit(&in)
			got := Evaluate(round2OwnerPolicy(false), in)
			if got.Allowed {
				t.Fatalf("missing or misbound per-file evidence allowed: %+v", got)
			}
		})
	}
}

func TestRound2ContradictoryOwnerAbsenceFailsClosed(t *testing.T) {
	for _, status := range []CodeOwnerStatus{OwnersUncovered, OwnersOwnerless} {
		for _, mode := range []string{"known-owned-pool", "unknown-membership", "empty-path-side", "email-entry"} {
			t.Run(fmt.Sprintf("%s/%s", status, mode), func(t *testing.T) {
				in := round2OwnerInput()
				reviewActor(&in, "alice", HumanAccount, 1)
				owner := &in.Classification.CodeOwners[1]
				owner.Status = status
				switch mode {
				case "unknown-membership":
					owner.Eligible = IdentitySet{Reason: "team could not be read"}
				case "empty-path-side":
					owner.Path = ""
					owner.Side = ""
					owner.Eligible = IdentitySet{}
				case "email-entry":
					owner.LastMatchingEntry = "src/b.go ops@example.com"
					owner.Eligible = IdentitySet{Reason: "email unresolved"}
				}
				got := Evaluate(round2OwnerPolicy(false), in)
				if got.Allowed {
					t.Fatalf("contradictory absence erased obligation: %+v", got)
				}
			})
		}
	}
}

func TestRound2InvalidDefaultEveryBoundary(t *testing.T) {
	for _, r := range []Requirement{
		{Selector: CodeOwners}, {Selector: CodeOwners, Receipt: true},
		{Selector: CodeOwners, Count: 1}, {Selector: CodeOwners, Count: -1},
		{Selector: CodeOwners, Group: "platform"}, {Selector: CodeOwners, Human: true},
		{Selector: None}, {},
	} {
		t.Run(fmt.Sprintf("%+v", r), func(t *testing.T) {
			in := round2OwnerInput()
			p := round2OwnerPolicy(false)
			p.Approvals.Default = r
			p.Approvals.Rules = []Rule{{When: Match{Authors: []AccountKind{HumanAccount}}, Require: Requirement{Selector: None}}}
			if got := Evaluate(p, in); got.Allowed {
				t.Fatalf("invalid default bypassed by none: %+v", got)
			}
			d := digestInputs(t)
			d.Queue.Approvals.Default = r
			if digest, err := PolicyDigest(d); err == nil {
				t.Fatalf("invalid default digest=%s", digest)
			}
		})
	}
}
