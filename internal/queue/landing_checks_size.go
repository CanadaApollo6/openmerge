package queue

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"
)

// MaxLandingChecksBytes bounds the complete compact wire-v1 envelope, including
// JSON escaping. Check this before copying shared inputs or serializing them.
const MaxLandingChecksBytes = 1 << 20

// MaxLandingCheckStringBytes bounds identities and check/provenance fields.
const MaxLandingCheckStringBytes = 4096

// ErrLandingChecksTooLarge identifies any count, string, or total-byte overflow.
var ErrLandingChecksTooLarge = errors.New("landing check evidence exceeds size limits")

// LandingChecksTooLargeError fails a start closed, without stale-work retry or
// truncation. Field identifies the limit that needs operator attention.
type LandingChecksTooLargeError struct {
	Field string
	Limit int
}

func (e LandingChecksTooLargeError) Error() string {
	return fmt.Sprintf("%s: %s limit %d bytes/items; reduce evidence before retrying", ErrLandingChecksTooLarge, e.Field, e.Limit)
}

// Is preserves the overflow classification through error wrappers.
func (e LandingChecksTooLargeError) Is(target error) bool { return target == ErrLandingChecksTooLarge }

const landingChecksEnvelopeBytes = len(`{"version":1,"type":"landing_checks_recorded","data":}`)

func (e LandingChecksRecorded) checkSize() error {
	_, err := landingJSONBytes(reflect.ValueOf(e), MaxLandingChecksBytes-landingChecksEnvelopeBytes)
	return err
}

// landingJSONBytes counts this event's explicit JSON shapes without allocating
// an encoded buffer. It stops at the budget, so even rejected inputs are bounded.
// Keep the encoding/json oracle tests when extending the event's field types.
func landingJSONBytes(v reflect.Value, remaining int) (int, error) {
	n := 0
	add := func(size int) error {
		if size > remaining-n {
			return LandingChecksTooLargeError{"wire event", MaxLandingChecksBytes}
		}
		n += size
		return nil
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		raw, err := v.Interface().(time.Time).MarshalJSON() // at most one timestamp
		if err != nil {
			return 0, err
		}
		return len(raw), add(len(raw))
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return 4, add(4)
		}
		return landingJSONBytes(v.Elem(), remaining)
	case reflect.String:
		if err := add(2); err != nil {
			return 0, err
		}
		s := v.String()
		for i := 0; i < len(s); {
			size, advance := 1, 1
			c := s[i]
			switch {
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
				size = 2
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				size = 6
			case c >= utf8.RuneSelf:
				r, w := utf8.DecodeRuneInString(s[i:])
				advance = w
				size = w
				if r == utf8.RuneError && w == 1 {
					return 0, fmt.Errorf("invalid UTF-8 in landing check evidence")
				}
				if r == '\u2028' || r == '\u2029' {
					size = 6
				}
			}
			if err := add(size); err != nil {
				return 0, err
			}
			i += advance
		}
	case reflect.Int, reflect.Int64:
		if err := add(len(strconv.FormatInt(v.Int(), 10))); err != nil {
			return 0, err
		}
	case reflect.Uint64:
		if err := add(len(strconv.FormatUint(v.Uint(), 10))); err != nil {
			return 0, err
		}
	case reflect.Bool:
		size := 5
		if v.Bool() {
			size = 4
		}
		if err := add(size); err != nil {
			return 0, err
		}
	case reflect.Slice:
		if v.IsNil() {
			return 4, add(4)
		}
		if err := add(2); err != nil {
			return 0, err
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if err := add(1); err != nil {
					return 0, err
				}
			}
			size, err := landingJSONBytes(v.Index(i), remaining-n)
			if err != nil {
				return 0, err
			}
			n += size
		}
	case reflect.Struct:
		if err := add(2); err != nil {
			return 0, err
		}
		for i := 0; i < v.NumField(); i++ {
			if i > 0 {
				if err := add(1); err != nil {
					return 0, err
				}
			}
			if err := add(len(v.Type().Field(i).Tag.Get("json")) + 3); err != nil {
				return 0, err
			}
			size, err := landingJSONBytes(v.Field(i), remaining-n)
			if err != nil {
				return 0, err
			}
			n += size
		}
	default:
		return 0, fmt.Errorf("unsupported landing check JSON type %s", v.Type())
	}
	return n, nil
}
