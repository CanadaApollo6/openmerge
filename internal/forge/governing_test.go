package forge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestGoverningSelectionAndBounds(t *testing.T) {
	for _, selected := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ""} {
		t.Run(selected, func(t *testing.T) {
			calls := []string{}
			got, err := CollectGoverningFiles(t.Context(), GoverningFilesRequest{CommitSHA: strings.Repeat("b", 40), PathsComplete: true}, func(_ context.Context, p string, _ int) (GoverningFile, error) {
				calls = append(calls, p)
				return GoverningFile{Present: p == selected, Coverage: DiffCoverage{Completeness: DiffComplete}}, nil
			})
			if err != nil || got.SelectedCodeowners != selected || got.Coverage.Completeness != DiffComplete {
				t.Fatal(got, err)
			}
			if selected != "" && slices.Index(calls, selected) != len(calls)-2 {
				t.Fatal(calls)
			}
			if selected == "" && len(got.Codeowners) != 3 {
				t.Fatal(got)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		paths   []string
		content string
		reason  DiffFailureReason
	}{
		{"deep", []string{strings.Repeat("d/", MaxGoverningFiles) + "f"}, "", DiffFileLimit},
		{"probes", func() []string {
			p := []string{}
			for i := 0; i < MaxGoverningFiles; i++ {
				p = append(p, fmt.Sprintf("d%d/f", i))
			}
			return p
		}(), "", DiffFileLimit},
		{"expanded-path-bytes", []string{strings.Repeat("p", MaxDiffBytes/2) + "/a/b/c/file"}, "", DiffByteLimit},
		{"input-bytes", []string{strings.Repeat("p", MaxDiffBytes+1)}, "", DiffByteLimit},
		{"invalid-utf8-path", []string{"\xff"}, "", DiffInvalidUTF8},
		{"invalid-path", []string{"../f"}, "", DiffReadFailure},
		{"bytes", nil, strings.Repeat("x", MaxGoverningBytes+1), DiffByteLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := CollectGoverningFiles(t.Context(), GoverningFilesRequest{CommitSHA: strings.Repeat("b", 40), Paths: tc.paths, PathsComplete: true}, func(_ context.Context, p string, budget int) (GoverningFile, error) {
				calls++
				f := GoverningFile{Path: p, Coverage: DiffCoverage{Completeness: DiffComplete}}
				if len(tc.content) > budget {
					f.Present = true
					f.Coverage = DiffCoverage{Completeness: DiffIncomplete, Reasons: []DiffFailureReason{DiffByteLimit}}
				}
				return f, nil
			})
			if (err != nil) != (tc.reason == DiffReadFailure) || got.Coverage.Completeness != DiffIncomplete || !slices.Contains(got.Coverage.Reasons, tc.reason) || calls > MaxGoverningFiles {
				t.Fatal(got.Coverage, err, calls)
			}
		})
	}
	_, err := CollectGoverningFiles(t.Context(), GoverningFilesRequest{CommitSHA: strings.Repeat("b", 40)}, func(context.Context, string, int) (GoverningFile, error) {
		return GoverningFile{}, errors.New("transport failed")
	})
	if err == nil {
		t.Fatal("lost read error")
	}
}

func TestGoverningExactProbeBudget(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			// Four config + three absent owners + root attributes + 248 directories.
			paths := []string{}
			for i := 0; i < MaxGoverningFiles-8+extra; i++ {
				paths = append(paths, fmt.Sprintf("dir%d/file", i))
			}
			calls := 0
			got, err := CollectGoverningFiles(t.Context(), GoverningFilesRequest{CommitSHA: strings.Repeat("b", 40), Paths: paths, PathsComplete: true}, func(context.Context, string, int) (GoverningFile, error) {
				calls++
				return GoverningFile{Coverage: DiffCoverage{Completeness: DiffComplete}}, nil
			})
			if err != nil || calls != MaxGoverningFiles {
				t.Fatal(got.Coverage, err, calls)
			}
			if extra == 0 {
				if got.Coverage.Completeness != DiffComplete {
					t.Fatal(got.Coverage)
				}
			} else if !slices.Contains(got.Coverage.Reasons, DiffFileLimit) {
				t.Fatal(got.Coverage)
			}
		})
	}
}

func TestCodeownersUnknownPresenceStopsFallback(t *testing.T) {
	for _, transportError := range []bool{false, true} {
		t.Run(fmt.Sprint(transportError), func(t *testing.T) {
			var checked []string
			got, err := CollectGoverningFiles(t.Context(), GoverningFilesRequest{CommitSHA: strings.Repeat("a", 40), PathsComplete: true}, func(_ context.Context, path string, _ int) (GoverningFile, error) {
				checked = append(checked, path)
				if path == ".github/CODEOWNERS" {
					if transportError {
						return GoverningFile{}, errors.New("unknown presence")
					}
					return GoverningFile{}, nil
				}
				return GoverningFile{Coverage: DiffCoverage{Completeness: DiffComplete}}, nil
			})
			if err == nil || got.SelectedCodeowners != "" || got.Coverage.Completeness != DiffIncomplete || slices.Contains(checked, "CODEOWNERS") || slices.Contains(checked, "docs/CODEOWNERS") || !slices.Contains(checked, ".gitattributes") {
				t.Fatalf("got=%+v checked=%v err=%v", got, checked, err)
			}
		})
	}
}
