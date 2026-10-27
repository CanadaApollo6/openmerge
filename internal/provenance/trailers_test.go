package provenance

import (
	"reflect"
	"strings"
	"testing"
)

func TestTrailerCaseFoldingDuplicatesAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		values        []string
		outcomes      []Outcome
	}{
		{"case duplicate", "subject\n\nEntire-Checkpoint: abcdef012345\neNTIRE-cHECKPOINT: abcdef012345", []string{"abcdef012345"}, []Outcome{NotRequested}},
		{"distinct", "subject\n\nAgent-Session: a:one\nAgent-Session: b:two", []string{"a:one", "b:two"}, []Outcome{NotRequested, NotRequested}},
		{"folded neutral", "subject\n\nAgent-Session: a:\n\tone", []string{"a: one"}, []Outcome{InvalidValue}},
		{"folded checkpoint", "subject\n\nEntire-Checkpoint: abcdef\n  012345", []string{"abcdef 012345"}, []Outcome{InvalidValue}},
		{"CRLF whitespace", "subject\r\n\r\nAGENT-SESSION \t: a:one \t\r\n\r\n", []string{"a:one"}, []Outcome{NotRequested}},
		{"empty", "subject\n\nEntire-Checkpoint:", []string{""}, []Outcome{InvalidValue}},
		{"upper hex", "subject\n\nEntire-Checkpoint: ABCDEF012345", []string{"ABCDEF012345"}, []Outcome{InvalidValue}},
		{"prose example", "subject\n\nExample: this is prose\n\n    Agent-Session: a:one", nil, nil},
		{"nonfinal example", "subject\n\nAgent-Session: a:one\n\nMore explanation", nil, nil},
		{"subject only", "Agent-Session: a:one", nil, nil},
		{"prose example mixed with footer", "subject\n\nprose\nAgent-Session: a:one\nprose\nprose", nil, nil},
		{"signed-off quarter", "subject\n\nprose\nSigned-off-by: A <a@example.com>\nAgent-Session: a:one\nprose\nprose\nprose\nprose\nprose", []string{"a:one"}, []Outcome{NotRequested}},
		{"folded empty checkpoint", "subject\n\nEntire-Checkpoint:\n abcdef012345", []string{"abcdef012345"}, []Outcome{NotRequested}},
		{"fixed comment prefix", "subject\n\nAgent-Session: a:one\n\n# comment", []string{"a:one"}, []Outcome{NotRequested}},
		{"less than quarter", "subject\n\nprose\nprose\nAgent-Session: a:one\nprose\nprose", nil, nil},
		{"patch divider", "subject\n\nAgent-Session: a:one\n---\ndiff", []string{"a:one"}, []Outcome{NotRequested}},
		{"body divider", "subject\n\nbody\n---\n\nAgent-Session: a:one", nil, nil},
		{"invalid source", "subject\n\nAgent-Session: Source:one", []string{"Source:one"}, []Outcome{InvalidValue}},
		{"invalid session", "subject\n\nAgent-Session: source:https://x", []string{"source:https://x"}, []Outcome{InvalidValue}},
		{"empty session", "subject\n\nAgent-Session: source:", []string{"source:"}, []Outcome{InvalidValue}},
		{"non ASCII", "subject\n\nAgent-Session: a:é", []string{"a:é"}, []Outcome{InvalidValue}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := ParseTrailers(tc.message)
			var values []string
			var outcomes []Outcome
			for _, d := range ds {
				values = append(values, d.Value)
				outcomes = append(outcomes, d.Outcome)
				if d.Class != Agent {
					t.Fatal("lost agent declaration")
				}
			}
			if !reflect.DeepEqual(values, tc.values) || !reflect.DeepEqual(outcomes, tc.outcomes) {
				t.Fatalf("got %#v", ds)
			}
		})
	}
}

func TestEntireIDStrictFormats(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"abcdef012345", true}, {"000000000000", true}, {"ABCDEF012345", false}, {"abcdef01234g", false},
		{"01ARZ3NDEKTSV4RRFFQ69G5FAV", true}, {"7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"8ZZZZZZZZZZZZZZZZZZZZZZZZZ", false}, {"01arz3ndektsv4rrffq69g5fav", false},
		{"01ARZ3NDEKTSV4RRFFQ69G5FAI", false}, {"01ARZ3NDEKTSV4RRFFQ69G5FAL", false},
		{"01ARZ3NDEKTSV4RRFFQ69G5FAO", false}, {"01ARZ3NDEKTSV4RRFFQ69G5FAU", false},
		{"01ARZ3NDEKTSV4RRFFQ69G5FA", false}, {"001ARZ3NDEKTSV4RRFFQ69G5FAV", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			if ValidEntireID(tc.value) != tc.valid {
				t.Fatal("ID validation mismatch")
			}
		})
	}
}

func TestTrailerValueBounds(t *testing.T) {
	valid := strings.Repeat("a", 32) + ":" + strings.Repeat("z", 128)
	for _, value := range []string{valid, valid + "z", strings.Repeat("x", 4096)} {
		ds := ParseTrailers("subject\n\nAgent-Session: " + value)
		if len(ds) != 1 {
			t.Fatal(ds)
		}
		d := ds[0]
		if len(value) <= maxDeclarationValue {
			if d.Value != value || d.Outcome != NotRequested {
				t.Fatal(d)
			}
		} else if d.Value != "" || d.ValueDigest != valueDigest(value) || d.Outcome != ValueTooLong {
			t.Fatal(d)
		}
	}
}

func FuzzParseTrailers(f *testing.F) {
	for _, s := range []string{"subject\n\nAgent-Session: a:one", "x\n\nEntire-Checkpoint: abcdef012345", "x\n\nEntire-Checkpoint:\n x", "x\r\n\r\nCo-Authored-By: bot <bot@example.com>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, b := ParseTrailers(s), ParseTrailers(s)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("nondeterministic")
		}
		for _, d := range a {
			if len(d.Value) > maxDeclarationValue || d.Class != Agent {
				t.Fatal(d)
			}
		}
		if !reflect.DeepEqual(a, canonicalDeclarations(append(a, a...))) {
			t.Fatal("not a set")
		}
	})
}
