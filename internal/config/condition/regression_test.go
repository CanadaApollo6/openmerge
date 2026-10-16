package condition

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) Expression {
	t.Helper()
	expression, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return expression
}

func TestThreeValuedLogic(t *testing.T) {
	// Rows and columns are met, not met, unknown. Literal truth tables pin
	// dominance in both operand orders, including when the group is negated.
	const yes, no, unsure = "met", "not met", "unknown"
	values := []Value{Bool(true), Bool(false), {}}
	tables := []struct {
		name  string
		group func(...Expression) Expression
		want  [3][3]string
	}{
		{"and", All, [3][3]string{{yes, no, unsure}, {no, no, no}, {unsure, no, unsure}}},
		{"or", Any, [3][3]string{{yes, yes, yes}, {yes, no, unsure}, {yes, unsure, unsure}}},
	}
	for _, table := range tables {
		for i, left := range values {
			for j, right := range values {
				t.Run(fmt.Sprintf("%s/%d/%d", table.name, i, j), func(t *testing.T) {
					group := table.group(mustParse(t, "draft"), mustParse(t, "conflict"))
					pr := Snapshot{"draft": left, "conflict": right}
					for _, test := range []struct {
						expression Expression
						want       bool
					}{
						{group, table.want[i][j] == yes},
						{Not(group), table.want[i][j] == no},
						{Not(Not(group)), table.want[i][j] == yes},
					} {
						ok, failures := test.expression.Evaluate(pr)
						if ok != test.want || (ok && failures != nil) || (!ok && len(failures) == 0) {
							t.Fatalf("%s = %v, %+v; want passing %v", test.expression, ok, failures, test.want)
						}
						if table.want[i][j] == unsure {
							for _, failure := range failures {
								if !failure.Unknown {
									t.Errorf("unknown group reported as known: %+v", failure)
								}
							}
						}
					}
				})
			}
		}
	}
}

func TestUnknownUnderNegation(t *testing.T) {
	for _, text := range []string{"conflict", "#approved-reviews-by < 1", "base != main", "label = ready"} {
		atom := mustParse(t, text)
		for _, pr := range []PR{nil, Snapshot{}, Snapshot{
			"conflict": String("false"), "approved-reviews-by": Bool(false),
			"base": Strings("main"), "label": String("ready"),
		}} {
			for _, expression := range []Expression{atom, mustParse(t, "-"+text), Not(atom), Not(Not(atom)), All(Not(atom))} {
				ok, failures := expression.Evaluate(pr)
				if ok || len(failures) != 1 || !failures[0].Unknown {
					t.Errorf("%s on %v = %v, %+v; want unknown", expression, pr, ok, failures)
					continue
				}
				failure := failures[0]
				for len(failure.Children) > 0 {
					failure = failure.Children[0]
				}
				if failure.Attribute != atom.name || !failure.Unknown || failure.Missing != (pr == nil || reflect.DeepEqual(pr, Snapshot{})) {
					t.Errorf("unknown evidence missing attribute/type distinction: %+v", failure)
				}
			}
		}
	}
}

func TestStructuredFailures(t *testing.T) {
	tests := []struct {
		text string
		pr   Snapshot
		want Failure
	}{
		{"label = ready", Snapshot{"label": Strings("blocked", "a; b")}, Failure{Condition: `label = "ready"`, Attribute: "label", Actual: []string{"blocked", "a; b"}}},
		{"#approved-reviews-by >= 2", Snapshot{"approved-reviews-by": Strings("alice")}, Failure{Condition: "#approved-reviews-by >= 2", Attribute: "approved-reviews-by", Actual: 1}},
		{"draft", Snapshot{"draft": Bool(false)}, Failure{Condition: "draft", Attribute: "draft", Actual: false}},
		{"title = wanted", Snapshot{"title": String("")}, Failure{Condition: `title = "wanted"`, Attribute: "title", Actual: ""}},
		{"label = ready", Snapshot{}, Failure{Condition: `label = "ready"`, Attribute: "label", Missing: true, Unknown: true}},
		{"label = ready", Snapshot{"label": String("ready")}, Failure{Condition: `label = "ready"`, Attribute: "label", Actual: "ready", Unknown: true}},
		{"#label >= 1", Snapshot{"label": Bool(false)}, Failure{Condition: "#label >= 1", Attribute: "label", Actual: false, Unknown: true}},
		{"label = ready", Snapshot{"label": Strings()}, Failure{Condition: `label = "ready"`, Attribute: "label", Actual: []string{}}},
	}
	for _, tt := range tests {
		t.Run(tt.text+fmt.Sprint(tt.pr), func(t *testing.T) {
			ok, failures := mustParse(t, tt.text).Evaluate(tt.pr)
			if ok || !reflect.DeepEqual(failures, []Failure{tt.want}) {
				t.Fatalf("Evaluate = %v, %+v; want %+v", ok, failures, tt.want)
			}
		})
	}
}

func TestGroupFailureEvidenceAndJSON(t *testing.T) {
	expression := Any(mustParse(t, "title = a; b"), mustParse(t, "draft"))
	_, failures := expression.Evaluate(Snapshot{"title": String("c")})
	want := Failure{Condition: `or (title = "a; b"; draft)`, Unknown: true, Children: []Failure{
		{Condition: `title = "a; b"`, Attribute: "title", Actual: "c"},
		{Condition: "draft", Attribute: "draft", Missing: true, Unknown: true},
	}}
	if !reflect.DeepEqual(failures, []Failure{want}) || failures[0].String() != expression.String() {
		t.Fatalf("group evidence = %+v, want %+v", failures, want)
	}
	data, err := json.Marshal(failures[0])
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"condition":"or (title = \"a; b\"; draft)","actual":null,"missing":false,"unknown":true,"children":[{"condition":"title = \"a; b\"","attribute":"title","actual":"c","missing":false,"unknown":false},{"condition":"draft","attribute":"draft","actual":null,"missing":true,"unknown":true}]}`
	if string(data) != wantJSON {
		t.Errorf("JSON = %s, want %s", data, wantJSON)
	}
	_, failures = Not(All(mustParse(t, "title = a; b"), mustParse(t, "draft"))).Evaluate(Snapshot{"title": String("a; b"), "draft": Bool(true)})
	want = Failure{Condition: `not (and (title = "a; b"; draft))`, Children: []Failure{
		{Condition: `and (title = "a; b"; draft)`, Children: []Failure{
			{Condition: `title = "a; b"`, Attribute: "title", Actual: "a; b"},
			{Condition: "draft", Attribute: "draft", Actual: true},
		}},
	}}
	if !reflect.DeepEqual(failures, []Failure{want}) {
		t.Errorf("failed NOT lost met operand evidence: %+v", failures)
	}
}

func TestQuotedConditionValues(t *testing.T) {
	for _, value := range []string{"a; b", "x) ; draft", "a\"b", "a\nb", `a\b`} {
		expression := Any(mustParse(t, "title = "+value), mustParse(t, "draft"))
		if want := fmt.Sprintf("or (title = %q; draft)", value); expression.String() != want {
			t.Errorf("String = %q, want %q", expression.String(), want)
		}
	}
}

func TestReferencedAttributes(t *testing.T) {
	expression := All(mustParse(t, "draft"), Not(mustParse(t, "#label < 1")), Any(
		mustParse(t, "label = ready"), mustParse(t, "conflict"), mustParse(t, "title = fix")))
	want := []string{"conflict", "draft", "label", "title"}
	if got := expression.Attributes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Attributes = %v, want %v", got, want)
	}
	copyOfNames := expression.Attributes()
	copyOfNames[0] = "changed"
	if got := expression.Attributes(); !reflect.DeepEqual(got, want) {
		t.Errorf("Attributes exposed mutable data: %v", got)
	}
	if got := All().Attributes(); len(got) != 0 {
		t.Errorf("empty conditions reference %v", got)
	}
}

func TestFailureListsAreCopies(t *testing.T) {
	pr := Snapshot{"label": Strings("blocked")}
	expression := mustParse(t, "label = ready")
	_, failures := expression.Evaluate(pr)
	failures[0].Actual.([]string)[0] = "ready"
	_, failures = expression.Evaluate(pr)
	if !reflect.DeepEqual(failures[0].Actual, []string{"blocked"}) {
		t.Fatal("failure exposed snapshot list")
	}
}

func TestFullTokensAndBoundedSuggestions(t *testing.T) {
	tests := []struct {
		text, want string
	}{
		{"check_success = ci", `unknown attribute "check_success"; did you mean "check-success"?`},
		{"Draft", `unknown attribute "Draft"; did you mean "draft"?`},
		{"completely_unrelated = x", `unknown attribute "completely_unrelated"`},
		{"title contains x", `unknown operator "contains"; valid operators: =, !=, ~=, glob, <, <=, >, >=`},
		{"title Contains_value x", `unknown operator "Contains_value"; valid operators: =, !=, ~=, glob, <, <=, >, >=`},
	}
	for _, tt := range tests {
		_, err := Parse(tt.text)
		if err == nil || err.Error() != tt.want {
			t.Errorf("Parse(%q) = %v, want %q", tt.text, err, tt.want)
		}
	}
	for _, word := range []string{"priorities", "contains", "unrelated"} {
		if got := Suggestion(word, []string{"version", "queues", "="}); strings.Contains(got, "did you mean") {
			t.Errorf("misleading suggestion for %q: %s", word, got)
		}
	}
}
