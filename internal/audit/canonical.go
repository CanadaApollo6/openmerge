// Package audit implements offline, read-only source exports. It has no runtime wiring.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxBytes bounds each raw/expanded v1 payload and metadata record, not the export.
const MaxBytes = 4 << 20

// MaxDepth bounds nesting in a v1 record.
const MaxDepth = 64

var errCanonical = errors.New("audit: invalid or excessive canonical JSON")

// Canonical produces serialization v1 without converting numbers through float64.
// Duplicate keys and malformed UTF-8/UTF-16 are rejected rather than repaired.
func Canonical(raw []byte) ([]byte, error) {
	if len(raw) > MaxBytes || !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, errCanonical
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out bytes.Buffer
	if err := canonicalValue(d, &out, 0, new(int)); err != nil {
		return nil, errCanonical
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errCanonical
	}
	if out.Len() > MaxBytes {
		return nil, errCanonical
	}
	return out.Bytes(), nil
}

func validEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		i++
		for ; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				return false
			}
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return false
			}
			n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if e != nil {
				return false
			}
			i += 4
			if n >= 0xdc00 && n <= 0xdfff {
				return false
			}
			if n >= 0xd800 && n <= 0xdbff {
				if i+6 >= len(raw) || string(raw[i+1:i+3]) != "\\u" {
					return false
				}
				lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if e != nil || lo < 0xdc00 || lo > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

func canonicalValue(d *json.Decoder, out *bytes.Buffer, depth int, expanded *int) error {
	if depth > MaxDepth || out.Len() > MaxBytes {
		return errCanonical
	}
	start := out.Len()
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			values := map[string][]byte{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok {
					return errCanonical
				}
				if _, ok = values[key]; ok {
					return errCanonical
				}
				var b bytes.Buffer
				if e = canonicalValue(d, &b, depth+1, expanded); e != nil {
					return e
				}
				*expanded += len(key) + 4
				if *expanded > MaxBytes {
					return errCanonical
				}
				values[key] = b.Bytes()
			}
			if t, e := d.Token(); e != nil || t != json.Delim('}') {
				return errCanonical
			}
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				writeString(out, key)
				out.WriteByte(':')
				out.Write(values[key])
				if out.Len() > MaxBytes {
					return errCanonical
				}
			}
			out.WriteByte('}')
		case '[':
			out.WriteByte('[')
			i := 0
			for d.More() {
				if i > 0 {
					out.WriteByte(',')
				}
				if e := canonicalValue(d, out, depth+1, expanded); e != nil {
					return e
				}
				i++
			}
			if t, e := d.Token(); e != nil || t != json.Delim(']') {
				return errCanonical
			}
			out.WriteByte(']')
		default:
			return errCanonical
		}
	case string:
		writeString(out, v)
	case json.Number:
		n, e := decimal(string(v))
		if e != nil {
			return e
		}
		out.WriteString(n)
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case nil:
		out.WriteString("null")
	default:
		return errCanonical
	}
	if _, container := t.(json.Delim); !container {
		*expanded += out.Len() - start
	}
	if out.Len() > MaxBytes || *expanded > MaxBytes {
		return errCanonical
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 32 {
				const hex = "0123456789abcdef"
				out.WriteString(`\u00`)
				out.WriteByte(hex[r>>4])
				out.WriteByte(hex[r&15])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

func decimal(s string) (string, error) {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exp := int64(0)
	exponent := ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exponent = s[i+1:]
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exp -= int64(len(s) - i - 1)
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	if exponent != "" {
		shift, err := strconv.ParseInt(exponent, 10, 32)
		if err != nil {
			return "", errCanonical
		}
		exp += shift
	}
	for strings.HasSuffix(s, "0") {
		s = s[:len(s)-1]
		exp++
	}
	point := int64(len(s)) + exp
	if point > MaxBytes || point < -MaxBytes || int64(len(s))+abs(exp)+3 > MaxBytes {
		return "", errCanonical
	}
	switch {
	case point <= 0:
		s = "0." + strings.Repeat("0", int(-point)) + s
	case point >= int64(len(s)):
		s += strings.Repeat("0", int(point)-len(s))
	default:
		s = s[:point] + "." + s[point:]
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
