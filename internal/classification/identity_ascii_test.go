package classification

import "testing"

func TestCodeownersRejectsUnicodeIdentities(t *testing.T) {
	for _, name := range []string{"ſam", "Kam", "Ａam", "ıam", "İam"} {
		for _, token := range []string{"@" + name, "@" + name + "/team", "@org/" + name} {
			parsed := parseOwners("* " + token + "\n")
			if len(parsed.tokens) != 0 || len(parsed.diagnostics) == 0 {
				t.Fatalf("accepted %q: %+v", token, parsed)
			}
		}
	}
	for _, token := range []string{"@Sam", "@Org/Team_Name"} {
		if _, err := canonicalOwner(token); err != nil {
			t.Fatal(err)
		}
	}
}
