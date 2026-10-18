package forge

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// CollectDiffHunks consumes already bounded, inspected blobs. A single span
// excludes equal prefix/suffix lines; internal equal lines are replaced too.
// This is linear work, with no heuristic matching or quadratic edit matrix.
// Call only after inventory blob inspection finishes. Missing blobs fail closed.
func CollectDiffHunks(ctx context.Context, result *RevisionDiff, blobs map[string]string) error {
	result.Hunks = DiffCoverage{Completeness: DiffComplete}
	count, remaining := 0, MaxDiffHunkBytes
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
			f.Omitted = []DiffHunkRange{span}
			f.HunkCoverage = DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{reason}}
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
		old, next := hunkLines(a), hunkLines(b)
		prefix := 0
		for prefix < len(old) && prefix < len(next) && old[prefix] == next[prefix] {
			prefix++
		}
		endA, endB := len(old), len(next)
		for endA > prefix && endB > prefix && old[endA-1] == next[endB-1] {
			endA--
			endB--
		}
		span := DiffHunkRange{OldStart: prefix + 1, OldLines: endA - prefix, NewStart: prefix + 1, NewLines: endB - prefix}
		if span.OldLines == 0 {
			span.OldStart--
		}
		if span.NewLines == 0 {
			span.NewStart--
		}
		if count == MaxDiffHunks {
			omit(DiffHunkLimit, span)
			continue
		}
		size := 0
		for _, lines := range [][]string{old[prefix:endA], next[prefix:endB]} {
			for _, line := range lines {
				size += 1 + len(line)
				if !strings.HasSuffix(line, "\n") {
					size += len("\n\\ No newline at end of file\n")
				}
			}
		}
		if size > remaining {
			omit(DiffHunkByteLimit, span)
			continue
		}
		var text strings.Builder
		text.Grow(size)
		for j, lines := range [][]string{old[prefix:endA], next[prefix:endB]} {
			for _, line := range lines {
				text.WriteByte("-+"[j])
				text.WriteString(line)
				if !strings.HasSuffix(line, "\n") {
					text.WriteString("\n\\ No newline at end of file\n")
				}
			}
		}
		f.Hunks = []DiffHunk{{OldStart: span.OldStart, OldLines: span.OldLines, NewStart: span.NewStart, NewLines: span.NewLines, Text: text.String()}}
		remaining -= size
		count++
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
