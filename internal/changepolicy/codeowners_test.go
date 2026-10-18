package changepolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func ownedPath(path string, side OwnerSide, ids ...string) CodeOwner {
	return CodeOwner{Path: path, Side: side, LastMatchingEntry: path + " @owners", Status: OwnersResolved,
		Eligible: IdentitySet{IDs: ids, Resolved: true, Fresh: true}}
}

func codeOwnerInput(owners ...CodeOwner) Input {
	in := currentInput()
	in.Classification.CodeOwnersKnown = true
	in.Classification.CodeOwners = owners
	in.Classification.Paths = nil
	for _, owner := range owners {
		in.Classification.Paths = append(in.Classification.Paths, owner.Path)
	}
	return in
}

func codeOwnerPolicy(receipt bool) Policy {
	p := writerPolicy()
	p.Approvals.Rules = []Rule{{When: Match{Authors: []AccountKind{HumanAccount}}, Require: Requirement{Selector: CodeOwners, Receipt: receipt}}}
	return p
}

func codeOwnerResults(e Evaluation) []RequirementResult {
	var out []RequirementResult
	for _, r := range e.Requirements {
		if r.Requirement.Selector == CodeOwners {
			out = append(out, r)
		}
	}
	return out
}

func checkCodeOwners(t *testing.T, p Policy, in Input, allowed bool, obligations int) Evaluation {
	t.Helper()
	before, _ := json.Marshal(in)
	got := Evaluate(p, in)
	after, _ := json.Marshal(in)
	if got.Allowed != allowed || len(codeOwnerResults(got)) != obligations {
		t.Fatalf("allowed=%v obligations=%d: %+v", allowed, obligations, got)
	}
	if string(before) != string(after) || !reflect.DeepEqual(got, Evaluate(p, in)) {
		t.Fatal("mutated inputs or unstable evidence")
	}
	for _, r := range codeOwnerResults(got) {
		if r.Requirement.Count != 1 {
			t.Fatalf("expanded count: %+v", r)
		}
	}
	if p.Approvals.Rules[0].Require.Selector == CodeOwners && p.Approvals.Rules[0].Require.Count != 0 {
		t.Fatal("mutated static count")
	}
	return got
}

func TestCodeOwners(t *testing.T) {
	t.Run("code-owners-every-owned-file", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("src/a.go", NewSide, "a"))
		addReviewer(&in, "outsider", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
		addReviewer(&in, "a", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, true, 1)
	})
	t.Run("code-owners-disjoint-owners", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("src/a.go", NewSide, "a", "shared"), ownedPath("src/b.go", NewSide, "b", "shared"))
		addReviewer(&in, "a", HumanAccount, false)
		got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 2)
		if !got.Requirements[0].Met || got.Requirements[1].Met {
			t.Fatalf("cross-file union: %+v", got)
		}
		addReviewer(&in, "b", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, true, 2)
		delete(in.Stances, "a")
		delete(in.Stances, "b")
		addReviewer(&in, "shared", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, true, 2)
	})
	for _, status := range []CodeOwnerStatus{OwnersUncovered, OwnersOwnerless} {
		name := "code-owners-uncovered-file"
		if status == OwnersOwnerless {
			name = "code-owners-ownerless-entry"
		}
		t.Run(name, func(t *testing.T) {
			owner := CodeOwner{Path: "src/free.go", Side: NewSide, Status: status}
			if status == OwnersOwnerless {
				owner.LastMatchingEntry = "src/free.go"
			}
			in := codeOwnerInput(owner)
			checkCodeOwners(t, codeOwnerPolicy(false), in, true, 0)
			// Known uncovered/ownerless paths alongside an owned file cannot erase it.
			in.Classification.CodeOwners = append(in.Classification.CodeOwners, ownedPath("src/a.go", NewSide, "a"))
			checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
		})
	}
	t.Run("code-owners-incomplete-inventory", func(t *testing.T) {
		for _, known := range []bool{false, true} {
			in := codeOwnerInput()
			in.Classification.InventoryComplete = false
			in.Classification.CodeOwnersKnown = known
			got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
			if !codeOwnerResults(got)[0].Unresolved || got.ReviewsKnown {
				t.Fatalf("incomplete inventory authorized vacuity: %+v", got)
			}
		}
	})
	t.Run("code-owners-unknown-owners", func(t *testing.T) {
		in := codeOwnerInput()
		in.Classification.CodeOwnersKnown = false
		got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
		if !codeOwnerResults(got)[0].Unresolved {
			t.Fatal("unknown owners not blocking")
		}
	})
	for _, name := range []string{"code-owners-mixed-entry", "code-owners-unknown-team-membership"} {
		t.Run(name, func(t *testing.T) {
			for _, known := range []bool{false, true} {
				owner := ownedPath("src/a.go", NewSide, "a")
				owner.Status = OwnersUnresolved
				owner.Eligible.Resolved = false
				owner.LastMatchingEntry = "src/a.go @a ops@example.com"
				if strings.Contains(name, "team") {
					owner.LastMatchingEntry = "src/a.go @a @acme/unreadable"
				}
				owner.Eligible.Reason = name
				in := codeOwnerInput(owner)
				in.Classification.CodeOwnersKnown = known
				addReviewer(&in, "a", HumanAccount, false)
				got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1+boolInt(!known))
				if !codeOwnerResults(got)[len(codeOwnerResults(got))-1].Unresolved {
					t.Fatal("partial owner approval cleared unknown pool")
				}
			}
		})
	}
	t.Run("code-owners-excluded-sole-owner", func(t *testing.T) {
		for _, id := range []string{"author", "submitter", "accountable"} {
			t.Run(id, func(t *testing.T) {
				in := codeOwnerInput(ownedPath("src/a.go", NewSide, id))
				addReviewer(&in, id, HumanAccount, true)
				got := checkCodeOwners(t, codeOwnerPolicy(true), in, false, 1)
				if got.Requirements[0].Unresolved || len(got.Requirements[0].EligibleIDs) != 0 {
					t.Fatal("excluded owner is not a known empty pool")
				}
			})
		}
	})
	t.Run("code-owners-owned-deletion", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("src/deleted.go", OldSide, "old"))
		in.Classification.Files = []FileEvidence{{OldPath: "src/deleted.go", OldBlob: "blob", OldMode: 0100644}}
		addReviewer(&in, "new", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
		addReviewer(&in, "old", HumanAccount, false)
		got := checkCodeOwners(t, codeOwnerPolicy(false), in, true, 1)
		if !strings.Contains(got.Requirements[0].Origin, "src/deleted.go (old)") {
			t.Fatal("old-side provenance missing")
		}
	})
	t.Run("code-owners-rename-disjoint-owners", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("old/a.go", OldSide, "old"), ownedPath("new/a.go", NewSide, "new"))
		in.Classification.Files = []FileEvidence{{OldPath: "old/a.go", NewPath: "new/a.go", Renamed: true, RenamePaired: true}}
		addReviewer(&in, "new", HumanAccount, false)
		got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 2)
		if got.Requirements[0].Met || !got.Requirements[1].Met {
			t.Fatal("new owner cleared old side")
		}
		addReviewer(&in, "old", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, true, 2)
	})
	t.Run("code-owners-owned-to-uncovered-rename", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("old/a.go", OldSide, "old"), CodeOwner{Path: "new/a.go", Side: NewSide, Status: OwnersUncovered})
		in.Classification.Files = []FileEvidence{{OldPath: "old/a.go", NewPath: "new/a.go", Renamed: true, RenamePaired: true}}
		checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
		addReviewer(&in, "old", HumanAccount, false)
		checkCodeOwners(t, codeOwnerPolicy(false), in, true, 1)
	})
	t.Run("code-owners-per-file-receipts", func(t *testing.T) {
		in := codeOwnerInput(ownedPath("src/a.go", NewSide, "a", "shared"), ownedPath("src/b.go", NewSide, "b", "shared"))
		addReviewer(&in, "a", HumanAccount, true)
		addReviewer(&in, "b", HumanAccount, false)
		got := checkCodeOwners(t, codeOwnerPolicy(true), in, false, 2)
		if !got.Requirements[0].Met || got.Requirements[1].Met || len(got.ReviewLogins) != 2 {
			t.Fatal("receipt leaked across files or native review lost")
		}
		addReviewer(&in, "b", HumanAccount, true)
		checkCodeOwners(t, codeOwnerPolicy(true), in, true, 2)
		stale := in.Current
		stale.IntentDigest = "stale"
		s := in.Stances["b"]
		s.Receipt = &stale
		in.Stances["b"] = s
		checkCodeOwners(t, codeOwnerPolicy(true), in, false, 2)
		delete(in.Stances, "a")
		delete(in.Stances, "b")
		addReviewer(&in, "shared", HumanAccount, true)
		got = checkCodeOwners(t, codeOwnerPolicy(true), in, true, 2)
		for _, r := range got.Requirements {
			if len(r.ReliedOn) != 1 || r.ReliedOn[0].Receipt == nil {
				t.Fatal("receipt evidence missing")
			}
		}
	})
	t.Run("code-owners-policy-independent", func(t *testing.T) {
		for _, withRule := range []bool{false, true} {
			in := codeOwnerInput(ownedPath("docs/CODEOWNERS", NewSide, "a"))
			in.Classification.Facts["policy"] = True
			in.Classification.PolicyOwnersKnown = true
			in.Classification.PolicyOwners = []PolicyOwner{{Path: "docs/CODEOWNERS", Eligible: IdentitySet{IDs: []string{"b"}, Resolved: true, Fresh: true}}}
			addReviewer(&in, "a", HumanAccount, false)
			p := codeOwnerPolicy(false)
			if !withRule {
				p.Approvals.Rules[0].Require = Requirement{Selector: None}
			}
			if got := Evaluate(p, in); got.Allowed {
				t.Fatal("code-owner rule removed mandatory policy pool")
			}
			addReviewer(&in, "b", HumanAccount, false)
			if got := Evaluate(p, in); !got.Allowed {
				t.Fatalf("independent requirements: %+v", got)
			}
			// A shared owner can satisfy both independent obligations.
			in.Classification.PolicyOwners[0].Eligible.IDs = []string{"a"}
			delete(in.Stances, "b")
			if got := Evaluate(p, in); !got.Allowed {
				t.Fatal("shared policy owner did not satisfy both")
			}
			in.Classification.CodeOwners[0].Status = OwnersOwnerless
			in.Classification.CodeOwners[0].LastMatchingEntry = "docs/CODEOWNERS"
			in.Classification.CodeOwners[0].Eligible = IdentitySet{}
			in.Classification.PolicyOwners[0].Eligible = IdentitySet{Resolved: true, Fresh: true}
			if got := Evaluate(p, in); got.Allowed || !got.Requirements[len(got.Requirements)-1].Unresolved {
				t.Fatal("ownerless policy path ceased blocking")
			}
			// Mixed email evidence is independently resolvable for policy owners.
			in.Classification.CodeOwners[0] = ownedPath("docs/CODEOWNERS", NewSide, "a")
			in.Classification.CodeOwners[0].Status = OwnersUnresolved
			in.Classification.CodeOwners[0].LastMatchingEntry = "docs/CODEOWNERS @a ops@example.com"
			in.Classification.CodeOwnersKnown = false
			in.Classification.PolicyOwners[0].Eligible = IdentitySet{IDs: []string{"a"}, Resolved: true, Fresh: true}
			got := Evaluate(p, in)
			if got.Allowed == withRule || !got.Requirements[len(got.Requirements)-1].Met {
				t.Fatalf("shared resolution status: %+v", got)
			}
		}
	})
	t.Run("code-owners-inactive-rule", func(t *testing.T) {
		in := currentInput()
		addReviewer(&in, "a", HumanAccount, false)
		p := codeOwnerPolicy(false)
		p.Approvals.Rules[0].When = Match{Authors: []AccountKind{AgentAccount}}
		if got := Evaluate(p, in); !got.Allowed || len(codeOwnerResults(got)) != 0 {
			t.Fatal("inactive code-owner rule inspected unknown evidence")
		}
		p.Approvals.Rules[0].When = Match{Labels: []string{"generated"}}
		in.Classification.Facts["generated"] = Unknown
		if got := Evaluate(p, in); got.Allowed || len(codeOwnerResults(got)) != 1 {
			t.Fatal("unknown code-owner rule not enforced")
		}
	})
	t.Run("code-owners-stale-or-malformed-evidence", func(t *testing.T) {
		for _, edit := range []func(*CodeOwner){
			func(o *CodeOwner) { o.Eligible.Fresh = false }, func(o *CodeOwner) { o.Eligible.Resolved = false },
			func(o *CodeOwner) { o.Path = "" }, func(o *CodeOwner) { o.Side = "" }, func(o *CodeOwner) { o.LastMatchingEntry = "" },
			func(o *CodeOwner) { o.Status = "invalid" }, func(o *CodeOwner) { o.Eligible.IDs = nil },
		} {
			in := codeOwnerInput(ownedPath("src/a.go", NewSide, "a"))
			edit(&in.Classification.CodeOwners[0])
			addReviewer(&in, "a", HumanAccount, false)
			got := checkCodeOwners(t, codeOwnerPolicy(false), in, false, 1)
			if !got.Requirements[0].Unresolved {
				t.Fatal("bad evidence did not fail closed")
			}
		}
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCodeOwnersIndependentCount(t *testing.T) {
	t.Run("code-owners-independent-count", func(t *testing.T) {
		for _, kind := range []string{"owned", "uncovered", "empty"} {
			t.Run(kind, func(t *testing.T) {
				in := codeOwnerInput()
				switch kind {
				case "owned":
					in = codeOwnerInput(ownedPath("src/a.go", NewSide, "a"))
				case "uncovered":
					in = codeOwnerInput(CodeOwner{Path: "free.go", Side: NewSide, Status: OwnersUncovered})
				}
				floor := Requirement{Selector: AnyWriter, Count: 2}
				p := Policy{Approvals: &Approvals{Default: floor, Rules: []Rule{
					{When: Match{Paths: []string{"**"}}, Require: floor},
					{When: Match{Touches: []string{"**"}}, Require: Requirement{Selector: CodeOwners}},
				}}}
				paths, _ := MatchRule(p.Approvals.Rules[0].When, in.Classification, in.AuthorKind)
				touches, _ := MatchRule(p.Approvals.Rules[1].When, in.Classification, in.AuthorKind)
				if kind == "empty" && (paths != Unknown || touches != False) {
					t.Fatalf("empty match: paths=%v touches=%v", paths, touches)
				}
				addReviewer(&in, "a", HumanAccount, false)
				got := Evaluate(p, in)
				if got.Allowed {
					t.Fatal("owner rule displaced count")
				}
				addReviewer(&in, "b", HumanAccount, false)
				got = Evaluate(p, in)
				if !got.Allowed {
					t.Fatalf("two eligible reviewers blocked: %+v", got)
				}
				if kind == "empty" {
					if len(got.Requirements) != 2 || got.Requirements[0].Origin != "rules[0]" || got.Requirements[1].Origin != "default" {
						t.Fatalf("unknown rule plus default missing: %+v", got)
					}
				}
				wantOwners := 0
				if kind == "owned" {
					wantOwners = 1
				}
				if len(codeOwnerResults(got)) != wantOwners {
					t.Fatal("unexpected owners")
				}
			})
		}
	})
}

func TestCodeOwnersParserSchemas(t *testing.T) {
	schemas := []*jsonschema.Schema{candidateSchema(t)}
	data, err := os.ReadFile("testdata/candidate.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("full.json", doc); err != nil {
		t.Fatal(err)
	}
	full, err := compiler.Compile("full.json")
	if err != nil {
		t.Fatal(err)
	}
	schemas = append(schemas, full)
	t.Run("code-owners-invalid-combinations", func(t *testing.T) {
		for _, tc := range []struct {
			form  string
			valid bool
		}{
			{"{code_owners: true}", true}, {"{code_owners: true, receipt: true}", true},
			{"{code_owners: true, count: 1}", false}, {"{code_owners: true, count: 0}", false}, {"{code_owners: true, count: null}", false},
			{"{code_owners: true, group: platform}", false}, {"{code_owners: true, group: ''}", false},
			{"{code_owners: true, any_writer: true}", false}, {"{code_owners: true, any_writer: false}", false},
			{"{code_owners: true, human: true}", false}, {"{code_owners: true, human: false}", false},
			{"{code_owners: false}", false}, {"{code_owners: 'true'}", false}, {"{code_owners: 1}", false}, {"{code_owners: null}", false},
			{"code_owners", false}, {"'code_owners: true'", false}, {"[code_owners]", false},
			{"{code_owners: true, receipt: false}", false}, {"{code_owners: true, receipt: 'true'}", false}, {"{code_owners: true, none: true}", false},
		} {
			t.Run(tc.form, func(t *testing.T) {
				source := fmt.Sprintf("groups: {platform: [alice]}\nqueues: [{name: main, approvals: {default: any_writer, rules: [{when: {paths: ['**']}, require: %s}]}}]", tc.form)
				r, _, err := ParseCandidate([]byte(source))
				if (err == nil) != tc.valid {
					t.Fatalf("valid=%v parser=%v", tc.valid, err)
				}
				var value map[string]any
				if err := yaml.Unmarshal([]byte(source), &value); err != nil {
					t.Fatal(err)
				}
				if err := schemas[0].Validate(value); (err == nil) != tc.valid {
					t.Fatalf("projection valid=%v schema=%v", tc.valid, err)
				}
				value["version"] = 1
				value["queues"].([]any)[0].(map[string]any)["merge_when"] = []any{"check-success = ci"}
				if err := schemas[1].Validate(value); (err == nil) != tc.valid {
					t.Fatalf("full valid=%v schema=%v", tc.valid, err)
				}
				if tc.valid {
					req := r.Queues[0].Approvals.Rules[0].Require
					if req.Selector != CodeOwners || req.Count != 0 || req.Validate() != nil {
						t.Fatalf("static contract: %+v", req)
					}
				}
			})
		}
		// Duplicate keys remain rejected even in the special object form.
		for _, form := range []string{"{code_owners: true, code_owners: true}", "{code_owners: true, receipt: true, receipt: true}"} {
			source := "queues: [{name: main, approvals: {default: any_writer, rules: [{when: {paths: ['**']}, require: " + form + "}]}}]"
			if _, _, err := ParseCandidate([]byte(source)); err == nil {
				t.Fatal("duplicate key accepted")
			}
		}
		for _, r := range []Requirement{{Selector: CodeOwners, Count: 1}, {Selector: CodeOwners, Group: "platform"}, {Selector: CodeOwners, Human: true}} {
			if r.Validate() == nil {
				t.Fatalf("typed invalid combination accepted: %+v", r)
			}
		}
	})
	t.Run("code-owners-default-rejected", func(t *testing.T) {
		for _, receipt := range []bool{false, true} {
			form := "{code_owners: true}"
			if receipt {
				form = "{code_owners: true, receipt: true}"
			}
			source := "queues: [{name: main, approvals: {default: " + form + "}}]"
			if _, _, err := ParseCandidate([]byte(source)); err == nil {
				t.Fatal("parser accepted code-owner default")
			}
			var value map[string]any
			if err := yaml.Unmarshal([]byte(source), &value); err != nil {
				t.Fatal(err)
			}
			if err := schemas[0].Validate(value); err == nil {
				t.Fatal("projection accepted code-owner default")
			}
			value["version"] = 1
			value["queues"].([]any)[0].(map[string]any)["merge_when"] = []any{"check-success = ci"}
			if err := schemas[1].Validate(value); err == nil {
				t.Fatal("full schema accepted code-owner default")
			}
			in := codeOwnerInput()
			p := codeOwnerPolicy(receipt)
			p.Approvals.Default = Requirement{Selector: CodeOwners, Receipt: receipt}
			if got := Evaluate(p, in); got.Allowed || got.ReviewsKnown || len(got.Requirements) != 0 {
				t.Fatalf("evaluator accepted invalid default: %+v", got)
			}
			d := digestInputs(t)
			d.Queue.Approvals.Default = p.Approvals.Default
			if _, err := PolicyDigest(d); err == nil {
				t.Fatal("digest accepted code-owner default")
			}
		}
	})
}

func TestCodeOwnersDigestVectors(t *testing.T) {
	for _, tc := range []struct {
		receipt bool
		want    string
	}{
		{false, "cc891b1319abcb4512391fc471ea6fe1389d4c0cd136f63082e8fbd823bf49a7"}, {true, "048bd4409f31d0d3f3236e68bdb94e648100492c0026103455ee7b3961600fc9"},
	} {
		t.Run(fmt.Sprint(tc.receipt), func(t *testing.T) {
			d := digestInputs(t)
			d.Queue.Approvals.Rules = append(d.Queue.Approvals.Rules, Rule{When: Match{Touches: []string{"**"}}, Require: Requirement{Selector: CodeOwners, Receipt: tc.receipt}})
			got, err := PolicyDigest(d)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("digest=%s want=%s", got, tc.want)
			}
		})
	}
	// Existing semantic and legacy vectors remain pinned in digest_test.go and
	// compatibility_test.go; no Requirement field or serialization was added.
	const oldEncoding = `{"Selector":"any_writer","Group":"","Human":false,"Receipt":false,"Count":1}`
	got, err := json.Marshal(Requirement{Selector: AnyWriter, Count: 1})
	if err != nil || string(got) != oldEncoding {
		t.Fatalf("legacy requirement bytes=%s err=%v", got, err)
	}
}
