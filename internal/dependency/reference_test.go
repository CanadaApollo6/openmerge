package dependency

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func testScope() Scope {
	return Scope{"github:https://github.com", "123", "omj1_" + strings.Repeat("a", 64)}
}
func testRef(n int, revision uint64) Reference {
	return Reference{testScope(), fmt.Sprintf("chg1_%064x", n), revision}
}
func canonical(t testing.TB, r Reference) string {
	t.Helper()
	s, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCanonicalURIGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/references.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Forge, Installation, Namespace, ChangeID string
		Revision                                 uint64
		URI                                      string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Installation, func(t *testing.T) {
			r := Reference{Scope{tc.Forge, tc.Installation, tc.Namespace}, tc.ChangeID, tc.Revision}
			if got := canonical(t, r); got != tc.URI {
				t.Fatalf("got %s; want %s", got, tc.URI)
			}
			parsed, err := ParseReference(tc.URI, r.Scope)
			if err != nil || parsed != r {
				t.Fatalf("round trip: %#v %v", parsed, err)
			}
		})
	}
}

func TestCanonicalURIRejectsMalformedAndForeign(t *testing.T) {
	raw := canonical(t, testRef(1, 0))
	for _, bad := range []string{
		"#42", "github:acme/api#42", raw + "/extra", raw + "?x=1", raw + "#fragment", raw + "@0", raw + "@01", raw + "@+1", raw + "@-1", raw + "@18446744073709551616", raw + "@1@2",
		strings.Replace(raw, "%3A", "%3a", 1), strings.Replace(raw, "github%3A", "%67ithub%3A", 1), strings.Replace(raw, "%3A", "%XX", 1), strings.Replace(raw, "/123/", "/%FF/", 1), strings.Replace(raw, "/123/", "/a+b/", 1), strings.Replace(raw, "/123/", "/a%00b/", 1), strings.Replace(raw, "chg1_", "chg2_", 1), strings.Replace(raw, "omj1_", "omj2_", 1),
		strings.Replace(raw, "github.com", "user%40github.com", 1), strings.Replace(raw, "github.com", "user%3Asecret%40github.com", 1),
		strings.Replace(raw, "github.com", ":443", 1), strings.Replace(raw, "github.com", "github.com:", 1), strings.Replace(raw, "github.com", "github.com:0", 1), strings.Replace(raw, "github.com", "github.com:65536", 1),
	} {
		t.Run(bad, func(t *testing.T) {
			if _, err := ParseReference(bad, testScope()); err == nil {
				t.Fatal("accepted malformed URI")
			}
		})
	}
	for _, mutate := range []func(*Scope){func(s *Scope) { s.Installation = "456" }, func(s *Scope) { s.JournalNamespace = "omj1_" + strings.Repeat("b", 64) }, func(s *Scope) { s.ForgeInstance = "github:https://other.example:8443" }} {
		foreign := testRef(1, 0)
		mutate(&foreign.Scope)
		if _, err := ParseReference(canonical(t, foreign), testScope()); !errors.Is(err, ErrOutOfScope) {
			t.Fatalf("foreign scope: %v", err)
		}
	}
}

func registryFixture() Registry {
	r := Registry{Scope: testScope(), Organization: "acme", Repositories: []string{"https://github.com/acme/api", "https://github.com/acme/client"}}
	r.Bindings = []Binding{{testRef(1, 0), QueueKey{r.Repositories[0], "main", ""}, []string{r.Repositories[0] + "/pull/42"}}, {testRef(2, 0), QueueKey{r.Repositories[1], "release", "fast"}, nil}}
	return r
}

func TestRevisionlessAliasesOptionalPinsAndDedupe(t *testing.T) {
	r := registryFixture()
	body := "Depends-On: https://github.com/acme/api/pull/42\nDepends-On: " + canonical(t, testRef(1, 0)) + "\nDepends-On: " + canonical(t, testRef(2, 7)) + "\nDepends-On: " + canonical(t, testRef(2, 7))
	inputs, err := ParseIntent(body, r.Scope)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(inputs, r)
	if err != nil || !reflect.DeepEqual(got, []Reference{testRef(1, 0), testRef(2, 7)}) {
		t.Fatalf("resolved: %#v %v", got, err)
	}
	for _, refs := range [][]Reference{{testRef(1, 0), testRef(1, 7)}, {testRef(1, 7), testRef(1, 8)}} {
		if _, err := Normalize(refs, r.Scope); !errors.Is(err, ErrConflict) {
			t.Fatalf("conflict accepted: %v", err)
		}
	}
	inputs = append(inputs, Input{Reference: testRef(1, 7)})
	if _, err := Resolve(inputs, r); !errors.Is(err, ErrConflict) {
		t.Fatalf("alias/pin conflict: %v", err)
	}
}

func TestResolutionRejectsUnservedAmbiguousAndForeign(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  Input
		change func(*Registry)
		want   error
	}{
		{"unserved alias", Input{Alias: "https://github.com/acme/external/pull/42"}, nil, ErrOutOfScope},
		{"other organization", Input{Alias: "https://github.com/other/api/pull/42"}, nil, ErrOutOfScope},
		{"unknown id", Input{Reference: testRef(9, 0)}, nil, ErrUnresolved},
		{"unserved canonical", Input{Reference: testRef(1, 0)}, func(r *Registry) { r.Repositories = r.Repositories[1:] }, ErrOutOfScope},
		{"ambiguous id", Input{Reference: testRef(1, 0)}, func(r *Registry) { r.Bindings = append(r.Bindings, r.Bindings[0]) }, ErrUnresolved},
		{"ambiguous alias", Input{Alias: "https://github.com/acme/api/pull/42"}, func(r *Registry) { r.Bindings = append(r.Bindings, r.Bindings[0]) }, ErrUnresolved},
		{"foreign binding", Input{Alias: "https://github.com/acme/api/pull/42"}, func(r *Registry) { r.Bindings[0].Reference.Scope.Installation = "456" }, ErrOutOfScope},
		{"wrong queue binding", Input{Alias: "https://github.com/acme/api/pull/42"}, func(r *Registry) { r.Bindings[0].Queue.Repository = r.Repositories[1] }, ErrUnresolved},
		{"pinned binding", Input{Reference: testRef(1, 0)}, func(r *Registry) { r.Bindings[0].Reference.Revision = 7 }, ErrUnresolved},
		{"missing base", Input{Reference: testRef(1, 0)}, func(r *Registry) { r.Bindings[0].Queue.Base = "" }, ErrUnresolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := registryFixture()
			if tc.change != nil {
				tc.change(&r)
			}
			if _, err := Resolve([]Input{tc.input}, r); !errors.Is(err, tc.want) {
				t.Fatalf("got %v; want %v", err, tc.want)
			}
		})
	}
	foreign := testRef(1, 0)
	foreign.Scope.JournalNamespace = "omj1_" + strings.Repeat("b", 64)
	// Empty bindings establish that foreign scope wins before any ID lookup.
	if _, err := Resolve([]Input{{Reference: foreign}}, Registry{Scope: testScope()}); !errors.Is(err, ErrOutOfScope) {
		t.Fatal(err)
	}
	for _, alias := range []string{"http://github.com/acme/api/pull/42", "https://u:p@github.com/acme/api/pull/42", "https://github.com/acme/api/pull/042", "https://github.com/acme/api/pull/42@7", "https://github.com/acme/api/pull/42?x", "https://github.com/acme/api/pull/42#x", "https://github.com/acme/api/pull/42/", "https://github.com/acme/api/pull/%34%32", "https://elsewhere/acme/api/pull/42"} {
		if _, err := ParseIntent("Depends-On: "+alias, testScope()); err == nil {
			t.Fatalf("accepted %s", alias)
		}
	}
}

func FuzzParseIntent(f *testing.F) {
	r := testRef(1, 7)
	raw, err := r.Canonical()
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", "Depends-On: " + raw, "```\nDepends-On: invalid\n```", "<!--\r\nDepends-On: bad\r\n-->", "Depends-On: https://github.com/acme/api/pull/42"} {
		f.Add(seed)
	}
	for _, tc := range intentCorpus(f) {
		f.Add(tc.Body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		assertIntentProseLines(t, body)
		inputs, err := ParseIntent(body, testScope())
		if err != nil {
			return
		}
		for _, input := range inputs {
			if input.Alias != "" {
				if _, err := parseAlias(input.Alias, testScope()); err != nil {
					t.Fatal(err)
				}
				continue
			}
			raw := canonical(t, input.Reference)
			got, err := ParseReference(raw, testScope())
			if err != nil || got != input.Reference {
				t.Fatalf("roundtrip: %v", err)
			}
		}
	})
}

func TestResolveInputCapBeforeBindingWork(t *testing.T) {
	registry := registryFixture()
	inputs := make([]Input, MaxInputs)
	for i := range inputs {
		inputs[i] = Input{Reference: testRef(1, 0)}
	}
	if refs, err := Resolve(inputs, registry); err != nil || len(refs) != 1 {
		t.Fatalf("at cap: %v %v", refs, err)
	}
	// Invalid inputs would fail as malformed if even the first were resolved.
	// ErrLimit proves the raw-count check precedes all per-input/binding work.
	for _, count := range []int{MaxInputs + 1, 20000} {
		if refs, err := Resolve(make([]Input, count), registry); !errors.Is(err, ErrLimit) || refs != nil {
			t.Fatalf("count %d: %v %v", count, refs, err)
		}
	}
}

func TestOriginsRejectAlternateCanonicalSpellings(t *testing.T) {
	for _, origin := range []string{"https://github.com:443", "https://github.com.", "https://github.com.:8443"} {
		t.Run(origin, func(t *testing.T) {
			scope := testScope()
			scope.ForgeInstance = "github:" + origin
			if err := scope.Validate(); !errors.Is(err, ErrMalformed) {
				t.Fatalf("scope: %v", err)
			}
			if _, err := repositoryURL(origin + "/acme/api"); !errors.Is(err, ErrMalformed) {
				t.Fatalf("repository: %v", err)
			}
			raw := strings.Replace(canonical(t, testRef(1, 0)), EscapeSegment(testScope().ForgeInstance), EscapeSegment(scope.ForgeInstance), 1)
			if _, err := ParseReference(raw, testScope()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("reference: %v", err)
			}
		})
	}
}

func FuzzParseReference(f *testing.F) {
	for _, seed := range []string{"", canonical(f, testRef(1, 0)), canonical(f, testRef(2, 7)), canonical(f, testRef(3, 0)) + "@01"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		ref, err := ParseReference(raw, testScope())
		if err != nil {
			return
		}
		if got := canonical(t, ref); got != raw {
			t.Fatalf("accepted %q != canonical %q", raw, got)
		}
	})
}
