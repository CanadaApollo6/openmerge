package dependency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

const maxEventBytes = 1 << 20
const maxSnapshotBytes = 64 << 20

type envelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}

// EncodeEvent only knows new wire-v1 kinds. It neither calls nor changes the
// deployed queue codec. First emission remains a reader compatibility boundary.
func EncodeEvent(event Event) (string, []byte, error) {
	if event == nil {
		return "", nil, ErrMalformed
	}
	if err := event.Validate(); err != nil {
		return "", nil, err
	}
	var kind string
	switch event.(type) {
	case JournalInitialized:
		kind = "dependency_journal_initialized"
	case DependenciesDeclared:
		kind = "change_dependencies_declared"
	case PolicyApplied:
		kind = "dependency_policy_applied"
	case Acknowledged:
		kind = "dependency_declaration_acknowledged"
	case SelfAcknowledged:
		kind = "dependency_declaration_self_acknowledged"
	case Observed:
		kind = "dependency_observed"
	case CompositionRecorded:
		kind = "dependency_composition_recorded"
	case DecisionRecorded:
		kind = "dependency_decision_recorded"
	default:
		return "", nil, ErrMalformed
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", nil, err
	}
	// Apply, Encode and Decode enforce identical presence/null rules.
	if err := requireShape(data, reflect.TypeOf(event)); err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(envelope{WireVersion, kind, data})
	if len(raw) > maxEventBytes {
		return "", nil, ErrLimit
	}
	return kind, raw, err
}
func decodeEventData[T Event](raw []byte) (Event, error) {
	var e T
	if err := strictJSON(raw, &e); err != nil {
		return nil, err
	}
	return e, e.Validate()
}

// DecodeEvent strictly reads only the new dependency kinds, with no queue fallback.
func DecodeEvent(raw []byte) (Event, error) {
	if len(raw) > maxEventBytes {
		return nil, ErrLimit
	}
	var env envelope
	if err := strictJSON(raw, &env); err != nil {
		return nil, err
	}
	if env.Version != WireVersion {
		return nil, fmt.Errorf("unsupported dependency wire version %d", env.Version)
	}
	switch env.Type {
	case "dependency_journal_initialized":
		return decodeEventData[JournalInitialized](env.Data)
	case "change_dependencies_declared":
		return decodeEventData[DependenciesDeclared](env.Data)
	case "dependency_policy_applied":
		return decodeEventData[PolicyApplied](env.Data)
	case "dependency_declaration_acknowledged":
		return decodeEventData[Acknowledged](env.Data)
	case "dependency_declaration_self_acknowledged":
		return decodeEventData[SelfAcknowledged](env.Data)
	case "dependency_observed":
		return decodeEventData[Observed](env.Data)
	case "dependency_composition_recorded":
		return decodeEventData[CompositionRecorded](env.Data)
	case "dependency_decision_recorded":
		return decodeEventData[DecisionRecorded](env.Data)
	default:
		return nil, fmt.Errorf("unknown dependency event %q", env.Type)
	}
}

// strictJSON rejects duplicate keys (including nested objects), missing zero
// values, null collections, case aliases and unknown fields. Every new field is
// required; a future shape change needs another kind/version, never an omission.
func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueKeys(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	if err := requireShape(raw, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func uniqueKeys(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrLimit
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = true
			if err := uniqueKeys(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueKeys(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrMalformed
	}
	_, err = d.Token()
	return err
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func requireShape(raw []byte, t reflect.Type) error {
	if t == rawMessageType {
		if bytes.Equal(raw, []byte("null")) {
			return ErrMalformed
		}
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return nil
		}
		return requireShape(raw, t.Elem())
	}
	if bytes.Equal(raw, []byte("null")) {
		return fmt.Errorf("null %s", t)
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		names := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			names[name] = true
			value, ok := fields[name]
			if !ok {
				return fmt.Errorf("missing %s.%s", t.Name(), name)
			}
			if err := requireShape(value, f.Type); err != nil {
				return err
			}
		}
		for key := range fields {
			if !names[key] {
				return fmt.Errorf("unknown %s field %q", t.Name(), key)
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := requireShape(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ClockEventKinds selects only eligibility-changing facts for an activated
// clock replay. Historical composition/decision evidence is not an input.
func ClockEventKinds() []string {
	return []string{"dependency_journal_initialized", "change_dependencies_declared", "dependency_policy_applied", "dependency_observed", "dependency_declaration_acknowledged", "dependency_declaration_self_acknowledged"}
}
