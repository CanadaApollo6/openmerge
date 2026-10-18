package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestUnifiedHunkBehaviorGolden(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		want            DiffHunk
	}{
		{"replacement", "a\nb\nc\n", "a\nx\nc\n", DiffHunk{1, 3, 1, 3, " a\n-b\n+x\n c\n"}},
		{"insertion", "a\n", "a\nb\n", DiffHunk{1, 1, 1, 2, " a\n+b\n"}},
		{"deletion", "a\nb\n", "a\n", DiffHunk{1, 2, 1, 1, " a\n-b\n"}},
		{"no-newline", "a", "b", DiffHunk{1, 1, 1, 1, "-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"}},
		{"interior-context", "a\nsame\nb\n", "x\nsame\ny\n", DiffHunk{1, 3, 1, 3, "-a\n+x\n same\n-b\n+y\n"}},
		{"crlf", "a\r\nb\r\n", "a\r\nc\r\n", DiffHunk{1, 2, 1, 2, " a\r\n-b\r\n+c\r\n"}},
		{"crlf-to-lf", "a\r\n", "a\n", DiffHunk{1, 1, 1, 1, "-a\r\n+a\n"}},
		{"lf-to-crlf", "a\n", "a\r\n", DiffHunk{1, 1, 1, 1, "-a\n+a\r\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
			if err := CollectDiffHunks(t.Context(), &result, map[string]string{"a": tc.old, "b": tc.next}); err != nil {
				t.Fatal(err)
			}
			f := result.Files[0]
			if result.Hunks.Completeness != DiffComplete || f.HunkCoverage.Completeness != DiffComplete || len(f.Hunks) != 1 || f.Hunks[0] != tc.want {
				t.Fatalf("%+v %+v", result, f)
			}
		})
	}
}

func TestHunkLimitsAndSpecialFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   DiffFile
		text   string
		reason DiffFailureReason
	}{
		{"binary", DiffFile{Binary: true, BinaryKnown: true}, "", DiffNonText},
		{"unknown-binary", DiffFile{}, "", DiffNonText},
		{"symlink", DiffFile{NewMode: "120000", BinaryKnown: true}, "target", DiffSpecialMode},
		{"gitlink-mode", DiffFile{NewMode: "160000"}, "", DiffSpecialMode},
		{"submodule", DiffFile{Submodule: true}, "", DiffSpecialMode},
		{"mode", DiffFile{OldMode: "100644", NewMode: "100755", BinaryKnown: true}, "", DiffSpecialMode},
		{"encoding", DiffFile{NewObjectID: "b", BinaryKnown: true}, "\xff", DiffInvalidUTF8},
		{"bytes", DiffFile{NewObjectID: "b", BinaryKnown: true}, strings.Repeat("x", MaxDiffHunkInputBytes+1), DiffHunkWorkLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{tc.file}}
			if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": tc.text}); err != nil {
				t.Fatal(err)
			}
			f := result.Files[0]
			if result.Hunks.Completeness != DiffIncomplete || !slices.Contains(result.Hunks.Reasons, tc.reason) || len(f.Hunks) != 0 || len(f.Omitted) != 1 || f.Omitted[0].Reason != tc.reason {
				t.Fatalf("%+v %+v", result, f)
			}
		})
	}
	result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}}
	for i := 0; i < MaxDiffFiles; i++ {
		result.Files = append(result.Files, DiffFile{NewPath: fmt.Sprintf("%04d", i), NewObjectID: "b", BinaryKnown: true})
	}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": "x\n"}); err != nil {
		t.Fatal(err)
	}
	if len(result.Files[MaxDiffFiles-1].Hunks) != 1 || result.Hunks.Completeness != DiffComplete {
		t.Fatal(result.Hunks)
	}
	// Exact byte budget includes the line marker, newline and all content.
	result.Files = []DiffFile{{NewObjectID: "b", BinaryKnown: true}}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": strings.Repeat("x", MaxDiffHunkBytes-2) + "\n"}); err != nil {
		t.Fatal(err)
	}
	if result.Hunks.Completeness != DiffComplete || len(result.Files[0].Hunks[0].Text) != MaxDiffHunkBytes {
		t.Fatal(result.Hunks)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CollectDiffHunks(ctx, &result, nil); err == nil || !slices.Contains(result.Hunks.Reasons, DiffReadFailure) {
		t.Fatal(result.Hunks, err)
	}
}

func TestSeparatedHunkGolden(t *testing.T) {
	for _, n := range []int{300, 70000} {
		r := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
		err := CollectDiffHunks(t.Context(), &r, map[string]string{"a": "a\n" + strings.Repeat("same\n", n) + "b\n", "b": "x\n" + strings.Repeat("same\n", n) + "y\n"})
		want := []DiffHunk{{1, 4, 1, 4, "-a\n+x\n same\n same\n same\n"}, {n - 1, 4, n - 1, 4, " same\n same\n same\n-b\n+y\n"}}
		if err != nil || !reflect.DeepEqual(r.Files[0].Hunks, want) || r.Hunks.Completeness != DiffComplete {
			t.Fatalf("n=%d: %+v %v", n, r, err)
		}
	}
}

func TestUnifiedRetentionBounds(t *testing.T) {
	collect := func(files []DiffFile, blobs map[string]string) RevisionDiff {
		t.Helper()
		r := RevisionDiff{Files: files, Inventory: DiffCoverage{Completeness: DiffComplete}}
		if err := CollectDiffHunks(t.Context(), &r, blobs); err != nil {
			t.Fatal(err)
		}
		return r
	}
	file := DiffFile{NewObjectID: "b", BinaryKnown: true}
	for _, n := range []int{200, 201} {
		r := collect([]DiffFile{file}, map[string]string{"b": strings.Repeat("x\n", n)})
		if n == 200 {
			if r.Hunks.Completeness != DiffComplete {
				t.Fatal(r)
			}
		} else {
			want := DiffHunkRange{0, 0, 1, 201, DiffHunkLineLimit, 0}
			if len(r.Files[0].Hunks) != 0 || !reflect.DeepEqual(r.Files[0].Omitted, []DiffHunkRange{want}) {
				t.Fatal(r)
			}
		}
	}
	r := collect([]DiffFile{file}, map[string]string{"b": strings.Repeat("x", 300001) + "\n"})
	if r.Hunks.Completeness != DiffComplete {
		t.Fatal(r)
	}
	// A large first hunk is omitted whole; the second tiny region still fits.
	a := strings.Repeat("a\n", 201) + strings.Repeat("same\n", 20) + "z\n"
	b := strings.Repeat("b\n", 201) + strings.Repeat("same\n", 20) + "y\n"
	r = collect([]DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}, map[string]string{"a": a, "b": b})
	if len(r.Files[0].Omitted) != 1 || r.Files[0].Omitted[0] != (DiffHunkRange{1, 204, 1, 204, DiffHunkLineLimit, 0}) || len(r.Files[0].Hunks) != 1 || r.Files[0].Hunks[0] != (DiffHunk{219, 4, 219, 4, " same\n same\n same\n-z\n+y\n"}) {
		t.Fatal(r)
	}
	// Byte retention is shared across files, independently of per-file input work.
	r = collect([]DiffFile{{NewPath: "z", NewObjectID: "b", BinaryKnown: true}, {NewPath: "a", NewObjectID: "b", BinaryKnown: true}}, map[string]string{"b": strings.Repeat("x", 1100000) + "\n"})
	if r.Files[0].NewPath != "a" || len(r.Files[0].Hunks) != 1 || r.Files[1].Omitted[0].Reason != DiffHunkByteLimit {
		t.Fatal(r)
	}
}

func TestUnifiedIsolationAndDeadline(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.external")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_CONFIG_KEY_1", "diff.ignoreSubmodules")
	t.Setenv("GIT_CONFIG_VALUE_1", "all")
	h, err := unifiedHunks(t.Context(), "a\n", "b\n")
	if err != nil || !reflect.DeepEqual(h, []DiffHunk{{1, 1, 1, 1, "-a\n+b\n"}}) {
		t.Fatal(h, err)
	}
	// This process has no children; CommandContext kills only our busy shell.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	r := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
	if err := CollectDiffHunks(t.Context(), &r, map[string]string{"a": "a\n", "b": "b\n"}); err != nil || r.Hunks.Completeness != DiffIncomplete || !slices.Contains(r.Hunks.Reasons, DiffHunkWorkLimit) {
		t.Fatal(r, err)
	}
}

func TestOmittedRangeBudgets(t *testing.T) {
	// Muse's probe: 160,000 short lines, with a change every eighth line.
	var old, next strings.Builder
	for i := 0; i < 160000; i++ {
		line := fmt.Sprintf("%04x\n", i%65536)
		old.WriteString(line)
		if i%8 == 0 {
			next.WriteString("edit\n")
		} else {
			next.WriteString(line)
		}
	}
	files := make([]DiffFile, 10)
	for i := range files {
		files[i] = DiffFile{NewPath: fmt.Sprintf("%02d", i), OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}
	}
	r := RevisionDiff{Files: files, Inventory: DiffCoverage{Completeness: DiffComplete}}
	if err := CollectDiffHunks(t.Context(), &r, map[string]string{"a": old.String(), "b": next.String()}); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, f := range r.Files {
		wantDetails := min(MaxDiffOmittedRangesPerFile, max(0, MaxDiffOmittedRanges-i*MaxDiffOmittedRangesPerFile))
		if len(f.Hunks) != 25 || len(f.Omitted) != wantDetails+1 || f.HunkCoverage.Completeness != DiffIncomplete {
			t.Fatalf("file %d: retained=%d omitted=%d coverage=%+v", i, len(f.Hunks), len(f.Omitted), f.HunkCoverage)
		}
		summary := f.Omitted[len(f.Omitted)-1]
		if summary.Reason != DiffOmittedRangeLimit || summary.Count != 19975-wantDetails || summary.OldStart != (25+wantDetails)*8-2 || summary.NewStart != summary.OldStart || summary.OldStart+summary.OldLines != 159997 || summary.NewLines != summary.OldLines {
			t.Fatalf("file %d: summary=%+v", i, summary)
		}
		total += len(f.Omitted)
	}
	if total != MaxDiffOmittedRanges+len(files) || !slices.Contains(r.Hunks.Reasons, DiffOmittedRangeLimit) || r.Inventory.Completeness != DiffComplete {
		t.Fatalf("omissions=%d coverage=%+v inventory=%+v", total, r.Hunks, r.Inventory)
	}
}

func TestUnifiedCacheAndStderr(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "missing", "cache"))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A warning after the patch must not become a patch body line.
	script := "#!/bin/sh\n\"" + realGit + "\" \"$@\"\nstatus=$?\nprintf 'warning: host diagnostic\\n' >&2\nexit $status\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	h, err := unifiedHunks(t.Context(), "a\n", "b\n")
	if err != nil || !reflect.DeepEqual(h, []DiffHunk{{1, 1, 1, 1, "-a\n+b\n"}}) {
		t.Fatal(h, err)
	}
	// Overflowing stderr must fail closed even when Git returns its normal
	// "differences found" exit status and stdout contains a valid patch.
	script = strings.Replace(script, "host diagnostic", strings.Repeat("w", MaxDiffHunkStderrBytes+1), 1)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := unifiedHunks(t.Context(), "a\n", "b\n"); err == nil {
		t.Fatal("stderr overflow accepted")
	}
}

func TestHunkEvidenceJSONEnvelope(t *testing.T) {
	// Over-approximate all permitted metadata simultaneously, with maximum
	// coordinates and worst-case escaping, independently of Git's hunk layout.
	type evidence struct {
		Hunks    []DiffHunk
		Omitted  []DiffHunkRange
		Coverage DiffCoverage
	}
	files := make([]evidence, MaxDiffFiles)
	escapedBytes, retained := 0, 0
	for i := range files {
		files[i].Coverage = DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{DiffHunkLineLimit, DiffHunkByteLimit, DiffOmittedRangeLimit}}
		for j := 0; j < MaxDiffHunkLinesPerFile && retained < MaxDiffHunks; j++ {
			files[i].Hunks = append(files[i].Hunks, DiffHunk{MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, "\x01\n"})
			escapedBytes += 8
			retained++
		}
		for j := 0; j < min(MaxDiffOmittedRangesPerFile, max(0, MaxDiffOmittedRanges-i*MaxDiffOmittedRangesPerFile)); j++ {
			files[i].Omitted = append(files[i].Omitted, DiffHunkRange{MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, DiffHunkByteLimit, 0})
		}
		files[i].Omitted = append(files[i].Omitted, DiffHunkRange{MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, MaxDiffHunkInputBytes, DiffOmittedRangeLimit, MaxDiffHunkInputBytes})
	}
	files[0].Hunks[0].Text = strings.Repeat("x", MaxDiffHunkEscapedBytes-escapedBytes) + files[0].Hunks[0].Text
	wire, err := json.Marshal(files)
	if err != nil || len(wire) >= 5_865_000 {
		t.Fatalf("hunk evidence JSON bytes=%d err=%v", len(wire), err)
	}
	t.Logf("over-approximated compact hunk evidence: %d bytes (<5.865 MB)", len(wire))
}

func TestRetainedHunkCountBudgetAndSummary(t *testing.T) {
	dir := t.TempDir()
	var patch strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&patch, "@@ -%d,0 +%d @@\n+x\n", i, i)
	}
	// Exercise retention deterministically; unified algorithm has separate real-Git goldens.
	script := "#!/bin/sh\ncat <<'PATCH'\n" + patch.String() + "PATCH\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}}
	for i := 0; i < 24; i++ {
		result.Files = append(result.Files, DiffFile{NewPath: fmt.Sprintf("%03d", i), NewMode: "100644", NewObjectID: "new", BinaryKnown: true})
	}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"new": strings.Repeat("x\n", 200)}); err != nil {
		t.Fatal(err)
	}
	total, collapsed := 0, 0
	for _, f := range result.Files {
		total += len(f.Hunks)
		for _, r := range f.Omitted {
			collapsed += r.Count
		}
	}
	if total != MaxDiffHunks || collapsed == 0 || !containsReason(result.Hunks.Reasons, DiffHunkCountLimit) || !containsReason(result.Hunks.Reasons, DiffOmittedRangeLimit) {
		t.Fatalf("hunks=%d collapsed=%d coverage=%+v", total, collapsed, result.Hunks)
	}
}
func TestEscapedTextBudget(t *testing.T) {
	for _, n := range []int{699050, 699051} {
		result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{NewPath: "a", NewMode: "100644", NewObjectID: "new", BinaryKnown: true}}}
		if err := CollectDiffHunks(t.Context(), &result, map[string]string{"new": strings.Repeat("\x01", n) + "\n"}); err != nil {
			t.Fatal(err)
		}
		if n == 699050 && len(result.Files[0].Hunks) != 1 {
			t.Fatal("within escaped budget omitted")
		}
		if n == 699051 && (len(result.Files[0].Hunks) != 0 || !containsReason(result.Hunks.Reasons, DiffHunkByteLimit)) {
			t.Fatal("escaped byte budget exceeded")
		}
	}
}
