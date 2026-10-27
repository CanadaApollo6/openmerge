package dependency

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAuthoritativeIntentBoundaries(t *testing.T) {
	body, err := os.ReadFile("testdata/intent.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(body), strings.ReplaceAll(string(body), "\n", "\r\n")} {
		inputs, err := ParseIntent(text, testScope())
		if err != nil {
			t.Fatal(err)
		}
		var aliases []string
		for _, input := range inputs {
			aliases = append(aliases, input.Alias)
		}
		want := []string{"https://github.com/acme/api/pull/1", "https://github.com/acme/api/pull/2", "https://github.com/acme/api/pull/3"}
		if !reflect.DeepEqual(aliases, want) {
			t.Fatalf("got %#v", aliases)
		}
	}
}

func TestMalformedRecognizedIntentFailsClosed(t *testing.T) {
	for _, body := range []string{"Depends-On:", "Depends-On: bad", "Depends-On: #42", "Depends-On: github:acme/api#42", "Depends-On: https://github.com/acme/api/pull/1 extra", "Depends-On: https://github.com/acme/api/pull/1\nDepends-On:"} {
		if got, err := ParseIntent(body, testScope()); err == nil || got != nil {
			t.Fatalf("partial/accepted %q: %#v %v", body, got, err)
		}
	}
}
