package condition

import (
	"reflect"
	"testing"
)

func TestRecordConditionInputs(t *testing.T) {
	e := All(Any(mustParse(t, "check-success = linux"), Not(mustParse(t, "check-failure = fallback"))), mustParse(t, "-draft"), mustParse(t, "#check-success >= 1"))
	values := CheckResults([]string{"linux"}, []string{"fallback"}, []string{"pending"})
	values["draft"] = Bool(false)
	before, failures := e.Evaluate(values)
	recorded := e.Record(values)
	if recorded.Validate() != nil || recorded.Expression != e.String() || len(recorded.Inputs) != 3 || recorded.Inputs[0].Attribute != "check-failure" || !reflect.DeepEqual(recorded.Inputs[0].Unsettled, []string{"pending"}) {
		t.Fatal("condition structure/knowledge lost", recorded)
	}
	values["draft"] = Bool(true)
	values["check-failure"] = Strings("changed")
	if recorded.Inputs[2].Flag || !reflect.DeepEqual(recorded.Inputs[0].List, []string{"fallback"}) {
		t.Fatal("evidence shares evaluator values")
	}
	original := CheckResults([]string{"linux"}, []string{"fallback"}, []string{"pending"})
	original["draft"] = Bool(false)
	after, gotFailures := e.Evaluate(original)
	if before != after || !reflect.DeepEqual(failures, gotFailures) {
		t.Fatal("recording changed evaluation")
	}
	unknown := mustParse(t, "conflict").Record(Snapshot{})
	if unknown.Inputs[0].Kind != "unknown" || unknown.Validate() != nil {
		t.Fatal("unknown input fabricated")
	}
}
