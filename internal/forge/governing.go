package forge

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
)

// GoverningFilesReader is dormant and independent of configuration parsing.
// Paths must include both rename sides and deleted paths from the pinned diff.
type GoverningFilesReader interface {
	GoverningFiles(context.Context, RepoRef, GoverningFilesRequest) (GoverningFiles, error)
}

// GoverningFilesRequest pins policy provenance separately from diff endpoints.
type GoverningFilesRequest struct {
	CommitSHA     string
	Paths         []string
	PathsComplete bool
}

// Governing probe and content limits are part of DiffSemantics.
const (
	MaxGoverningFiles = 256 // Includes absent probes, bounding tree reads too.
	MaxGoverningBytes = 1 << 20
)

// GoverningFile retains tree-backed presence/absence, mode and blob provenance.
// Present with incomplete Coverage never supplies usable policy content. An
// absent file has complete Coverage, Present=false and no mode/object/content.
type GoverningFile struct {
	Path, ObjectID, Mode string
	Present              bool
	Content              string
	Coverage             DiffCoverage
}

// GoverningFiles retains checked candidates; missing probes prove no absence.
type GoverningFiles struct {
	CommitSHA, Semantics           string
	Config, Codeowners, Attributes []GoverningFile
	SelectedCodeowners             string
	Coverage                       DiffCoverage
}

// GoverningFileRead is a pinned transport primitive. It must check size before
// reading content, cap output at budget, and distinguish absence from errors.
type GoverningFileRead func(context.Context, string, int) (GoverningFile, error)

// CollectGoverningFiles bounds probes and content, preserving checked absences.
// Config candidates mirror config.Find; selection/ambiguity belongs to 4a.
// CODEOWNERS stops at the first present location, even if it is unreadable.
func CollectGoverningFiles(ctx context.Context, req GoverningFilesRequest, read GoverningFileRead) (GoverningFiles, error) {
	out := GoverningFiles{CommitSHA: req.CommitSHA, Semantics: DiffSemantics, Coverage: DiffCoverage{Completeness: DiffComplete}}
	fail := func(reason DiffFailureReason, err error) (GoverningFiles, error) {
		out.Coverage.Completeness = DiffIncomplete
		if !containsReason(out.Coverage.Reasons, reason) {
			out.Coverage.Reasons = append(out.Coverage.Reasons, reason)
		}
		return out, err
	}
	if !ValidDiffObjectID(req.CommitSHA) {
		return fail(DiffInvalidObjectID, errors.New("governing read requires full commit object ID"))
	}
	if len(req.Paths) > 2*MaxDiffFiles {
		return fail(DiffFileLimit, nil)
	}
	bytes, directoryBytes := 0, len(".gitattributes")
	dirs := map[string]bool{".gitattributes": true}
	for _, p := range req.Paths {
		bytes += len(p)
		if bytes > MaxDiffBytes {
			return fail(DiffByteLimit, nil)
		}
		if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return fail(DiffReadFailure, errors.New("invalid governing changed path"))
		}
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			attribute := dir + "/.gitattributes"
			if !dirs[attribute] {
				directoryBytes += len(attribute)
				if directoryBytes > MaxDiffBytes {
					return fail(DiffByteLimit, nil)
				}
				dirs[attribute] = true
			}
			if len(dirs) > MaxGoverningFiles {
				return fail(DiffFileLimit, nil)
			}
		}
	}
	paths := make([]string, 0, len(dirs))
	for p := range dirs {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	count, remaining := 0, MaxGoverningBytes
	probe := func(p string, target *[]GoverningFile) (bool, DiffFailureReason, error) {
		if err := ctx.Err(); err != nil {
			return false, DiffReadFailure, err
		}
		if count == MaxGoverningFiles {
			return false, DiffFileLimit, nil
		}
		count++
		f, err := read(ctx, p, remaining)
		if err != nil {
			return false, DiffReadFailure, err
		}
		f.Path = p
		*target = append(*target, f)
		remaining -= len(f.Content)
		if f.Coverage.Completeness != DiffComplete {
			reason := DiffReadFailure
			if len(f.Coverage.Reasons) != 0 {
				reason = f.Coverage.Reasons[0]
			}
			if reason == DiffReadFailure {
				return f.Present, reason, errors.New("governing file coverage is unknown")
			}
			return f.Present, reason, nil
		}
		return f.Present, "", nil
	}
	for _, p := range []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml"} {
		if _, reason, err := probe(p, &out.Config); reason != "" {
			return fail(reason, err)
		}
	}
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		present, reason, err := probe(p, &out.Codeowners)
		if present {
			out.SelectedCodeowners = p
		}
		if reason != "" {
			return fail(reason, err)
		}
		if present {
			break
		}
	}
	for _, p := range paths {
		if _, reason, err := probe(p, &out.Attributes); reason != "" {
			return fail(reason, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(DiffReadFailure, err)
	}
	if !req.PathsComplete {
		return fail(DiffInventoryIncomplete, nil)
	}
	return out, nil
}
