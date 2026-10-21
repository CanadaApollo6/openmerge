package condition

import "fmt"

// ObservedInput preserves a typed evaluator input, including the knowledge that
// makes missing and pending checks unknown under negation. Empty values are
// distinct from unknown values. Fields are explicit for the journal wire codec.
type ObservedInput struct {
	Attribute string   `json:"attribute"`
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	List      []string `json:"list"`
	Flag      bool     `json:"flag"`
	Checks    bool     `json:"checks"`
	Observed  []string `json:"observed"`
	Unsettled []string `json:"unsettled"`
}

// Evidence retains the complete expression, including nested and negative terms,
// and every referenced input even when another operand decides the result.
type Evidence struct {
	Expression string          `json:"expression"`
	Inputs     []ObservedInput `json:"inputs"`
}

// Record captures the same immutable values consumed by Evaluate, without I/O.
func (e Expression) Record(pr PR) Evidence {
	out := Evidence{Expression: e.String(), Inputs: []ObservedInput{}}
	for _, name := range e.Attributes() {
		v := pr.Attribute(name)
		kind := "unknown"
		switch v.kind {
		case stringValue:
			kind = "string"
		case listValue:
			kind = "list"
		case boolValue:
			kind = "bool"
		}
		out.Inputs = append(out.Inputs, ObservedInput{Attribute: name, Kind: kind, Text: v.text, List: append([]string{}, v.list...), Flag: v.flag, Checks: v.checks, Observed: append([]string{}, v.observed...), Unsettled: append([]string{}, v.unsettled...)})
	}
	return out
}

// Validate rejects shapes the evaluator cannot produce. The expression is the
// stable display form, not a second condition parser or a new selection policy.
func (e Evidence) Validate() error {
	if e.Expression == "" || len(e.Expression) > 65536 || e.Inputs == nil || len(e.Inputs) > len(attributes) {
		return fmt.Errorf("invalid condition evidence")
	}
	for i, v := range e.Inputs {
		if attributeKind(v.Attribute) == missingValue || i > 0 && e.Inputs[i-1].Attribute >= v.Attribute || v.List == nil || v.Observed == nil || v.Unsettled == nil || len(v.List) > 10000 || len(v.Observed) > 10000 || len(v.Unsettled) > 10000 {
			return fmt.Errorf("invalid condition input")
		}
		switch v.Kind {
		case "unknown":
			if v.Text != "" || len(v.List) > 0 || v.Flag || v.Checks {
				return fmt.Errorf("invalid unknown input")
			}
		case "string":
			if attributeKind(v.Attribute) != stringValue || len(v.List) > 0 || v.Flag || v.Checks {
				return fmt.Errorf("invalid string input")
			}
		case "list":
			if attributeKind(v.Attribute) != listValue || v.Text != "" || v.Flag {
				return fmt.Errorf("invalid list input")
			}
		case "bool":
			if attributeKind(v.Attribute) != boolValue || v.Text != "" || len(v.List) > 0 || v.Checks {
				return fmt.Errorf("invalid bool input")
			}
		default:
			return fmt.Errorf("invalid input kind")
		}
		if !v.Checks && (len(v.Observed) > 0 || len(v.Unsettled) > 0) {
			return fmt.Errorf("unexpected check knowledge")
		}
	}
	return nil
}
