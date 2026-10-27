package provenance

import (
	"reflect"
	"strings"
	"testing"
)

func TestTrailerCaseFoldingDuplicatesAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, parsed string
		values       []string
		outcomes     []Outcome
	}{
		{"case duplicate", "Entire-Checkpoint: abcdef012345\neNTIRE-cHECKPOINT: abcdef012345\n", []string{"abcdef012345"}, []Outcome{NotRequested}},
		{"distinct", "Agent-Session: a:one\nAgent-Session: b:two\n", []string{"a:one", "b:two"}, []Outcome{NotRequested, NotRequested}},
		{"unfolded neutral", "Agent-Session: a: one\n", []string{"a: one"}, []Outcome{InvalidValue}},
		{"unfolded checkpoint", "Entire-Checkpoint: abcdef 012345\n", []string{"abcdef 012345"}, []Outcome{InvalidValue}},
		{"Git preserved CR", "Agent-Session: a:\r one\n", []string{"a:\r one"}, []Outcome{InvalidValue}},
		{"Git emitted whitespace is evidence", "Agent-Session:  a:one \t\r\n", []string{" a:one \t\r"}, []Outcome{InvalidValue}},
		{"empty", "Entire-Checkpoint: \n", []string{""}, []Outcome{InvalidValue}},
		{"upper hex", "Entire-Checkpoint: ABCDEF012345\n", []string{"ABCDEF012345"}, []Outcome{InvalidValue}},
		{"valid checkpoint", "Entire-Checkpoint: abcdef012345\n", []string{"abcdef012345"}, []Outcome{NotRequested}},
		{"unrelated keys", "Signed-off-by: A <a@example.com>\nCo-authored-by: B <b@example.com>\n", nil, nil},
		{"no trailers", "", nil, nil},
		{"invalid source", "Agent-Session: Source:one\n", []string{"Source:one"}, []Outcome{InvalidValue}},
		{"invalid session", "Agent-Session: source:https://x\n", []string{"source:https://x"}, []Outcome{InvalidValue}},
		{"empty session", "Agent-Session: source:\n", []string{"source:"}, []Outcome{InvalidValue}},
		{"non ASCII", "Agent-Session: a:é\n", []string{"a:é"}, []Outcome{InvalidValue}},
		{"invalid encoding", "Agent-Session: a:\xff\n", []string{""}, []Outcome{InvalidEncoding}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := ParseGitTrailers(GitTrailerOutput(tc.parsed))
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
		ds := ParseGitTrailers(GitTrailerOutput("Agent-Session: " + value))
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

func FuzzParseGitTrailers(f *testing.F) {
	for _, s := range []string{"Agent-Session: a:one", "Entire-Checkpoint: abcdef012345", "Entire-Checkpoint:\n x", "Co-Authored-By: bot <bot@example.com>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, b := ParseGitTrailers(GitTrailerOutput(s)), ParseGitTrailers(GitTrailerOutput(s))
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
