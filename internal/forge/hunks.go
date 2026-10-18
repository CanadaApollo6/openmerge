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

// DiffHunkAlgorithmFlags is pinned by the semantics golden and used verbatim.
const DiffHunkAlgorithmFlags = "--diff-algorithm=myers --minimal --no-indent-heuristic --inter-hunk-context=0 --unified=3"

// CollectDiffHunks computes isolated, bounded Git unified patches from inspected blobs.
func CollectDiffHunks(ctx context.Context, result *RevisionDiff, blobs map[string]string) error {
	result.Hunks = DiffCoverage{Completeness: DiffComplete}
	work, cancel := context.WithTimeout(ctx, MaxDiffHunksDuration)
	defer cancel()
	remaining := MaxDiffHunkBytes
	omitted := 0
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
			if len(f.Omitted) < MaxDiffOmittedRangesPerFile && omitted < MaxDiffOmittedRanges {
				f.Omitted = append(f.Omitted, span)
				omitted++
			} else {
				if len(f.Omitted) == 0 || f.Omitted[len(f.Omitted)-1].Reason != DiffOmittedRangeLimit {
					f.Omitted = append(f.Omitted, DiffHunkRange{Reason: DiffOmittedRangeLimit})
				}
				summary := &f.Omitted[len(f.Omitted)-1]
				if summary.Count == 0 {
					summary.OldStart, summary.OldLines = span.OldStart, span.OldLines
					summary.NewStart, summary.NewLines = span.NewStart, span.NewLines
				} else {
					summary.OldLines = max(summary.OldStart+summary.OldLines, span.OldStart+span.OldLines) - summary.OldStart
					summary.NewLines = max(summary.NewStart+summary.NewLines, span.NewStart+span.NewLines) - summary.NewStart
				}
				summary.Count++
				f.HunkCoverage.Reasons = appendUniqueReason(f.HunkCoverage.Reasons, DiffOmittedRangeLimit)
				result.Hunks.Reasons = appendUniqueReason(result.Hunks.Reasons, DiffOmittedRangeLimit)
			}
			f.HunkCoverage.Completeness = DiffIncomplete
			if !containsReason(f.HunkCoverage.Reasons, reason) {
				f.HunkCoverage.Reasons = append(f.HunkCoverage.Reasons, reason)
			}
			result.Hunks.Completeness = DiffIncomplete
			if !containsReason(result.Hunks.Reasons, reason) {
				result.Hunks.Reasons = append(result.Hunks.Reasons, reason)
			}
		}
		if f.Submodule || f.OldMode == "160000" || f.NewMode == "160000" || f.OldMode == "120000" || f.NewMode == "120000" || f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode {
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

func appendUniqueReason(reasons []DiffFailureReason, reason DiffFailureReason) []DiffFailureReason {
	if !containsReason(reasons, reason) {
		return append(reasons, reason)
	}
	return reasons
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
	if err := os.MkdirAll(cache, 0700); err != nil {
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
	args := []string{"-c", "core.attributesFile=/dev/null", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames"}
	args = append(args, strings.Fields(DiffHunkAlgorithmFlags)...)
	args = append(args, "--", "old", "new")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}
	// Each input byte can add a prefix, plus newline markers and headers.
	output := &hunkOutput{remaining: MaxDiffHunkOutputBytes}
	diagnostic := &hunkOutput{remaining: MaxDiffHunkStderrBytes}
	cmd.Stdout, cmd.Stderr = output, diagnostic
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.overflow {
		return nil, context.DeadlineExceeded
	}
	if diagnostic.overflow {
		return nil, errors.New("unified diff diagnostic limit")
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
