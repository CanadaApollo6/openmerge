package classification

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Only local Git subprocesses are used. Scrub inherited repository/config
// variables as well as system/global attributes, and use an empty HOME.
func gitOracle(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir, home := t.TempDir(), t.TempDir()
	env := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") && !strings.HasPrefix(item, "HOME=") && !strings.HasPrefix(item, "LC_ALL=") {
			env = append(env, item)
		}
	}
	env = append(env, "HOME="+home, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1")
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null"}, args...)...)
		cmd.Env, cmd.Stdin = env, strings.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || args[0] != "check-ignore" {
				t.Fatalf("git %v: %v: %s", args, err, stderr.String())
			}
		}
		return string(raw)
	}
	run("", "init", "-q")
	return dir, run
}

func oraclePatterns() []string {
	patterns := []string{"/a.go", "a/", "a//b.go", "a/b.go/", "a/./b.go", "a?b", "?.go", "??.go", "[ab].go", "[!a-z].go", "[^a-z].go", "[,-0]", "a[,-0]b", "a[!-x]b", "[[:alpha:]].go", "[abc", "[]a].go", "[-ab].go", `a\ b.go`, `a\#b`, `literal\*`, `a\?b`, `a\`, `a/**\/b.go`, `a/\**/b.go`, `a\/b.go`, `a/\?/b.go`, `a\\b.go`, `"a\303\251.go"`, `"a\q.go"`, `"a.go\000suffix"`, `\#file`, "!a.go", `"a b.go"`, `"a\142.go"`, `"é.go"`}
	for n := 1; n <= 4; n++ {
		stars := strings.Repeat("*", n)
		for _, template := range []string{"%s", "/%s", "%s/", "%s/b.go", "a/%s/b.go", "a/%s", "a%s", "%sb.go", "a%sb", "a/%s/x/%s/b.go"} {
			patterns = append(patterns, strings.ReplaceAll(template, "%s", stars))
		}
	}
	// Seeded combinations extend the hand-picked boundary vectors reproducibly.
	rng := rand.New(rand.NewSource(69))
	pieces := []string{"a", "b.go", "*", "**", "***", "****", "?", "[!x]", "[,-0]"}
	for i := 0; i < 40; i++ {
		patterns = append(patterns, pieces[rng.Intn(len(pieces))]+"/"+pieces[rng.Intn(len(pieces))])
	}
	return patterns
}

var oraclePaths = []string{"a.go", "b.go", "9.go", "é.go", "ab.go", "a b.go", "literal*", "#file", "a#b", "a-b", "a,b", "a0b", "a/b", "a/x/b", "a/b.go", "a/x/b.go", "a/x/y/b.go", "x/a/b.go", "src/a.go", "src/sub/a.go", "a.go/child", "a/b.go/child", "a/x/x/b.go", "a/ab", "x/y/a.go", "x/y/b.go"}

func TestAttributeDifferentialCorpus(t *testing.T) {
	dir, run := gitOracle(t)
	type vector struct{ root, nested, deeper string }
	var corpus []vector
	for _, pattern := range oraclePatterns() {
		corpus = append(corpus, vector{root: pattern + " linguist-generated\n"}, vector{root: "* linguist-generated\n" + pattern + " -linguist-generated\n"})
	}
	for _, assignment := range []string{"linguist-generated", "-linguist-generated", "!linguist-generated", "linguist-generated=false", "linguist-generated=true", "binary", "linguist-generated=other"} {
		corpus = append(corpus, vector{"* linguist-generated\n", "\ufeff* " + assignment + "\r\n", "b.go !linguist-generated\n"})
	}
	corpus = append(corpus, vector{root: "\ufeff* linguist-generated\r\n"}, vector{root: "* linguist-generated\n*.go -linguist-generated" + strings.Repeat(" a", 1100) + "\n"}, vector{root: "*.go linguist-generated" + strings.Repeat(" a", 1100) + "\n"}, vector{root: "[attr]binary linguist-generated\n* binary\n"})
	comparisons, closed := 0, 0
	for index, v := range corpus {
		contents := map[string]string{".gitattributes": v.root, "a/.gitattributes": v.nested, "a/x/.gitattributes": v.deeper}
		for name, content := range contents {
			file := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		actual := strings.Split(run(strings.Join(oraclePaths, "\x00")+"\x00", "check-attr", "-z", "--stdin", "linguist-generated"), "\x00")
		if len(actual) != len(oraclePaths)*3+1 {
			t.Fatal(actual)
		}
		for j, p := range oraclePaths {
			g := forge.GoverningFiles{}
			for _, name := range attributePaths(p) {
				if content, ok := contents[name]; ok {
					g.Attributes = append(g.Attributes, present(name, content))
				} else {
					g.Attributes = append(g.Attributes, forge.GoverningFile{Path: name, Coverage: complete})
				}
			}
			got := generatedAttribute(p, g)
			want := cp.False
			if actual[j*3+2] == "set" || actual[j*3+2] == "true" {
				want = cp.True
			}
			comparisons++
			if got == cp.Unknown {
				closed++
				continue
			}
			if got != want {
				t.Errorf("case %d path %q root=%q nested=%q: got %v git=%q", index, p, v.root, v.nested, got, actual[j*3+2])
			}
		}
	}
	t.Logf("attributes: %d inputs, %d comparisons, %d Unknown, zero non-fail-closed disagreements required", len(corpus), comparisons, closed)
}

func TestCodeownersDifferentialCorpus(t *testing.T) {
	dir, run := gitOracle(t)
	comparisons, closed := 0, 0
	patterns := oraclePatterns()
	for _, pattern := range patterns {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(pattern+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		matches := map[string]bool{}
		for _, p := range strings.Split(run(strings.Join(oraclePaths, "\x00")+"\x00", "check-ignore", "--no-index", "-z", "--stdin"), "\x00") {
			matches[p] = true
		}
		parsed := parseOwners(pattern + " @alice\n")
		for _, p := range oraclePaths {
			comparisons++
			if !parsed.valid {
				closed++
				continue
			}
			entry := parsed.effective(p)
			if entry != nil && entry.pattern.ambiguousDirectory(p) {
				closed++
				continue
			}
			got := entry != nil
			if got != matches[p] {
				t.Errorf("pattern %q path %q got %v git=%v", pattern, p, got, matches[p])
			}
		}
	}
	t.Logf("CODEOWNERS: %d patterns, %d comparisons, %d Unknown, zero non-fail-closed disagreements required", len(patterns), comparisons, closed)
}

func TestOwnerBudgets(t *testing.T) {
	for name, content := range map[string]string{
		"entries": strings.Repeat("* @alice\n", MaxOwnerEntries+1),
		"line":    strings.Repeat("a", MaxOwnerLineBytes+1) + " @alice",
		"owners":  "*" + strings.Repeat(" @alice", MaxOwnersPerEntry+1),
		"tokens": func() string {
			var s strings.Builder
			for i := 0; i <= MaxOwnerTokens; i++ {
				fmt.Fprintf(&s, "* @user%d\n", i)
			}
			return s.String()
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			f := parseOwners(content)
			if f.valid || len(f.diagnostics) == 0 {
				t.Fatal("budget did not fail closed")
			}
		})
	}
}
