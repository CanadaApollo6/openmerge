package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

// Slice 1 stays inert: only tests invoke Git for provenance. Reuse the session
// wrapper's environment isolation, plus a fresh template-free repository so
// no local trailer.* aliases/config can alter the reference parser's output.
func provenanceGitParser(t *testing.T) func(string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	must(t, err)
	s := &session{r: &Repo{git: git, cfg: Config{Now: time.Now}}}
	s.dir = filepath.Join(t.TempDir(), "parser.git")
	_, err = s.command(t.Context(), "", "init", "--bare", "--template=", s.dir)
	must(t, err)
	version, err := s.command(t.Context(), "", "--version")
	must(t, err)
	t.Log(version)
	return func(message string) string {
		t.Helper()
		out, diagnostic, err := s.run(t.Context(), message,
			"-c", "trailer.separators=:", "-c", "core.commentChar=#", "-c", "core.commentString=#",
			"interpret-trailers", "--parse")
		if err != nil {
			t.Fatalf("Git trailer parser: %v: %s", err, diagnostic)
		}
		return out
	}
}

func TestProvenanceGitTrailerRegressions(t *testing.T) {
	parse := provenanceGitParser(t)
	classifier, err := provenance.NewClassifier([]provenance.IdentityRule{{ID: "human", Mailbox: "human@example.test", Class: provenance.Human}})
	must(t, err)
	for _, tc := range []struct {
		name, message, parsed string
		class                 provenance.Class
	}{
		{"cherry-pick marker", "subject\n\nAgent-Session: a:one\n(cherry picked from commit " + strings.Repeat("a", 40) + ")\n", "Agent-Session: a:one\n", provenance.Mixed},
		{"patch divider with text", "subject\n\nAgent-Session: a:one\n--- patch\ndiff\n", "Agent-Session: a:one\n", provenance.Mixed},
		{"patch divider with whitespace", "subject\n\nAgent-Session: a:one\n--- \t\ndiff\n", "Agent-Session: a:one\n", provenance.Mixed},
		{"URL in prose", "subject\n\nhttps://example.com\nAgent-Session: a:one\n", "", provenance.Human},
		{"divider before declaration", "subject\n\nbody\n--- patch\n\nAgent-Session: a:one\n", "", provenance.Human},
		{"reserved metadata continuation", "subject\n\nOpenMerge-Batch: contributor\n Agent-Session: a:one\n", "OpenMerge-Batch: contributor Agent-Session: a:one\n", provenance.Human},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parse(tc.message)
			if parsed != tc.parsed {
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

// Compare against precisely the set emitted by Git, including unknown keys and
// invalid unfolded values. The oracle only projects known corpus values; it
// never tries to locate blocks, count lines, fold continuations or skip comments.
func TestProvenanceGitTrailerDifferential(t *testing.T) {
	parse := provenanceGitParser(t)
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
					want := map[provenance.Declaration]bool{}
					coauthor := false
					for line := range strings.SplitSeq(parsed, "\n") {
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
	t.Logf("compared %d messages against Git's trailer set", count)
}

func TestProvenanceGitTrailerConfigIsolation(t *testing.T) {
	parse := provenanceGitParser(t)
	message := "subject\n\nAgent-Session: a:one\n# comment\n"
	want := parse(message)
	if want != "Agent-Session: a:one\n" {
		t.Fatalf("unexpected baseline: %q", want)
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
	if got := parse(message); got != want {
		t.Fatalf("inherited config changed output: %q != %q", got, want)
	}
}
