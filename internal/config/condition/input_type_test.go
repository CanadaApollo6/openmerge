package condition

import "testing"

func TestCompiledInputTypeSurvivesUnknownRuntimeValue(t *testing.T) {
	for _, tc := range []struct{ text, name, typ string }{
		{"title = allowed", "title", "string"}, {"#label > 0", "label", "list"}, {"draft", "draft", "bool"},
	} {
		e, err := Parse(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		nested := All(e)
		if nested.Record(Snapshot{}).Inputs[0].Kind != "unknown" || nested.InputType(tc.name) != tc.typ {
			t.Fatal("unknown input lost its compiled type", tc)
		}
	}
	e, err := ParseCandidate("provenance = human", Snapshot{"provenance": String("")})
	if err != nil {
		t.Fatal(err)
	}
	if e.InputType("provenance") != "string" || e.InputType("other") != "" {
		t.Fatal("candidate input type required public registration")
	}
}
