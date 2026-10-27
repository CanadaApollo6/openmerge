package gitops

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

type provenanceFixture struct {
	t                        *testing.T
	dir, scratch, tree, root string
	classifier               provenance.Classifier
}

func newProvenanceFixture(t *testing.T) *provenanceFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "source.git")
	gitCommand(t, "", "", nil, "init", "--bare", "--template=", dir)
	f := &provenanceFixture{t: t, dir: dir, scratch: t.TempDir()}
	f.tree = gitCommand(t, dir, "", nil, "mktree")
	var err error
	f.classifier, err = provenance.NewClassifier([]provenance.IdentityRule{
		{ID: "human", Mailbox: "human@example.test", Class: provenance.Human},
		{ID: "generated", Mailbox: "openmerge@localhost", Class: provenance.Agent},
	})
	must(t, err)
	f.root = f.commit("root\n", nil)
	return f
}

func (f *provenanceFixture) commit(message string, parents []string) string {
	f.t.Helper()
	// hash-object preserves NUL, CRLF, and invalid UTF-8 byte-for-byte.
	raw := "tree " + f.tree + "\n"
	for _, parent := range parents {
		raw += "parent " + parent + "\n"
	}
	raw += "author Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\n\n" + message
	return gitCommand(f.t, f.dir, raw, nil, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
}

func (f *provenanceFixture) request(head, pinned, build string) provenance.InventoryRequest {
	return provenance.InventoryRequest{Repository: "fixture", Queue: "main", ChangeID: "change-1", Revision: "r1", Generation: "g1", Head: head, PinnedBase: pinned, BuildBase: build}
}

func (f *provenanceFixture) read(req provenance.InventoryRequest, limits provenance.InventoryLimits) provenance.Inventory {
	f.t.Helper()
	return ReadProvenanceInventory(f.t.Context(), f.dir, f.scratch, req, f.classifier, limits)
}

func originalIDs(commits []provenance.Commit) []string {
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.ObjectID
	}
	slices.Sort(out)
	return out
}

func expectInventoryIDs(t *testing.T, inv provenance.Inventory, want ...string) {
	t.Helper()
	slices.Sort(want)
	if !inv.Complete || inv.Diagnostic.Outcome != provenance.ReadComplete || !slices.Equal(originalIDs(inv.Commits), want) {
		t.Fatalf("inventory = %+v, want originals %v", inv, want)
	}
}

func TestProvenanceInventoryAllParentsAndBaseRewrites(t *testing.T) {
	f := newProvenanceFixture(t)
	common := f.commit("common\n", []string{f.root})
	left := f.commit("left\n", []string{common})
	side1 := f.commit("side 1\n\nAgent-Session: a:one\n", []string{common})
	side2 := f.commit("side 2\n\nEntire-Checkpoint: abcdef012345\n", []string{side1})
	merge := f.commit("merge\n", []string{left, side2})
	head := f.commit("tip\n", []string{merge})
	limits := provenance.DefaultInventoryLimits()
	stable := f.read(f.request(head, common, common), limits)
	expectInventoryIDs(t, stable, left, side1, side2, merge, head)
	if !slices.Equal(stable.MergeBases, []string{common}) {
		t.Fatal(stable.MergeBases)
	}
	// Ordinary predecessor landings cannot erase the stable inventory.
	landed := f.read(f.request(head, common, merge), limits)
	expectInventoryIDs(t, landed, left, side1, side2, merge, head)
	if !slices.Equal(landed.Stable, stable.Stable) || len(landed.Additional) != 0 {
		t.Fatal("stable obligations changed")
	}
	// A rewritten base exposes an additional original with unchanged head.
	rewritten := f.commit("rewritten base\n", []string{f.root})
	req := f.request(head, common, rewritten)
	req.Generation = "g2"
	refreshed := f.read(req, limits)
	expectInventoryIDs(t, refreshed, common, left, side1, side2, merge, head)
	if !slices.Equal(refreshed.Additional, []string{common}) || !slices.Equal(refreshed.BuildMergeBases, []string{f.root}) || refreshed.Request.Generation != "g2" {
		t.Fatalf("rewrite = %+v", refreshed)
	}
	oracle := strings.Fields(gitCommand(t, f.dir, "", nil, "rev-list", head, "^"+rewritten))
	slices.Sort(oracle)
	if !slices.Equal(originalIDs(refreshed.Commits), oracle) {
		t.Fatal("union disagrees with Git")
	}
	// Shared originals are counted once per member, never pooled across changes.
	member := req
	member.ChangeID = "change-2"
	second := f.read(member, limits)
	expectInventoryIDs(t, second, common, left, side1, side2, merge, head)
	if second.Request.ChangeID == refreshed.Request.ChangeID {
		t.Fatal("member identity lost")
	}
	member.Revision, member.Generation, member.Head = "r2", "g3", side1
	repushed := f.read(member, limits)
	expectInventoryIDs(t, repushed, common, side1)
	// Exclusions come from exact trusted build records, never trailer spelling.
	marker := f.commit("marker\n\nOpenMerge-Batch: synthetic\n", []string{head})
	req = f.request(marker, common, common)
	req.Excluded = []string{marker, left}
	excluded := f.read(req, limits)
	expectInventoryIDs(t, excluded, side1, side2, merge, head)
	// Untrusted metadata on an ordinary original cannot exclude it.
	forged := f.commit("forged\n\nOpenMerge-Batch: synthetic\nOpenMerge-Replayed-From: "+side1+"\n", []string{head})
	expectInventoryIDs(t, f.read(f.request(forged, head, head), limits), forged)
}

func TestProvenanceInventoryPolicyAndOriginalBytes(t *testing.T) {
	f := newProvenanceFixture(t)
	message := "subject\n\nhttps://example.com\nAgent-Session: a:one\n"
	head := f.commit(message, []string{f.root})
	inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, head)
	parse, oracle := provenanceGitParser(t)
	parsed := parse(message)
	if string(parsed) != oracle(message) {
		t.Fatal("parser differs from Git commit reader")
	}
	want := provenance.Classify(append(provenance.ParseGitTrailers(parsed), f.classifier.Declarations(provenance.IdentityInput{AuthorMailbox: "human@example.test", CommitterMailbox: "human@example.test", ParsedTrailers: parsed})...))
	if got := provenance.Classify(inv.Commits[0].Declarations); got != want {
		t.Fatalf("class %s, want %s", got, want)
	}
	version := gitCommand(t, "", "", nil, "version")
	if inv.Policy.GitVersion != version || inv.PolicyID != inv.Policy.Identity() {
		t.Fatalf("policy = %+v", inv.Policy)
	}
	changed := inv.Policy
	changed.GitVersion += "-upgrade"
	if changed.Identity() == inv.PolicyID {
		t.Fatal("Git upgrade did not change policy identity")
	}
	// Config in the source repo cannot remap keys, execute commands, rewrite
	// identities through mailmap, or alter original byte interpretation.
	gitCommand(t, f.dir, "", nil, "config", "trailer.agent-session.key", "Hidden")
	gitCommand(t, f.dir, "", nil, "config", "core.commentChar", ";")
	gitCommand(t, f.dir, "", nil, "config", "trailer.agent-session.command", "false")
	again := f.read(inv.Request, provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, again, head)
	if !slices.Equal(again.Commits[0].Declarations, inv.Commits[0].Declarations) {
		t.Fatal("source config changed declarations")
	}
	for _, message := range []string{
		"subject\n\nAgent-Session: a:one\n--- patch\ndiff\n",
		"subject\n\nOpenMerge-Batch: contributor\n Agent-Session: a:one\n",
		"subject\n\nbody\n---\n\nEntire-Checkpoint: abcdef012345\n",
		"subject\r\n\r\nAgent-Session: a:\r\n one\r\n",
	} {
		head = f.commit(message, []string{f.root})
		got := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
		expectInventoryIDs(t, got, head)
		want := provenance.ParseGitTrailers(parse(message))
		var trailers []provenance.Declaration
		for _, d := range got.Commits[0].Declarations {
			if d.Source == "git-trailers" {
				trailers = append(trailers, d)
			}
		}
		if !slices.Equal(trailers, want) {
			t.Fatalf("original bytes were rewritten: %q", message)
		}
	}
}

func TestProvenanceInventoryFailuresNeverBecomeHuman(t *testing.T) {
	f := newProvenanceFixture(t)
	nul := f.commit("subject\n\nbody\x00\n\nEntire-Checkpoint: abcdef012345\n", []string{f.root})
	req := f.request(nul, f.root, f.root)
	inv := f.read(req, provenance.DefaultInventoryLimits())
	expectPendingInventory(t, inv)
	if inv.Complete || inv.Diagnostic.Resource != "message_nul" || inv.Diagnostic.Outcome != provenance.ReadPending || len(inv.Commits) != 1 || inv.Commits[0].Complete || len(inv.Commits[0].Declarations) != 0 {
		t.Fatalf("NUL = %+v", inv)
	}
	human := f.commit("human\n", []string{f.root})
	req = f.request(human, f.root, f.root)
	t.Run("execution failure", func(t *testing.T) {
		parser, err := NewTrailerParser(t.Context(), f.scratch)
		must(t, err)
		defer func() { must(t, parser.Close()) }()
		parser.s.r.git = filepath.Join(t.TempDir(), "missing-git")
		out, err := parser.Parse(t.Context(), "human\n")
		if err == nil || out != "" {
			t.Fatalf("failed parser returned %q, %v", out, err)
		}
	})
	t.Run("nonzero parser exit", func(t *testing.T) {
		parser, err := NewTrailerParser(t.Context(), f.scratch)
		must(t, err)
		defer func() { must(t, parser.Close()) }()
		must(t, os.WriteFile(filepath.Join(parser.s.dir, "config"), []byte("[broken\n"), 0o600))
		out, err := parser.Parse(t.Context(), "human\n")
		if err == nil || out != "" {
			t.Fatalf("failed parser returned %q, %v", out, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got := ReadProvenanceInventory(ctx, f.dir, f.scratch, req, f.classifier, provenance.DefaultInventoryLimits())
		if got.Complete || got.Diagnostic.Outcome != provenance.ReadPending {
			t.Fatalf("cancelled = %+v", got)
		}
	})
	t.Run("missing object", func(t *testing.T) {
		bad := req
		bad.Head = strings.Repeat("f", 40)
		got := f.read(bad, provenance.DefaultInventoryLimits())
		if got.Complete || got.Diagnostic.Outcome != provenance.ReadPending {
			t.Fatalf("missing = %+v", got)
		}
	})
	t.Run("tag is not commit", func(t *testing.T) {
		raw := "object " + human + "\ntype commit\ntag v1\ntagger Human <human@example.test> 1 +0000\n\ntag\n"
		tag := gitCommand(t, f.dir, raw, nil, "hash-object", "-t", "tag", "-w", "--stdin")
		bad := req
		bad.Head = tag
		got := f.read(bad, provenance.DefaultInventoryLimits())
		if got.Complete {
			t.Fatalf("tag accepted = %+v", got)
		}
	})
}

func TestProvenanceInventorySourceLimits(t *testing.T) {
	for _, tc := range []struct {
		name, message, resource string
		lower                   func(*provenance.InventoryLimits)
	}{
		{"source commits", "human\n", "source_commits", func(l *provenance.InventoryLimits) { l.SourceCommits = 1 }},
		{"graph commits", "human\n", "graph", func(l *provenance.InventoryLimits) { l.GraphCommits = 1 }},
		{"graph bytes", "human\n", "graph_bytes", func(l *provenance.InventoryLimits) { l.GraphBytes = 300 }},
		{"commit bytes", strings.Repeat("x", 200), "commit_bytes", func(l *provenance.InventoryLimits) { l.CommitBytes = 300 }},
		{"message bytes", strings.Repeat("x", 50), "message_bytes", func(l *provenance.InventoryLimits) { l.MessageBytes = 75 }},
		{"declarations", "subject\n\nAgent-Session: a:one\nAgent-Session: a:two\n", "declarations", func(l *provenance.InventoryLimits) { l.Declarations = 3 }},
		{"time", "human\n", "time", func(l *provenance.InventoryLimits) { l.Duration = time.Nanosecond }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProvenanceFixture(t)
			first := f.commit(tc.message, []string{f.root})
			head := f.commit(tc.message+"\n", []string{first})
			limits := provenance.DefaultInventoryLimits()
			tc.lower(&limits)
			got := f.read(f.request(head, f.root, f.root), limits)
			expectPendingInventory(t, got)
			if got.Complete || got.Diagnostic.Outcome != provenance.LimitExceeded || got.Diagnostic.Resource != tc.resource {
				t.Fatalf("limit = %+v", got)
			}
			// Limits must remain explicit, terminal, and stable for unchanged inputs.
			again := f.read(f.request(head, f.root, f.root), limits)
			if again.Diagnostic != got.Diagnostic {
				t.Fatalf("unstable terminal diagnostic: %+v != %+v", again.Diagnostic, got.Diagnostic)
			}
			for _, commit := range got.Commits {
				if !commit.Complete && len(commit.Declarations) != 0 {
					t.Fatal("failed commit retained human declarations")
				}
			}
		})
	}
}

func TestProvenanceInventoryRawHistoryCannotBeHidden(t *testing.T) {
	f := newProvenanceFixture(t)
	agent := f.commit("agent\n\nAgent-Session: a:one\n", []string{f.root})
	head := f.commit("human\n", []string{agent})
	fake := f.commit("replacement without agent ancestry\n", []string{f.root})
	gitCommand(t, f.dir, "", nil, "replace", head, fake)
	must(t, os.WriteFile(filepath.Join(f.dir, "shallow"), []byte(head+"\n"), 0o600))
	must(t, os.MkdirAll(filepath.Join(f.dir, "info"), 0o700))
	must(t, os.WriteFile(filepath.Join(f.dir, "info", "grafts"), []byte(head+" "+f.root+"\n"), 0o600))
	req := f.request(head, f.root, f.root)
	got := f.read(req, provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, got, agent, head)
	// A shallow boundary with missing raw ancestors is incomplete, even if
	// Git's ordinary shallow walk and replace ref would have hidden the gap.
	must(t, os.Remove(filepath.Join(f.dir, "objects", agent[:2], agent[2:])))
	missing := f.read(req, provenance.DefaultInventoryLimits())
	if missing.Complete || missing.Diagnostic.Outcome != provenance.ReadPending {
		t.Fatalf("hidden missing ancestor passed: %+v", missing)
	}
}

func TestProvenanceInventoryCrissCrossMergeBases(t *testing.T) {
	f := newProvenanceFixture(t)
	left := f.commit("left\n", []string{f.root})
	right := f.commit("right\n", []string{f.root})
	base := f.commit("base\n", []string{left, right})
	head := f.commit("head\n", []string{right, left})
	got := f.read(f.request(head, base, base), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, got, head)
	oracle := strings.Fields(gitCommand(t, f.dir, "", nil, "merge-base", "--all", base, head))
	slices.Sort(oracle)
	if !slices.Equal(got.MergeBases, oracle) || len(oracle) != 2 {
		t.Fatalf("merge bases = %v, Git = %v", got.MergeBases, oracle)
	}
}

func TestProvenanceInventoryEmptyAndDeduplicatedDeclarations(t *testing.T) {
	f := newProvenanceFixture(t)
	limits := provenance.DefaultInventoryLimits()
	empty := f.read(f.request(f.root, f.root, f.root), limits)
	expectInventoryIDs(t, empty)
	message := "subject\n\n" + strings.Repeat("Agent-Session: a:one\n", 100)
	head := f.commit(message, []string{f.root})
	limits.Declarations = 3 // author, committer, and one deduplicated declaration.
	got := f.read(f.request(head, f.root, f.root), limits)
	expectInventoryIDs(t, got, head)
	if len(got.Commits[0].Declarations) != 3 {
		t.Fatalf("duplicates counted as separate declarations: %+v", got.Commits[0])
	}
	limits.SourceCommits = provenance.DefaultInventoryLimits().SourceCommits + 1
	if got := f.read(f.request(head, f.root, f.root), limits); got.Diagnostic.Outcome != provenance.InvalidInput {
		t.Fatal("raised ceiling accepted")
	}
}

func TestProvenanceInventoryDefaultSourceAndDeclarationCeilings(t *testing.T) {
	f := newProvenanceFixture(t)
	limits := provenance.DefaultInventoryLimits()
	tip := f.root
	for i := range limits.SourceCommits {
		tip = f.commit("source "+string(rune('A'+i))+"\n", []string{tip})
	}
	atLimit := f.read(f.request(tip, f.root, f.root), limits)
	if !atLimit.Complete || len(atLimit.Commits) != limits.SourceCommits {
		t.Fatalf("source ceiling = %+v, count %d", atLimit.Diagnostic, len(atLimit.Commits))
	}
	tip = f.commit("one too many\n", []string{tip})
	over := f.read(f.request(tip, f.root, f.root), limits)
	if over.Complete || over.Diagnostic.Outcome != provenance.LimitExceeded || over.Diagnostic.Resource != "source_commits" {
		t.Fatalf("source overflow = %+v", over.Diagnostic)
	}
	var lines []string
	for i := range limits.Declarations - 2 {
		lines = append(lines, "Agent-Session: a:"+string(rune('a'+i)))
	}
	head := f.commit("subject\n\n"+strings.Join(lines, "\n")+"\n", []string{f.root})
	exact := f.read(f.request(head, f.root, f.root), limits)
	expectInventoryIDs(t, exact, head)
	if len(exact.Commits[0].Declarations) != limits.Declarations {
		t.Fatal("wrong declaration boundary")
	}
	head = f.commit("subject\n\n"+strings.Join(lines, "\n")+"\nAgent-Session: a:extra\n", []string{f.root})
	over = f.read(f.request(head, f.root, f.root), limits)
	if over.Complete || over.Diagnostic.Outcome != provenance.LimitExceeded || over.Diagnostic.Resource != "declarations" {
		t.Fatalf("declaration overflow = %+v", over.Diagnostic)
	}
}

func expectPendingInventory(t *testing.T, inventory provenance.Inventory) {
	t.Helper()
	inputs := provenance.DeclarationAttributes(inventory.Commits, inventory.Complete)
	for _, text := range []string{"provenance = human", "#agent-commits = 0"} {
		expr, err := provenance.ParseCondition(text)
		must(t, err)
		if provenance.Truth(expr, inputs) != cp.Unknown || provenance.Truth(condition.Not(expr), inputs) != cp.Unknown {
			t.Fatalf("incomplete inventory authorized %s or its negation", text)
		}
	}
}
