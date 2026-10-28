package condition

import (
	"errors"
	"fmt"
	"slices"
)

// MaxEvidenceStringBytes bounds one typed input string or list element.
const MaxEvidenceStringBytes = 4096

// MaxEvidenceExpressionBytes bounds the complete displayed expression.
const MaxEvidenceExpressionBytes = 65536

// ErrEvidenceTooLarge identifies evidence that cannot be recorded without loss.
var ErrEvidenceTooLarge = errors.New("condition evidence exceeds size limits")

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

// VisitInputs visits borrowed immutable evaluator inputs without expanding their
// lists. A retaining caller must bound the input before copying any slices.
func (e Expression) VisitInputs(pr PR, visit func(ObservedInput) error) error {
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
		if err := visit(ObservedInput{Attribute: name, Kind: kind, Text: v.text, List: v.list, Flag: v.flag, Checks: v.checks, Observed: v.observed, Unsettled: v.unsettled}); err != nil {
			return err
		}
	}
	return nil
}

// Clone owns the input's lists and represents empty lists explicitly on wire.
func (v ObservedInput) Clone() ObservedInput {
	v.List = append([]string{}, v.List...)
	v.Observed = append([]string{}, v.Observed...)
	v.Unsettled = append([]string{}, v.Unsettled...)
	return v
}

// Equal compares input values, treating borrowed nil lists as empty lists.
func (v ObservedInput) Equal(w ObservedInput) bool {
	return v.Attribute == w.Attribute && v.Kind == w.Kind && v.Text == w.Text && v.Flag == w.Flag && v.Checks == w.Checks && slices.Equal(v.List, w.List) && slices.Equal(v.Observed, w.Observed) && slices.Equal(v.Unsettled, w.Unsettled)
}

// Record captures the same immutable values consumed by Evaluate, without I/O.
func (e Expression) Record(pr PR) Evidence {
	out := Evidence{Expression: e.String(), Inputs: []ObservedInput{}}
	_ = e.VisitInputs(pr, func(v ObservedInput) error { out.Inputs = append(out.Inputs, v.Clone()); return nil })
	return out
}

// Validate rejects shapes the evaluator cannot produce. The expression is the
// stable display form, not a second condition parser or a new selection policy.
func (e Evidence) Validate() error {
	if len(e.Expression) > MaxEvidenceExpressionBytes || len(e.Inputs) > len(attributes) {
		return ErrEvidenceTooLarge
	}
	if e.Expression == "" || e.Inputs == nil {
		return fmt.Errorf("invalid condition evidence")
	}
	for i, v := range e.Inputs {
		if i > 0 && e.Inputs[i-1].Attribute >= v.Attribute || v.List == nil || v.Observed == nil || v.Unsettled == nil {
			return fmt.Errorf("invalid condition input")
		}
		if err := v.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate also accepts borrowed empty lists, before the journal takes ownership.
func (v ObservedInput) Validate() error {
	if len(v.List) > 10000 || len(v.Observed) > 10000 || len(v.Unsettled) > 10000 {
		return ErrEvidenceTooLarge
	}
	for _, s := range []string{v.Attribute, v.Kind, v.Text} {
		if len(s) > MaxEvidenceStringBytes {
			return ErrEvidenceTooLarge
		}
	}
	for _, list := range [][]string{v.List, v.Observed, v.Unsettled} {
		for _, s := range list {
			if len(s) > MaxEvidenceStringBytes {
				return ErrEvidenceTooLarge
			}
		}
	}
	if attributeKind(v.Attribute) == missingValue {
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
	return nil
}

// InputType returns the type retained by the compiled expression, even when a
// runtime value is unknown. Candidate attributes need no public registration.
func (e Expression) InputType(name string) string {
	if e.kind == atom && e.name == name {
		switch e.typ {
		case stringValue:
			return "string"
		case listValue:
			return "list"
		case boolValue:
			return "bool"
		}
	}
	for _, child := range e.children {
		if typ := child.InputType(name); typ != "" {
			return typ
		}
	}
	return ""
}
