package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestRevisionDiffContract(t *testing.T) {
	f := newFixture(t)
	tip := f.commit(f.base, "base moves", map[string]string{"base-only.go": "code"})
	f.push("main", tip, false)
	forgetest.RunRevisionDiff(t, f.g, testRepo, forge.RevisionDiffRequest{BaseSHA: tip, HeadSHA: f.prs[0].HeadSHA}, f.base, "pr-1.txt")
}

type diffEntry struct{ mode, content, object string }

func (f *fixture) diffCommit(parent, message string, entries map[string]diffEntry, moreParents ...string) string {
	f.t.Helper()
	index := filepath.Join(f.t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	if parent != "" {
		gitCommand(f.t, f.source, "", env, "read-tree", parent)
	} else {
		gitCommand(f.t, f.source, "", env, "read-tree", "--empty")
	}
	var input strings.Builder
	blobs := map[string]string{}
	paths := []string{}
	for p := range entries {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, path := range paths {
		entry := entries[path]
		id := entry.object
		if entry.mode == "" {
			entry.mode = "0"
			id = strings.Repeat("0", 40)
		} else if id == "" {
			id = blobs[entry.content]
			if id == "" {
				id = gitCommand(f.t, f.source, entry.content, nil, "hash-object", "-w", "--stdin")
				blobs[entry.content] = id
			}
		}
		fmt.Fprintf(&input, "%s %s\t%s\x00", entry.mode, id, path)
	}
	gitCommand(f.t, f.source, input.String(), env, "update-index", "-z", "--index-info")
	tree := gitCommand(f.t, f.source, "", env, "write-tree")
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	for _, p := range moreParents {
		args = append(args, "-p", p)
	}
	sha := gitCommand(f.t, f.source, message, nil, args...)
	f.push("diff-fixture-"+sha, sha, false)
	return sha
}

func TestRevisionDiffInventory(t *testing.T) {
	f := newFixture(t)
	old := f.diffCommit(f.base, "old", map[string]diffEntry{
		"old.md": {mode: "100644", content: "same\n"}, "deleted": {mode: "100644", content: "gone"},
		"exec": {mode: "100644", content: "echo hi"}, "link": {mode: "120000", content: "old-target"},
		"module": {mode: "160000", object: f.base},
	})
	head := f.diffCommit(old, "head", map[string]diffEntry{
		"old.md": {}, "new.md": {mode: "100644", content: "same\n"}, "deleted": {},
		"exec": {mode: "100755", content: "echo hi"}, "link": {mode: "120000", content: "new-target"},
		"module": {mode: "160000", object: f.prs[0].HeadSHA},
		"binary": {mode: "100644", content: "a\x00b"}, "weird\tname\n": {mode: "100644", content: "odd"},
		".gitattributes": {mode: "100644", content: "* diff\n"},
	})
	req := forge.RevisionDiffRequest{BaseSHA: old, HeadSHA: head}
	result, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	must(t, err)
	if result.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(result.Inventory.Reasons, forge.DiffUnpairedRename) || len(result.Files) != 8 {
		t.Fatalf("%+v", result)
	}
	byPath := map[string]forge.DiffFile{}
	for _, file := range result.Files {
		path := file.NewPath
		if path == "" {
			path = file.OldPath
		}
		byPath[path] = file
	}
	if file := byPath["new.md"]; file.OldPath != "old.md" || file.Rename != forge.DiffExactRename || file.OldObjectID != file.NewObjectID {
		t.Fatalf("rename: %+v", file)
	}
	if file := byPath["deleted"]; file.NewPath != "" || file.NewMode != "" || file.NewObjectID != "" {
		t.Fatalf("delete: %+v", file)
	}
	if file := byPath["exec"]; file.OldMode != "100644" || file.NewMode != "100755" {
		t.Fatalf("mode: %+v", file)
	}
	if file := byPath["link"]; file.OldMode != "120000" || file.NewMode != "120000" {
		t.Fatalf("link: %+v", file)
	}
	if file := byPath["module"]; !file.Submodule || file.NewMode != "160000" || file.NewObjectID != f.prs[0].HeadSHA {
		t.Fatalf("module: %+v", file)
	}
	if file := byPath["binary"]; !file.Binary || !file.BinaryKnown {
		t.Fatalf("binary hidden by attributes: %+v", file)
	}
	if file := byPath["weird\tname\n"]; file.NewPath != "weird\tname\n" {
		t.Fatalf("path: %+v", file)
	}
	for _, p := range []string{"binary", "exec", "link", "module"} {
		file := byPath[p]
		if file.HunkCoverage.Completeness != forge.DiffIncomplete || len(file.Omitted) != 1 || len(file.Hunks) != 0 {
			t.Fatalf("special file hunk coverage: %+v", file)
		}
	}
	if file := byPath["new.md"]; file.HunkCoverage.Completeness != forge.DiffComplete || len(file.Hunks) != 0 {
		t.Fatalf("exact rename content: %+v", file)
	}
	if file := byPath["deleted"]; file.HunkCoverage.Completeness != forge.DiffComplete || len(file.Hunks) != 1 || file.Hunks[0].OldLines != 1 || file.Hunks[0].NewLines != 0 || !strings.Contains(file.Hunks[0].Text, "-gone") {
		t.Fatalf("deleted content: %+v", file)
	}
	// Mutable default/base refs never replace an endpoint supplied by the caller.
	tip := f.diffCommit(old, "base moves", map[string]diffEntry{"new-base.go": {mode: "100644", content: "base"}})
	f.push("main", tip, true)
	repeated, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	must(t, err)
	if !reflect.DeepEqual(result, repeated) {
		t.Fatal("branch movement changed pinned evidence")
	}
}

func TestRevisionDiffRewindReintroducesCode(t *testing.T) {
	f := newFixture(t)
	shared := f.diffCommit(f.base, "shared code", map[string]diffEntry{"shared.go": {mode: "100644", content: "code"}})
	head := f.diffCommit(shared, "docs", map[string]diffEntry{"docs.md": {mode: "100644", content: "docs"}})
	first, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: shared, HeadSHA: head})
	must(t, err)
	rewound, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: head})
	must(t, err)
	if len(first.Files) != 1 || first.Files[0].NewPath != "docs.md" || len(rewound.Files) != 2 || rewound.Files[1].NewPath != "shared.go" {
		t.Fatalf("first=%+v rewound=%+v", first, rewound)
	}
}

func TestRevisionDiffLimitsAndFailures(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name    string
		entries map[string]diffEntry
		reason  forge.DiffFailureReason
	}{
		{"bytes", map[string]diffEntry{"big": {mode: "100644", content: strings.Repeat("x", forge.MaxDiffBytes+1)}}, forge.DiffByteLimit},
		{"files", func() map[string]diffEntry {
			m := map[string]diffEntry{}
			for i := 0; i < forge.MaxDiffFiles+1; i++ {
				m[fmt.Sprintf("file-%04d", i)] = diffEntry{mode: "100644", content: "x"}
			}
			return m
		}(), forge.DiffFileLimit},
		{"raw-bytes", func() map[string]diffEntry {
			m := map[string]diffEntry{}
			for i := 0; i < 600; i++ {
				m[fmt.Sprintf("%04d-", i)+strings.Repeat("p", 4000)] = diffEntry{mode: "100644", content: "x"}
			}
			return m
		}(), forge.DiffByteLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := f.diffCommit(f.base, tc.name, tc.entries)
			result, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: head})
			if err != nil || result.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(result.Inventory.Reasons, tc.reason) || len(result.Files) > forge.MaxDiffFiles {
				t.Fatalf("coverage=%+v files=%d err=%v", result.Inventory, len(result.Files), err)
			}
		})
	}
	missing, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: strings.Repeat("a", 40)})
	if err == nil || !slices.Contains(missing.Inventory.Reasons, forge.DiffMissingObject) {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	orphan := f.diffCommit("", "orphan", map[string]diffEntry{"o": {mode: "100644", content: "orphan"}})
	unrelated, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: orphan})
	if err != nil || !slices.Contains(unrelated.Inventory.Reasons, forge.DiffNoMergeBase) {
		t.Fatalf("unrelated=%+v %v", unrelated, err)
	}
	f.g.cfg.Remote = func(_ context.Context, _ forge.RepoRef) (string, Auth, error) {
		return "", Auth{Password: "secret"}, errors.New("secret unavailable")
	}
	failed, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: orphan})
	if err == nil || strings.Contains(err.Error(), "secret") || !slices.Contains(failed.Inventory.Reasons, forge.DiffReadFailure) {
		t.Fatalf("failure=%+v %v", failed, err)
	}
}

func TestRevisionDiffMultipleMergeBases(t *testing.T) {
	f := newFixture(t)
	a := f.diffCommit(f.base, "a", nil)
	b := f.diffCommit(f.base, "b", nil)
	left := f.diffCommit(a, "left", nil, b)
	right := f.diffCommit(b, "right", nil, a)
	result, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: left, HeadSHA: right})
	if err != nil || result.MergeBaseSHA != "" || result.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(result.Inventory.Reasons, forge.DiffMultipleMergeBases) {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRevisionDiffRejectsTagAndBrokenBlob(t *testing.T) {
	f := newFixture(t)
	gitCommand(t, f.source, "", nil, "tag", "-a", "fixture-tag", f.base, "-m", "tag")
	tag := gitCommand(t, f.source, "", nil, "rev-parse", "fixture-tag")
	gitCommand(t, f.source, "", nil, "push", f.remote, "refs/tags/fixture-tag")
	result, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: tag, HeadSHA: f.base})
	if err == nil || !slices.Contains(result.Inventory.Reasons, forge.DiffInvalidObjectID) {
		t.Fatalf("tag=%+v %v", result, err)
	}
	// Populate a cache, then remove an uncompressed blob to model a failed read.
	req := forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: f.prs[0].HeadSHA}
	good, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	must(t, err)
	s, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	id := good.Files[0].NewObjectID
	// Copy the source's loose objects exactly; hide any fetched packs so the
	// missing blob cannot be recovered from a duplicate packed representation.
	ids := []string{f.base, req.HeadSHA,
		gitCommand(t, f.source, "", nil, "rev-parse", f.base+"^{tree}"),
		gitCommand(t, f.source, "", nil, "rev-parse", req.HeadSHA+"^{tree}")}
	for _, objectID := range ids {
		data, err := os.ReadFile(filepath.Join(f.source, "objects", objectID[:2], objectID[2:]))
		must(t, err)
		dir := filepath.Join(s.dir, "objects", objectID[:2])
		must(t, os.MkdirAll(dir, 0700))
		target := filepath.Join(dir, objectID[2:])
		if _, err := os.Stat(target); os.IsNotExist(err) {
			must(t, os.WriteFile(target, data, 0600))
		}
	}
	blobPath := filepath.Join(s.dir, "objects", id[:2], id[2:])
	if err := os.Remove(blobPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	packs := filepath.Join(s.dir, "objects", "pack")
	must(t, os.Rename(packs, packs+"-hidden"))
	unlock()
	broken, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	if err == nil || broken.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(broken.Inventory.Reasons, forge.DiffReadFailure) {
		t.Fatalf("broken=%+v %v", broken, err)
	}
}

// Astra's C -> S -> K, C -> D, B = merge(K,D), S -> H graph.
// H reverts code.go and adds docs.md: truncating at K falsely makes C the base
// and hides code.go, even though both endpoints and S exist in the cache.
func TestRevisionDiffShallowHistory(t *testing.T) {
	f := newFixture(t)
	c := f.diffCommit(f.base, "C", map[string]diffEntry{"code.go": {mode: "100644", content: "original\n"}})
	s := f.diffCommit(c, "S", map[string]diffEntry{"code.go": {mode: "100644", content: "changed\n"}})
	k := f.diffCommit(s, "K", nil)
	d := f.diffCommit(c, "D", nil)
	b := f.diffCommit(k, "B", nil, d)
	h := f.diffCommit(s, "H", map[string]diffEntry{"code.go": {mode: "100644", content: "original\n"}, "docs.md": {mode: "100644", content: "docs\n"}})
	req := forge.RevisionDiffRequest{BaseSHA: b, HeadSHA: h}
	full, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	must(t, err)
	if full.Inventory.Completeness != forge.DiffComplete || full.MergeBaseSHA != s || len(full.Files) != 2 || full.Files[0].NewPath != "code.go" || full.Files[1].NewPath != "docs.md" {
		t.Fatalf("full history: %+v", full)
	}
	for _, mode := range []string{"existing-objects", "ordinary-fetch", "cache-maintenance"} {
		t.Run(mode, func(t *testing.T) {
			r := f.client(t, 0)
			r.cfg.GCInterval, r.cfg.GCThresholdBytes = -1, -1
			session, unlock, err := r.open(t.Context(), testRepo)
			must(t, err)
			dir := session.dir
			if mode == "existing-objects" {
				must(t, session.commits(t.Context(), b, h))
				must(t, os.WriteFile(filepath.Join(dir, "shallow"), []byte(k+"\n"), 0600))
			} else {
				gitCommand(t, dir, "", nil, "fetch", "--no-write-fetch-head", "--depth=1", "file://"+f.remote, k)
			}
			if mode == "cache-maintenance" {
				// Retain the endpoints across real GC. A normal fetch does not unshallow
				// K, and GC must not make the next read trust the truncated graph.
				must(t, session.commits(t.Context(), b, h))
				gitCommand(t, dir, "", nil, "update-ref", "refs/heads/pinned-base", b)
				gitCommand(t, dir, "", nil, "update-ref", "refs/heads/pinned-head", h)
				must(t, os.WriteFile(filepath.Join(dir, "openmerge-gc"), nil, 0600))
				r.cfg.GCInterval = time.Nanosecond
			}
			unlock()
			for i := 0; i < 2; i++ {
				got, err := r.RevisionDiff(t.Context(), testRepo, req)
				if err != nil || got.MergeBaseSHA != "" || len(got.Files) != 0 || got.Inventory.Completeness != forge.DiffIncomplete || !reflect.DeepEqual(got.Inventory.Reasons, []forge.DiffFailureReason{forge.DiffShallowHistory}) {
					t.Fatalf("read %d: %+v err=%v", i, got, err)
				}
				if got := gitCommand(t, dir, "", nil, "rev-parse", "--is-shallow-repository"); got != "true" {
					t.Fatalf("fixture lost shallow boundary: %q", got)
				}
			}
			if got := gitCommand(t, dir, "", nil, "merge-base", b, h); got != c {
				t.Fatalf("fixture must reproduce the falsely docs-only base C: %q", got)
			}
			if mode == "cache-maintenance" {
				packs, err := filepath.Glob(filepath.Join(dir, "objects", "pack", "*.pack"))
				must(t, err)
				if len(packs) == 0 {
					t.Fatal("cache maintenance did not pack retained history")
				}
			}
		})
	}
}

func TestRevisionDiffReviewBoundaries(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name     string
		entries  map[string]diffEntry
		complete forge.DiffCompleteness
		binary   bool
	}{
		{"exact-1000-files", func() map[string]diffEntry {
			entries := map[string]diffEntry{}
			for i := 0; i < 1000; i++ {
				entries[fmt.Sprintf("doc-%04d.md", i)] = diffEntry{mode: "100644", content: "x"}
			}
			return entries
		}(), forge.DiffComplete, false},
		// A SHA-1 raw record costs 100 bytes plus the 8-byte path.
		{"exact-2MiB", map[string]diffEntry{"limit.md": {mode: "100644", content: strings.Repeat("x", forge.MaxDiffBytes-108)}}, forge.DiffComplete, false},
		{"over-2MiB-by-one", map[string]diffEntry{"limit.md": {mode: "100644", content: strings.Repeat("x", forge.MaxDiffBytes-107)}}, forge.DiffIncomplete, false},
		{"late-nul", map[string]diffEntry{"late.md": {mode: "100644", content: strings.Repeat("x", 9000) + "\x00"}}, forge.DiffComplete, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := f.diffCommit(f.base, tc.name, tc.entries)
			got, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: head})
			if err != nil || got.Inventory.Completeness != tc.complete || len(got.Files) != len(tc.entries) {
				t.Fatalf("coverage=%+v files=%d err=%v", got.Inventory, len(got.Files), err)
			}
			if tc.complete == forge.DiffIncomplete {
				if !slices.Contains(got.Inventory.Reasons, forge.DiffByteLimit) || got.Files[0].BinaryKnown {
					t.Fatalf("byte limit: %+v", got)
				}
			} else {
				for _, file := range got.Files {
					if !file.BinaryKnown || file.Binary != tc.binary {
						t.Fatalf("binary evidence: %+v", file)
					}
				}
			}
		})
	}
}

func TestRevisionDiffIgnoresHostRenameAndSubmoduleSettings(t *testing.T) {
	f := newFixture(t)
	before, after := map[string]diffEntry{}, map[string]diffEntry{}
	for i := 0; i < 3; i++ {
		before[fmt.Sprintf("old-%d.md", i)] = diffEntry{mode: "100644", content: fmt.Sprint(i)}
		after[fmt.Sprintf("old-%d.md", i)] = diffEntry{}
		after[fmt.Sprintf("new-%d.md", i)] = diffEntry{mode: "100644", content: fmt.Sprint(i)}
	}
	before["module"] = diffEntry{mode: "160000", object: f.base}
	after["module"] = diffEntry{mode: "160000", object: f.prs[0].HeadSHA}
	base := f.diffCommit(f.base, "rename-base", before)
	head := f.diffCommit(base, "rename-head", after)
	req := forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: head}
	want, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	must(t, err)
	if want.Inventory.Completeness != forge.DiffComplete || len(want.Files) != 4 || !want.Files[0].Submodule {
		t.Fatalf("%+v", want)
	}
	for _, file := range want.Files[1:] {
		if file.Rename != forge.DiffExactRename {
			t.Fatalf("%+v", file)
		}
	}
	session, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	_, err = session.command(t.Context(), "", "config", "diff.renameLimit", "1")
	must(t, err)
	_, err = session.command(t.Context(), "", "config", "diff.ignoreSubmodules", "all")
	must(t, err)
	unlock()
	got, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("host settings changed evidence: %+v err=%v", got, err)
	}
}

func TestRevisionDiffUnpairedRenamesIncomplete(t *testing.T) {
	f := newFixture(t)
	old := f.diffCommit(f.base, "old", map[string]diffEntry{"old.md": {mode: "100644", content: "same"}})
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			entries := map[string]diffEntry{"old.md": {}, "new.md": {mode: "100644", content: "different"}}
			if ambiguous {
				entries["new.md"] = diffEntry{mode: "100644", content: "same"}
				entries["copy.md"] = diffEntry{mode: "100644", content: "same"}
			}
			head := f.diffCommit(old, "uncertain rename", entries)
			got, err := f.g.RevisionDiff(t.Context(), testRepo, forge.RevisionDiffRequest{BaseSHA: old, HeadSHA: head})
			if err != nil || got.Inventory.Completeness != forge.DiffIncomplete || !reflect.DeepEqual(got.Inventory.Reasons, []forge.DiffFailureReason{forge.DiffUnpairedRename}) || len(got.Files) != len(entries) {
				t.Fatalf("%+v err=%v", got, err)
			}
		})
	}
}
