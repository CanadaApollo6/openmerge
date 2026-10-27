package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// strictJSON rejects duplicate keys (including nested objects), missing zero
// values, null collections, case aliases and unknown fields. Every new field is
// required; a future shape change needs another kind/version, never an omission.
func strictJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return ErrMalformed
	}
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
