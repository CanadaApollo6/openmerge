package journal

import (
	"encoding/json"
	"fmt"
)

type envelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}

// EncodeEvent encodes only new kinds, without calling any journal append API.
func EncodeEvent(e Event) (string, []byte, error) {
	switch e.(type) {
	case Observation, Landing:
	default:
		return "", nil, ErrMalformed
	}
	if err := e.Validate(); err != nil {
		return "", nil, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(envelope{WireVersion, e.eventKind(), data})
	if err != nil {
		return "", nil, err
	}
	// Encoding, decoding and pure Apply use exactly the same required-field rules.
	if _, err := DecodeEvent(raw); err != nil {
		return "", nil, err
	}
	return e.eventKind(), raw, nil
}

// DecodeEvent is a strict decoder composed by journalreader. Unknown kinds are errors;
// no deployed queue event or condition vocabulary is extended here.
func DecodeEvent(raw []byte) (Event, error) {
	if len(raw) > maxEventBytes {
		return nil, ErrLimit
	}
	var env envelope
	if err := strictJSON(raw, &env); err != nil {
		return nil, err
	}
	if env.Version != WireVersion {
		return nil, fmt.Errorf("unsupported provenance wire version %d", env.Version)
	}
	var e Event
	switch env.Type {
	case "change_provenance_observed":
		var v Observation
		if err := strictJSON(env.Data, &v); err != nil {
			return nil, err
		}
		e = v
	case "landing_provenance_recorded":
		var v Landing
		if err := strictJSON(env.Data, &v); err != nil {
			return nil, err
		}
		e = v
	default:
		return nil, fmt.Errorf("unknown provenance event %q", env.Type)
	}
	return e, e.Validate()
}
