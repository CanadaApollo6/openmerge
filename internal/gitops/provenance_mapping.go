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
func MapProvenanceBuild(ctx context.Context, gitDir, _ string, inventory provenance.Inventory, build provenance.BuildObservation, limits provenance.InventoryLimits) provenance.BuildMapping {
	result := provenance.BuildMapping{Request: inventory.Request, PolicyID: inventory.PolicyID, Build: build, Commits: []provenance.CommitMapping{}}
	result.Build.Replays = append([]provenance.CommitMapping{}, build.Replays...)
	slices.SortFunc(result.Build.Replays, func(a, b provenance.CommitMapping) int { return strings.Compare(a.Original, b.Original) })
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
		if errors.Is(err, errProvenanceShape) {
			return fail(provenance.ReadPending, "commit_shape")
		}
		return fail(provenance.ReadPending, "git")
	}
	r, err := newProvenanceReader(ctx, gitDir, limits)
	if err != nil {
		return failure(err)
	}
	defer func() { _ = r.close() }()
	for _, id := range []string{build.Base, build.Result, inventory.Request.Head} {
		if err := r.inspectDeltaMetadata(id); err != nil {
			return failure(err)
		}
	}
	graph, err := r.frontier(ctx, build.Result, build.Base, 1)
	if err != nil {
		return failure(err)
	}
	// Reject visibly oversized compositions early. Every reachability query
	// remains supervised regardless of these advisory metadata checks.
	for id := range graph {
		if err := r.inspectDeltaMetadata(id); err != nil {
			return failure(err)
		}
	}
	isBase, err := r.ancestor(ctx, build.Base, build.Result)
	if err != nil {
		return failure(err)
	}
	if !isBase {
		return fail(provenance.InvalidInput, "composition_base")
	}
	parents, err := r.parents(ctx, build.Result)
	if err != nil {
		return failure(err)
	}
	switch build.Method {
	case "merge":
		if build.Marker != "" || len(parents) != 2 || parents[0] != build.Base || parents[1] != inventory.Request.Head {
			return fail(provenance.InvalidInput, "merge_shape")
		}
	case "squash":
		if build.Marker != "" || len(parents) != 1 || parents[0] != build.Base {
			return fail(provenance.InvalidInput, "squash_shape")
		}
	case "fast-forward":
		if build.Marker == "" {
			if build.Result != inventory.Request.Head {
				return fail(provenance.InvalidInput, "fast_forward_head")
			}
		} else {
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
		for id := range graph {
			if !builds[id] {
				return fail(provenance.InvalidInput, "missing_replay")
			}
		}
	}

	for original := range ids {
		if err := r.inspectDeltaMetadata(original); err != nil {
			return failure(err)
		}
		landed, err := r.ancestor(ctx, original, build.Base)
		if err != nil {
			return failure(err)
		}
		target := original
		if !landed {
			switch build.Method {
			case "squash":
				target = build.Result
			case "rebase":
				target = replay[original]
				if target == "" || len(graph[target]) != 1 {
					return fail(provenance.InvalidInput, "missing_replay")
				}
			}
		} else if replay[original] != "" {
			return fail(provenance.InvalidInput, "landed_replay")
		}
		reachable, err := r.ancestor(ctx, target, build.Result)
		if err != nil {
			return failure(err)
		}
		if !reachable || target == build.Marker {
			return fail(provenance.InvalidInput, "mapping_target")
		}
		result.Commits = append(result.Commits, provenance.CommitMapping{Original: original, Build: target})
	}
	if build.Method == "rebase" {
		for target := range builds {
			if _, present := graph[target]; !present {
				return fail(provenance.InvalidInput, "replay_target")
			}
		}
	}
	slices.SortFunc(result.Commits, func(a, b provenance.CommitMapping) int { return strings.Compare(a.Original, b.Original) })
	if err := ctx.Err(); err != nil {
		return failure(err)
	}
	if err := r.close(); err != nil {
		return failure(err)
	}
	result.Complete = true
	result.Diagnostic.Outcome = provenance.ReadComplete
	return result
}
