package gitops

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

// MapProvenanceBuild validates a trusted local composition against immutable
// objects. It does not infer mappings from contributor replay trailers, classify
// generated committers, publish refs, or mutate the original inventory.
func MapProvenanceBuild(ctx context.Context, gitDir, scratch string, inventory provenance.Inventory, build provenance.BuildObservation, limits provenance.InventoryLimits) provenance.BuildMapping {
	result := provenance.BuildMapping{Request: inventory.Request, PolicyID: inventory.PolicyID, Build: build}
	fail := func(outcome provenance.ReadOutcome, resource string) provenance.BuildMapping {
		result.Diagnostic = provenance.Diagnostic{Outcome: outcome, Resource: resource}
		result.Commits = nil
		return result
	}
	if !inventory.Complete {
		return fail(provenance.ReadPending, "inventory")
	}
	if limits.Validate() != nil || !filepath.IsAbs(gitDir) || !validInventoryRequest(inventory.Request) || len(inventory.Commits) > limits.SourceCommits ||
		build.ID == "" || len(build.ID) > 256 || strings.ContainsAny(build.ID, "\x00\r\n") ||
		(build.Stage != "tested" && build.Stage != "landed") || !forge.ValidDiffObjectID(build.Base) || !forge.ValidDiffObjectID(build.Result) ||
		(build.Marker != "" && !forge.ValidDiffObjectID(build.Marker)) || len(build.Replays) > limits.SourceCommits {
		return fail(provenance.InvalidInput, "build")
	}
	if build.Method != "merge" && build.Method != "squash" && build.Method != "rebase" && build.Method != "fast-forward" {
		return fail(provenance.InvalidInput, "method")
	}
	if build.Base != inventory.Request.BuildBase {
		return fail(provenance.InvalidInput, "build_base")
	}
	ids := make(map[string]bool, len(inventory.Commits))
	for _, c := range inventory.Commits {
		if !c.Complete {
			return fail(provenance.ReadPending, "inventory")
		}
		if !forge.ValidDiffObjectID(c.ObjectID) || ids[c.ObjectID] {
			return fail(provenance.InvalidInput, "originals")
		}
		ids[c.ObjectID] = true
	}
	replay := map[string]string{}
	builds := map[string]bool{}
	for _, pair := range build.Replays {
		if build.Method != "rebase" || !ids[pair.Original] || !forge.ValidDiffObjectID(pair.Build) || replay[pair.Original] != "" || builds[pair.Build] || ids[pair.Build] {
			return fail(provenance.InvalidInput, "replays")
		}
		replay[pair.Original], builds[pair.Build] = pair.Build, true
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, limits.Duration)
	defer cancel()
	failure := func(err error) provenance.BuildMapping {
		if errors.Is(err, errDiffOutputLimit) {
			resource := "graph"
			var limitError *provenanceLimitError
			if errors.As(err, &limitError) {
				resource = limitError.resource
			}
			return fail(provenance.LimitExceeded, resource)
		}
		if parentCtx.Err() == nil && ctx.Err() != nil {
			return fail(provenance.LimitExceeded, "time")
		}
		return fail(provenance.ReadPending, "git")
	}
	parser, err := NewTrailerParser(ctx, scratch)
	if err != nil {
		return failure(err)
	}
	defer func() { _ = parser.Close() }()
	s := &session{r: parser.s.r, dir: gitDir, diffRead: true}
	graph, err := readProvenanceGraph(ctx, s, limits, nil, build.Base, build.Result, inventory.Request.Head)
	if err != nil {
		return failure(err)
	}
	base, composed := graph.ancestors(build.Base), graph.ancestors(build.Result)
	if !composed[build.Base] {
		return fail(provenance.InvalidInput, "composition_base")
	}
	switch build.Method {
	case "merge":
		parents := graph[build.Result]
		if build.Marker != "" || len(parents) != 2 || parents[0] != build.Base || parents[1] != inventory.Request.Head {
			return fail(provenance.InvalidInput, "merge_shape")
		}
	case "squash":
		parents := graph[build.Result]
		if build.Marker != "" || len(parents) != 1 || parents[0] != build.Base {
			return fail(provenance.InvalidInput, "squash_shape")
		}
	case "fast-forward":
		if build.Marker == "" {
			if build.Result != inventory.Request.Head {
				return fail(provenance.InvalidInput, "fast_forward_head")
			}
		} else {
			parents := graph[build.Marker]
			if build.Marker != build.Result || len(parents) != 1 || parents[0] != inventory.Request.Head {
				return fail(provenance.InvalidInput, "marker")
			}
		}
	case "rebase":
		if build.Marker != "" {
			return fail(provenance.InvalidInput, "marker")
		}
		// Every newly composed commit must be a recorded replay; predecessors are
		// already beneath Base. No extra generated commit can hide in the result.
		for id := range composed {
			if !base[id] && !builds[id] {
				return fail(provenance.InvalidInput, "missing_replay")
			}
		}
	}

	for original := range ids {
		target := original
		if !base[original] {
			switch build.Method {
			case "squash":
				target = build.Result
			case "rebase":
				target = replay[original]
				if target == "" || !composed[target] || base[target] || len(graph[target]) != 1 {
					return fail(provenance.InvalidInput, "missing_replay")
				}
			}
		} else if replay[original] != "" {
			return fail(provenance.InvalidInput, "landed_replay")
		}
		if !composed[target] || target == build.Marker {
			return fail(provenance.InvalidInput, "mapping_target")
		}
		result.Commits = append(result.Commits, provenance.CommitMapping{Original: original, Build: target})
	}
	if build.Method == "rebase" {
		for target := range builds {
			if !composed[target] || base[target] {
				return fail(provenance.InvalidInput, "replay_target")
			}
		}
	}
	slices.SortFunc(result.Commits, func(a, b provenance.CommitMapping) int { return strings.Compare(a.Original, b.Original) })
	if err := ctx.Err(); err != nil {
		return failure(err)
	}
	result.Complete = true
	result.Diagnostic.Outcome = provenance.ReadComplete
	return result
}
