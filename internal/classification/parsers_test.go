package classification

import (
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// Pinned from GitHub's CODEOWNERS examples and gitignore/gitattributes(5).
// Keep explicit misses: the two tree dialects differ from CompileGlob.
func TestTreePatternVectors(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		owners, want  bool
	}{
		{"*.go", "a.go", true, true},
		{"?.go", "é.go", false, false}, {"??.go", "é.go", false, true}, {"é.go", "é.go", false, true}, {"é.go", "a/é.go", true, true}, {"*.go", "src/a.go", true, true}, {"*.go", "src/a.GO", true, false},
		{"/docs/", "docs/a/b.md", true, true}, {"/docs/", "x/docs/a", true, false}, {"/docs/", "docs", true, false},
		{"apps/", "x/apps/a/b", true, true}, {"apps/", "apps/a", true, true}, {"apps/", "apps", true, false},
		{"docs/*", "docs/a", true, true}, {"docs/*", "docs/sub/a", true, true}, {"docs/*", "x/docs/a", true, false},
		{"/apps/github", "apps/github/a", true, true}, {"**/logs", "x/y/logs/a", true, true},
		{"a/**/b", "a/b", true, true}, {"a/**/b", "a/x/y/b", true, true}, {"a/**/b", "a/x/y/c", true, false},
		{"abc/**", "abc/x/y", true, true}, {"abc/**", "x/abc/y", true, false},
		{"file?.txt", "a/file1.txt", true, true}, {"file?.txt", "a/file12.txt", true, false},
		{"/a.go", "a.go", true, true}, {"/a.go", "dir/a.go", true, false},
		{`a\#b`, "a#b", true, true}, {`a\ b`, "a b", true, true}, {`literal\*`, "literal*", true, true},
		{"f**o", "f/a/o", true, false}, {"a/***/b", "a/x/y/b", true, true},
		{"*.go", "a/b.go", false, true}, {"src", "src/a", false, false}, {"src/", "src/a", false, false},
		{"src/**", "src/a/b", false, true}, {"src/*", "src/a/b", false, false},
		{"[ab]?.go", "a1.go", false, true}, {"[!a-z].go", "9.go", false, true}, {"[!a-z].go", "a.go", false, false},
		{"a[^x]b", "a/b", false, false}, {"a[!-x]b", "a-b", false, false}, {"a[!-x]b", "a/b", false, false},
	} {
		t.Run(tc.pattern+"/"+tc.path+"/"+string(rune('0'+boolInt(tc.owners))), func(t *testing.T) {
			r, err := treePattern(tc.pattern, tc.owners)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.MatchString(tc.path); got != tc.want {
				t.Fatalf("%q on %q: %v", tc.pattern, tc.path, got)
			}
		})
	}
	for _, p := range []string{"!x", `\#x`, "[a-z]", `trailing\`} {
		if _, err := treePattern(p, true); err == nil {
			t.Fatalf("unsupported CODEOWNERS pattern accepted: %s", p)
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestOwnersParsingAndCanonicalization(t *testing.T) {
	f := parseOwners("# comment\r\n* @Alice @ACME/Platform Ops@EXAMPLE.COM\na.go @Bob # inline\na.go\na\\#b @Alice\ninvalid @@@ @Alice\n")
	if !f.valid || len(f.diagnostics) != 1 || len(f.tokens) != 4 {
		t.Fatalf("%+v", f)
	}
	for token, want := range map[string]string{"@Alice": "owned:user:alice", "@ACME/Platform": "owned:team:acme/platform", "Ops@EXAMPLE.COM": "owned:email:Ops@example.com", "@robot[bot]": "owned:user:robot[bot]"} {
		owner, err := canonicalOwner(token)
		if err != nil || owner.Label() != want || !cp.ValidClassificationLabel(owner.Label()) {
			t.Fatalf("%v %v", owner, err)
		}
	}
	for _, token := range []string{"alice", "@a/b/c", "@/team", "@@alice", "@a_b", "ops@@example.com", "bad@.", "@org/team_", "@alice!", ""} {
		if _, err := canonicalOwner(token); err == nil {
			t.Fatalf("invalid owner accepted %q", token)
		}
	}
	if len(f.effective("a.go").owners) != 0 || f.effective("a.go").text != "a.go" {
		t.Fatal("last entry did not clear owners")
	}
	if f.effective("a#b").owners[0].Name != "alice" {
		t.Fatal("interior hash escape")
	}
	if len(f.effective("invalid").diagnostics) != 1 {
		t.Fatal("invalid owner lost")
	}
	for _, content := range []string{strings.Repeat(" ", MaxCodeownersBytes), "* @alice\x00", string([]byte{255}), "!x @alice", `\#x @alice`, "[a-z] @alice"} {
		parsed := parseOwners(content)
		if parsed.valid || len(parsed.diagnostics) == 0 {
			t.Fatal("invalid file not unknown")
		}
	}
	if !parseOwners(strings.Repeat(" ", MaxCodeownersBytes-1)).valid {
		t.Fatal("under limit rejected")
	}
}
