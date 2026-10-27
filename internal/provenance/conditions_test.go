package provenance

import (
	"fmt"
	"reflect"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func expression(t *testing.T, text string) condition.Expression {
	t.Helper()
	e, err := ParseCondition(text)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestThreeValuedOperators(t *testing.T) {
	values := []cp.Truth{cp.Unknown, cp.False, cp.True}
	expr := expression(t, "provenance = agent")
	atom := func(v cp.Truth) condition.Expression {
		switch v {
		case cp.True:
			return condition.All()
		case cp.False:
			return condition.Any()
		default:
			return expr
		}
	}
	and := [][]cp.Truth{{cp.Unknown, cp.False, cp.Unknown}, {cp.False, cp.False, cp.False}, {cp.Unknown, cp.False, cp.True}}
	or := [][]cp.Truth{{cp.Unknown, cp.Unknown, cp.True}, {cp.Unknown, cp.False, cp.True}, {cp.True, cp.True, cp.True}}
	not := []cp.Truth{cp.Unknown, cp.True, cp.False}
	for i, a := range values {
		t.Run(fmt.Sprintf("not-%d", a), func(t *testing.T) {
			if got := Truth(condition.Not(atom(a)), condition.Snapshot{}); got != not[i] {
				t.Fatal(got)
			}
		})
		for j, b := range values {
			t.Run(fmt.Sprintf("and-or-%d-%d", a, b), func(t *testing.T) {
				if got := Truth(condition.All(atom(a), atom(b)), nil); got != and[i][j] {
					t.Fatalf("AND got %v", got)
				}
				if got := Truth(condition.Any(atom(a), atom(b)), nil); got != or[i][j] {
					t.Fatalf("OR got %v", got)
				}
			})
		}
	}
	inputs := condition.Snapshot{"provenance": condition.String("agent"), "agent-commits": condition.Strings("a", "b"), "checkpoint-present": condition.Bool(false)}
	for _, text := range []string{"provenance = agent", "provenance\n=\nagent", "provenance != human", "#agent-commits = 2", "#agent-commits != 0", "#agent-commits < 3", "#agent-commits <= 2", "#agent-commits > 1", "#agent-commits >= 2", "agent-commits = a", "agent-commits ~= ^a$", "agent-commits glob a*", "-checkpoint-present", "checkpoint-present = false", "checkpoint-present != true"} {
		t.Run(text, func(t *testing.T) {
			e := expression(t, text)
			if Truth(e, inputs) != cp.True {
				t.Fatal("known condition failed")
			}
			if Truth(e, condition.Snapshot{}) != cp.Unknown || Truth(condition.Not(e), condition.Snapshot{}) != cp.Unknown {
				t.Fatal("unknown passed or became false")
			}
		})
	}
	for _, text := range []string{"provenance = robot", "provenance ~= agent", "#provenance > 0", "#agent-commits glob *", "checkpoint-present > 0"} {
		if _, err := ParseCondition(text); err == nil {
			t.Fatal("invalid condition accepted", text)
		}
	}
	// Frozen journal evidence must not silently acquire the candidate vocabulary.
	if err := expr.Record(inputs).Validate(); err == nil {
		t.Fatal("candidate evidence accepted by frozen validator")
	}
}

func TestNoDeclarationAndInventoryClasses(t *testing.T) {
	human := Declaration{Class: Human}
	agent := Declaration{Class: Agent}
	for _, tc := range []struct {
		name     string
		commits  []Commit
		complete bool
		class    Class
		count    int
	}{
		{"empty", nil, true, Undeclared, 0},
		{"no declaration", []Commit{{ObjectID: "a", Complete: true}}, true, Undeclared, 0},
		{"all human", []Commit{{"a", true, []Declaration{human}}, {"b", true, []Declaration{human}}}, true, Human, 0},
		{"part human", []Commit{{"a", true, []Declaration{human}}, {"b", true, nil}}, true, Undeclared, 0},
		{"agent and undeclared", []Commit{{"a", true, []Declaration{agent, agent}}, {"b", true, nil}}, true, Agent, 1},
		{"across commits mixed", []Commit{{"a", true, []Declaration{agent}}, {"b", true, []Declaration{human}}}, true, Mixed, 1},
		{"incomplete inventory", nil, false, "", 0},
		{"incomplete source", []Commit{{"a", false, []Declaration{agent}}}, true, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := DeclarationAttributes(tc.commits, tc.complete)
			if tc.class == "" {
				for _, text := range []string{"provenance = undeclared", "#agent-commits = 0"} {
					if Truth(condition.Not(expression(t, text)), inputs) != cp.Unknown {
						t.Fatal("partial input authorized")
					}
				}
			} else if Truth(expression(t, "provenance = "+string(tc.class)), inputs) != cp.True || Truth(expression(t, fmt.Sprintf("#agent-commits = %d", tc.count)), inputs) != cp.True {
				t.Fatal(inputs)
			}
			if Truth(expression(t, "#unresolved-checkpoints = 0"), inputs) != cp.Unknown {
				t.Fatal("invented resolver knowledge")
			}
		})
	}
}

func TestPendingWhenRequiresTrueObligation(t *testing.T) {
	for _, trigger := range []cp.Truth{cp.Unknown, cp.False, cp.True} {
		for _, obligation := range []cp.Truth{cp.Unknown, cp.False, cp.True} {
			t.Run(fmt.Sprintf("%d-%d", trigger, obligation), func(t *testing.T) {
				atom := func(v cp.Truth) condition.Expression {
					switch v {
					case cp.True:
						return condition.All()
					case cp.False:
						return condition.Any()
					default:
						return expression(t, "checkpoint-present")
					}
				}
				c := atom(obligation)
				r := Rule{When: atom(trigger), Conditions: &c}
				got := EvaluateRequirements(condition.All(), nil, cp.Evaluation{}, nil, []Rule{r}, nil)
				want := cp.Unknown
				switch {
				case trigger == cp.False || obligation == cp.True:
					want = cp.True
				case trigger == cp.True:
					want = obligation
				}
				if got.Result != want {
					t.Fatalf("got %v want %v", got.Result, want)
				}
			})
		}
	}
}

func approvalFixture(t *testing.T, pending bool) (cp.Evaluation, cp.Evaluation) {
	t.Helper()
	in := cp.Input{Classification: cp.Classification{Inputs: cp.ClassificationInputs{ChangeID: "change", Revision: 1, Base: "base", MergeBase: "merge", Head: "head", PolicyDigest: "policy", Generation: 1, ClassifierVersion: "classifier"}, InventoryComplete: true, PathsSafe: true, Facts: map[string]cp.Truth{"policy": cp.False}}, Current: cp.ReceiptIdentity{ChangeID: "change", Head: "head", Revision: 1, Base: "base", MergeBase: "merge", PolicyDigest: "policy", Generation: 1}, Actors: map[string]cp.Actor{}, Stances: map[string]cp.Stance{}}
	for _, name := range []string{"alice", "bob"} {
		in.Actors[name] = cp.Actor{Identity: cp.Identity{ID: name, Login: name, Kind: cp.HumanAccount}, Grant: cp.WriteGrant{SubjectID: name, ReturnedID: name, Fresh: true, Write: cp.True}}
		in.Stances[name] = cp.Stance{UserID: name, Kind: cp.Approve, Head: "head", Revision: 1, Source: cp.StanceSource{Channel: cp.NativeReview, ID: name}}
	}
	groups := map[string]cp.IdentitySet{"platform": {IDs: []string{"alice"}, Resolved: true, Fresh: !pending}, "security": {IDs: []string{"bob"}, Resolved: true, Fresh: true}}
	eval := func(group string) cp.Evaluation {
		return cp.Evaluate(cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Group, Group: group, Count: 1}}, Groups: groups}, in)
	}
	base, local := eval("platform"), eval("security")
	if !local.Allowed || !pending && !base.Allowed {
		t.Fatalf("approval fixtures: %+v %+v", base, local)
	}
	return base, local
}

func TestReviewerEligibilityAcceptanceCases(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, second := range []bool{false, true} {
			t.Run(fmt.Sprintf("pending=%v/second=%v", pending, second), func(t *testing.T) {
				base, local := approvalFixture(t, pending)
				baseline := expression(t, "#approved-reviews-by >= 2")
				when := expression(t, "provenance = agent")
				if second {
					when = condition.Not(expression(t, "approved-reviews-by = bob"))
				}
				conditions := expression(t, "approved-reviews-by = bob")
				rule := Rule{When: when, Conditions: &conditions, Approvals: &cp.Requirement{Selector: cp.Group, Group: "security", Count: 1}}
				attrs := condition.Snapshot{"provenance": condition.String("agent")}
				disabled := EvaluateRequirements(baseline, nil, base, attrs, nil, nil)
				before := baseline.Record(condition.Snapshot{"approved-reviews-by": base.ReviewAttribute()})
				for _, rules := range [][]Rule{{rule}, {rule, {When: expression(t, "provenance = human"), Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}}, {{When: expression(t, "provenance = human"), Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}, rule}} {
					locals := []cp.Evaluation{local, local}
					got := EvaluateRequirements(baseline, nil, base, attrs, rules, locals)
					index := 0
					if len(rules) == 2 && rules[0].When.String() != rule.When.String() {
						index = 1
					}
					rr := got.Rules[index]
					if got.Baseline != disabled.Baseline || got.Result == cp.True {
						t.Fatal("added pool changed baseline", got)
					}
					want := cp.True
					if pending && second {
						want = cp.Unknown
					}
					if rr.Trigger != want {
						t.Fatalf("trigger=%v want %v", rr.Trigger, want)
					}
					if pending {
						if rr.Obligation != cp.Unknown || got.Baseline != cp.Unknown {
							t.Fatal("pending membership expanded", got)
						}
					} else if rr.Obligation != cp.True || got.Baseline != cp.False {
						t.Fatal(got)
					}
					after := baseline.Record(condition.Snapshot{"approved-reviews-by": base.ReviewAttribute()})
					if !reflect.DeepEqual(before, after) {
						t.Fatal("baseline inputs mutated")
					}
				}
			})
		}
	}
}

func TestRuleLocalPoolsDoNotLeak(t *testing.T) {
	base, bob := approvalFixture(t, false)
	carol := cp.Evaluation{OptedIn: true, Allowed: true, ReviewsKnown: true, ReviewLogins: []string{"carol"}}
	wantBob := expression(t, "approved-reviews-by = bob")
	wantCarol := expression(t, "approved-reviews-by = carol")
	trigger := condition.Not(wantBob)
	first := Rule{When: trigger, Conditions: &wantBob, Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}
	second := Rule{When: trigger, Conditions: &wantBob, Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}
	for _, tc := range []struct {
		rules []Rule
		local []cp.Evaluation
		index int
	}{
		{[]Rule{first, second}, []cp.Evaluation{bob, carol}, 1},
		{[]Rule{second, first}, []cp.Evaluation{carol, bob}, 0},
	} {
		got := EvaluateRequirements(expression(t, "#approved-reviews-by = 1"), nil, base, nil, tc.rules, tc.local)
		if got.Baseline != cp.True || got.Result != cp.False || got.Rules[tc.index].Trigger != cp.True || got.Rules[tc.index].Obligation != cp.False {
			t.Fatal("another rule expanded inputs", got)
		}
	}
	// A conditions-only rule sees the baseline pool even when another rule adds Bob.
	first.Conditions = &wantCarol
	second.Approvals = nil
	got := EvaluateRequirements(expression(t, "#approved-reviews-by = 1"), nil, base, nil, []Rule{first, second}, []cp.Evaluation{carol})
	if got.Rules[0].Obligation != cp.True || got.Rules[1].Obligation != cp.False {
		t.Fatal(got)
	}
}
