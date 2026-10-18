package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CollectDiffHunks computes isolated, bounded Git unified patches from inspected blobs.
func CollectDiffHunks(ctx context.Context, result *RevisionDiff, blobs map[string]string) error {
	result.Hunks = DiffCoverage{Completeness: DiffComplete}
	work, cancel := context.WithTimeout(ctx, MaxDiffHunksDuration)
	defer cancel()
	remaining := MaxDiffHunkBytes
	slices.SortStableFunc(result.Files, func(a, b DiffFile) int {
		key := func(f DiffFile) string {
			if f.NewPath != "" {
				return f.NewPath
			}
			return f.OldPath
		}
		return strings.Compare(key(a), key(b))
	})
	for i := range result.Files {
		if err := ctx.Err(); err != nil {
			result.Hunks = DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{DiffReadFailure}}
			return err
		}
		f := &result.Files[i]
		f.Hunks, f.Omitted = nil, nil
		f.HunkCoverage = DiffCoverage{Completeness: DiffComplete}
		omit := func(reason DiffFailureReason, span DiffHunkRange) {
			span.Reason = reason
			f.Omitted = append(f.Omitted, span)
			f.HunkCoverage.Completeness = DiffIncomplete
			if !containsReason(f.HunkCoverage.Reasons, reason) {
				f.HunkCoverage.Reasons = append(f.HunkCoverage.Reasons, reason)
			}
			result.Hunks.Completeness = DiffIncomplete
			if !containsReason(result.Hunks.Reasons, reason) {
				result.Hunks.Reasons = append(result.Hunks.Reasons, reason)
			}
		}
		if f.Submodule || f.OldMode == "120000" || f.NewMode == "120000" || f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode {
			omit(DiffSpecialMode, DiffHunkRange{})
			continue
		}
		if !f.BinaryKnown || f.Binary {
			omit(DiffNonText, DiffHunkRange{})
			continue
		}
		a, aok := blobs[f.OldObjectID]
		b, bok := blobs[f.NewObjectID]
		if f.OldObjectID != "" && !aok || f.NewObjectID != "" && !bok {
			omit(DiffReadFailure, DiffHunkRange{})
			return errors.New("inspected hunk blob is missing")
		}
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			omit(DiffInvalidUTF8, DiffHunkRange{})
			continue
		}
		if a == b {
			continue
		}
		if i >= MaxDiffFiles {
			omit(DiffFileLimit, DiffHunkRange{})
			continue
		}
		if len(a)+len(b) > MaxDiffHunkInputBytes || work.Err() != nil {
			omit(DiffHunkWorkLimit, DiffHunkRange{})
			continue
		}
		hunks, err := unifiedHunks(work, a, b)
		if err != nil {
			if ctx.Err() != nil {
				omit(DiffReadFailure, DiffHunkRange{})
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				omit(DiffHunkWorkLimit, DiffHunkRange{})
				continue
			}
			omit(DiffReadFailure, DiffHunkRange{})
			return err
		}
		lines := 0
		for _, h := range hunks {
			span := DiffHunkRange{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines}
			bodyLines := len(hunkLines(h.Text))
			if lines+bodyLines > MaxDiffHunkLinesPerFile {
				omit(DiffHunkLineLimit, span)
				continue
			}
			if len(h.Text) > remaining {
				omit(DiffHunkByteLimit, span)
				continue
			}
			f.Hunks = append(f.Hunks, h)
			remaining -= len(h.Text)
			lines += bodyLines
		}
	}
	if err := ctx.Err(); err != nil {
		result.Hunks = DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{DiffReadFailure}}
		return err
	}
	if result.Inventory.Completeness != DiffComplete {
		result.Hunks.Completeness = DiffIncomplete
		result.Hunks.Reasons = append(result.Hunks.Reasons, DiffInventoryIncomplete)
	}
	return nil
}

func hunkLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func containsReason(reasons []DiffFailureReason, reason DiffFailureReason) bool {
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// No repository is discovered: neutral filenames in a private directory, an
// explicit ceiling, and a fresh environment exclude attributes and caller config.
func unifiedHunks(ctx context.Context, old, next string) ([]DiffHunk, error) {
	ctx, cancel := context.WithTimeout(ctx, MaxDiffHunkDuration)
	defer cancel()
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(cache, "openmerge-hunks-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, content := range map[string]string{"old": old, "new": next} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "diff.algorithm=myers", "-c", "diff.indentHeuristic=false", "-c", "core.attributesFile=/dev/null", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--diff-algorithm=myers", "--no-indent-heuristic", "--minimal", "--inter-hunk-context=0", fmt.Sprintf("--unified=%d", DiffHunkContext), "--", "old", "new")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}
	// Each input byte can add a prefix, plus newline markers and headers.
	output := &hunkOutput{remaining: MaxDiffHunkOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.overflow {
		return nil, context.DeadlineExceeded
	}
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("unified diff: %w", err)
	}
	return parseUnifiedHunks(output.String())
}

type hunkOutput struct {
	strings.Builder
	remaining int
	overflow  bool
}

func (b *hunkOutput) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.overflow = true
		return 0, errors.New("patch output limit")
	}
	b.remaining -= len(p)
	return b.Builder.Write(p)
}

var unifiedHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedHunks(patch string) ([]DiffHunk, error) {
	var hunks []DiffHunk
	var body strings.Builder
	flush := func() {
		if len(hunks) > 0 {
			hunks[len(hunks)-1].Text = body.String()
			body.Reset()
		}
	}
	for _, line := range hunkLines(patch) {
		if strings.HasPrefix(line, "@@") {
			flush()
			m := unifiedHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, errors.New("invalid unified header")
			}
			values := [4]int{0, 1, 0, 1}
			for i := range values {
				if m[i+1] != "" {
					values[i], _ = strconv.Atoi(m[i+1])
				}
			}
			hunks = append(hunks, DiffHunk{OldStart: values[0], OldLines: values[1], NewStart: values[2], NewLines: values[3]})
		} else if len(hunks) > 0 {
			if !strings.ContainsRune(" +-\\", rune(line[0])) {
				return nil, errors.New("invalid unified body")
			}
			body.WriteString(line)
		}
	}
	flush()
	return hunks, nil
}
