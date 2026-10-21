package store

import (
	"strings"
	"testing"
)

func TestDatabaseIdentityUsesConfiguredConnection(t *testing.T) {
	raw := databaseURL(t)
	s := openTestStore(t, raw)
	want, err := DatabaseIdentity(raw)
	must(t, err)
	if got := s.DatabaseIdentity(); got != want || !strings.Contains(got, "search_path=om_test_") || strings.Contains(got, "openmerge@") {
		t.Fatalf("configured identity=%q want=%q", got, want)
	}
}

func TestDatabaseIdentityFormat(t *testing.T) {
	for _, raw := range []string{
		"postgres://operator:secret@localhost:54329/sandbox?search_path=isolated",
		"host=localhost port=54329 dbname=sandbox user=other password=different search_path=isolated",
	} {
		got, err := DatabaseIdentity(raw)
		must(t, err)
		if got != "localhost:54329/sandbox?search_path=isolated" {
			t.Fatal(got)
		}
	}
	if _, err := DatabaseIdentity("postgres://host:invalid/db"); err == nil {
		t.Fatal("invalid DSN accepted")
	}
}
