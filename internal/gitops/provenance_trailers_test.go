package gitops

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

// The oracle writes exact message bytes to a real commit and asks Git's
// independent commit reader for trailers. Only the production adapter produces
// GitTrailerOutput; the oracle deliberately returns an ordinary string.
func provenanceGitParser(t *testing.T) (parse func(string) provenance.GitTrailerOutput, readCommit func(string) string) {
	t.Helper()
	parser, err := NewTrailerParser(t.Context(), t.TempDir())
	must(t, err)
	t.Cleanup(func() { must(t, parser.Close()) })
	s := parser.s
	t.Log(parser.Policy().GitVersion)
	tree, err := s.command(t.Context(), "", "mktree")
	must(t, err)
	run := func(input string, args ...string) string {
		t.Helper()
		argv := append(append([]string{}, provenanceParserArgs...), args...)
		out, diagnostic, err := s.run(t.Context(), input, argv...)
		if err != nil {
			t.Fatalf("Git oracle: %v: %s", err, diagnostic)
		}
		return out
	}
	parse = func(message string) provenance.GitTrailerOutput {
		t.Helper()
		out, err := parser.Parse(t.Context(), message)
		must(t, err)
		return out
	}
	readCommit = func(message string) string {
		t.Helper()
		if strings.ContainsRune(message, 0) {
			t.Fatal("oracle cannot read embedded NUL")
		}
		commit := "tree " + tree + "\nauthor Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\n\n" + message
		oid := strings.TrimSpace(run(commit, "hash-object", "-t", "commit", "-w", "--stdin"))
		out := run("", "log", "-1", "--format=%(trailers:only,unfold)", oid)
		return strings.TrimSuffix(out, "\n")
	}
	return parse, readCommit
}

func TestProvenanceGitTrailerRegressions(t *testing.T) {
	parse, readCommit := provenanceGitParser(t)
	classifier, err := provenance.NewClassifier([]provenance.IdentityRule{{ID: "human", Mailbox: "human@example.test", Class: provenance.Human}})
	must(t, err)
	t.Run("URL in prose", func(t *testing.T) {
		// Git < 2.56 parses the URL as "https: //example.com", admitting
		// Agent-Session as agent evidence: a tightening-only difference from
		// 2.56. Compare readers from the same binary, not a fixed trailer set
		// or class, because provenance depends on the deployed Git version.
		message := "subject\n\nhttps://example.com\nAgent-Session: a:one\n"
		parsed := parse(message)
		if reference := readCommit(message); string(parsed) != reference {
			t.Fatalf("adapter %q != commit reader %q", parsed, reference)
		}
		t.Logf("Git trailers: %q", parsed)
	})
	for _, tc := range []struct {
		name, message, parsed string
		class                 provenance.Class
	}{
		{"cherry-pick marker", "subject\n\nAgent-Session: a:one\n(cherry picked from commit " + strings.Repeat("a", 40) + ")\n", "Agent-Session: a:one\n", provenance.Mixed},
		{"patch divider with text", "subject\n\nAgent-Session: a:one\n--- patch\ndiff\n", "", provenance.Human},
		{"patch divider with whitespace", "subject\n\nAgent-Session: a:one\n--- \t\ndiff\n", "", provenance.Human},
		{"divider before declaration", "subject\n\nbody\n--- patch\n\nAgent-Session: a:one\n", "Agent-Session: a:one\n", provenance.Mixed},
		{"markdown rule before checkpoint", "subject\n\nbody\n---\n\nEntire-Checkpoint: abcdef012345\n", "Entire-Checkpoint: abcdef012345\n", provenance.Mixed},
		{"reserved metadata continuation", "subject\n\nOpenMerge-Batch: contributor\n Agent-Session: a:one\n", "OpenMerge-Batch: contributor Agent-Session: a:one\n", provenance.Human},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parse(tc.message)
			if reference := readCommit(tc.message); string(parsed) != reference {
				t.Fatalf("adapter %q != commit reader %q", parsed, reference)
			}
			if string(parsed) != tc.parsed {
				t.Fatalf("Git output %q, want %q", parsed, tc.parsed)
			}
			declarations := provenance.ParseGitTrailers(parsed)
			identities := classifier.Declarations(provenance.IdentityInput{AuthorMailbox: "human@example.test", ParsedTrailers: parsed})
			if got := provenance.Classify(append(declarations, identities...)); got != tc.class {
				t.Fatalf("class %s, want %s", got, tc.class)
			}
		})
	}
}

// Compare against Git's commit-reader set, including unknown keys and invalid
// unfolded values. The oracle only projects known corpus values; it
// never tries to locate blocks, count lines, fold continuations or skip comments.
func TestProvenanceGitTrailerDifferential(t *testing.T) {
	parse, readCommit := provenanceGitParser(t)
	classifier, err := provenance.NewClassifier([]provenance.IdentityRule{{ID: "bot", Mailbox: "bot@example.test", Class: provenance.Agent}})
	must(t, err)
	prefixes := []string{"subject\n\n", "subject\n", "", "subject\n\nprose\n", "subject\n\nhttps://example.com\n", "subject\n\n# comment\n"}
	blocks := []string{
		"Agent-Session: a:one", "aGeNt-SeSsIoN: a:one", "Entire-Checkpoint: abcdef012345",
		"ENTIRE-CHECKPOINT: abcdef012345\nAgent-Session: a:one",
		"Agent-Session: a:one\nAgent-Session: a:one\nAgent-Session: b:two",
		"Agent-Session: a:\n one", "Entire-Checkpoint:\n\tabcdef012345",
		"Agent-Session: a:one\n# comment\n continuation", "Agent-Session: a:one\n# comment",
		"Co-Authored-By: Bot\n <bot@example.test>\nAgent-Session: a:one",
		"Agent-Session: a:one\nSigned-off-by: A <a@example.test>\nprose\nprose\nprose\nprose\nprose\nprose",
		"Agent-Session: a:one\nsigned-off-by: A <a@example.test>\nprose\nprose\nprose\nprose\nprose\nprose",
	}
	suffixes := []string{"\n", "\n\n", "\n# trailing comment\n", "\n(cherry picked from commit " + strings.Repeat("b", 40) + ")\n", "\n--- patch\ndiff\n", "\n--- \t\ndiff\n", "\n\nMore prose\n", "\nhttps://example.com\n"}
	outcomes := map[string]provenance.Outcome{
		"a:one": provenance.NotRequested, "b:two": provenance.NotRequested,
		"abcdef012345": provenance.NotRequested, "a: one": provenance.InvalidValue,
		"a:one continuation": provenance.InvalidValue, "a:\r one": provenance.InvalidValue,
		"\r abcdef012345": provenance.InvalidValue, "a:one\r continuation": provenance.InvalidValue,
	}
	count := 0
	for _, prefix := range prefixes {
		for _, block := range blocks {
			for _, suffix := range suffixes {
				for _, newline := range []string{"\n", "\r\n"} {
					message := strings.ReplaceAll(prefix+block+suffix, "\n", newline)
					parsed := parse(message)
					reference := readCommit(message)
					if string(parsed) != reference {
						t.Fatalf("adapter %q != commit reader %q: message %q", parsed, reference, message)
					}
					want := map[provenance.Declaration]bool{}
					coauthor := false
					for line := range strings.SplitSeq(string(reference), "\n") {
						key, value, ok := strings.Cut(line, ": ")
						if !ok {
							continue
						}
						key = strings.ToLower(key)
						if key == "co-authored-by" && (value == "Bot <bot@example.test>" || value == "Bot\r <bot@example.test>") {
							coauthor = true
						}
						if key != "agent-session" && key != "entire-checkpoint" {
							continue
						}
						outcome, known := outcomes[value]
						if !known {
							t.Fatalf("unlisted corpus value %q from Git: %q", value, parsed)
						}
						want[provenance.Declaration{Source: "git-trailers", Version: provenance.ParserVersion, Kind: key, Value: value, Outcome: outcome, Class: provenance.Agent}] = true
					}
					got := provenance.ParseGitTrailers(parsed)
					if len(got) != len(want) {
						t.Fatalf("set size mismatch: message %q, Git %q, declarations %+v", message, parsed, got)
					}
					for _, d := range got {
						if !want[d] {
							t.Fatalf("unexpected declaration %+v: message %q, Git %q", d, message, parsed)
						}
						delete(want, d)
					}
					identities := classifier.Declarations(provenance.IdentityInput{ParsedTrailers: parsed})
					var wantIdentities []provenance.Declaration
					if coauthor {
						wantIdentities = append(wantIdentities, provenance.Declaration{Source: "git-identities", Version: provenance.ClassifierVersion, Kind: "co-authored-by", Value: "bot@example.test", Outcome: provenance.NotRequested, Class: provenance.Agent, RuleID: "bot"})
					}
					if !slices.Equal(identities, wantIdentities) {
						t.Fatalf("coauthor mismatch: message %q, Git %q, identities %+v", message, parsed, identities)
					}
					count++
				}
			}
		}
	}
	t.Logf("compared %d messages against Git's commit-reader trailer set", count)
}

func TestProvenanceGitTrailerConfigIsolation(t *testing.T) {
	parse, readCommit := provenanceGitParser(t)
	message := "subject\n\nAgent-Session: a:one\n# comment\n"
	want := parse(message)
	if want != "Agent-Session: a:one\n" {
		t.Fatalf("unexpected baseline: %q", want)
	}
	if got := readCommit(message); got != string(want) {
		t.Fatalf("unexpected commit-reader baseline: %q", got)
	}
	poison := filepath.Join(t.TempDir(), "gitconfig")
	must(t, os.WriteFile(poison, []byte("[trailer]\nseparators = =\n[trailer \"agent-session\"]\nkey = Hidden\n[core]\ncommentChar = ;\ncommentString = ;\n"), 0o600))
	for _, key := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG"} {
		t.Setenv(key, poison)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "trailer.separators")
	t.Setenv("GIT_CONFIG_VALUE_0", "=")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'trailer.agent-session.key=Hidden'")
	for name, read := range map[string]func(string) string{"adapter": func(s string) string { return string(parse(s)) }, "commit reader": readCommit} {
		if got := read(message); got != string(want) {
			t.Fatalf("inherited config changed %s output: %q != %q", name, got, want)
		}
	}
}
