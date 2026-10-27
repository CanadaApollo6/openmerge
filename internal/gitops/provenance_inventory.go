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

// ReadProvenanceInventory reads only already-acquired local objects. It has no
// Repo/open/fetch dependency and is deliberately unwired from queue execution.
func ReadProvenanceInventory(ctx context.Context, gitDir, scratch string, req provenance.InventoryRequest, classifier provenance.Classifier, limits provenance.InventoryLimits) provenance.Inventory {
	result := provenance.Inventory{Request: req, Limits: limits}
	fail := func(outcome provenance.ReadOutcome, resource, id string) provenance.Inventory {
		result.Complete = false
		result.Diagnostic = provenance.Diagnostic{Outcome: outcome, Resource: resource, ObjectID: id}
		return result
	}
	if limits.Validate() != nil || !filepath.IsAbs(gitDir) || !validInventoryRequest(req) {
		return fail(provenance.InvalidInput, "request", "")
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, limits.Duration)
	defer cancel()
	failure := func(err error, resource, id string) provenance.Inventory {
		outcome := provenance.ReadPending
		if errors.Is(err, errDiffOutputLimit) {
			outcome = provenance.LimitExceeded
			var limitError *provenanceLimitError
			if errors.As(err, &limitError) {
				resource = limitError.resource
			}
		}
		if parentCtx.Err() == nil && ctx.Err() != nil {
			outcome, resource = provenance.LimitExceeded, "time"
		}
		if errors.Is(err, errProvenanceShape) {
			resource = "commit_shape"
		}
		return fail(outcome, resource, id)
	}
	parser, err := NewTrailerParser(ctx, scratch)
	if err != nil {
		return failure(err, "parser", "")
	}
	defer func() { _ = parser.Close() }()
	result.Policy = parser.Policy()
	result.PolicyID = result.Policy.Identity()
	r, err := newProvenanceReader(ctx, gitDir, limits)
	if err != nil {
		return failure(err, "reader", "")
	}
	defer func() { _ = r.close() }()
	// Reject visibly oversized endpoints early; the process cap bounds traversal.
	for _, id := range []string{req.Head, req.PinnedBase, req.BuildBase} {
		if err := r.inspectDeltaMetadata(id); err != nil {
			return failure(err, "graph", id)
		}
	}
	stable, err := r.frontier(ctx, req.Head, req.PinnedBase, len(req.Excluded))
	if err != nil {
		return failure(err, "graph", "")
	}
	build, err := r.frontier(ctx, req.Head, req.BuildBase, len(req.Excluded))
	if err != nil {
		return failure(err, "graph", "")
	}
	result.MergeBases, err = r.mergeBases(ctx, req.Head, req.PinnedBase)
	if err != nil {
		return failure(err, "graph", "")
	}
	result.BuildMergeBases, err = r.mergeBases(ctx, req.Head, req.BuildBase)
	if err != nil {
		return failure(err, "graph", "")
	}
	slices.Sort(result.MergeBases)
	slices.Sort(result.BuildMergeBases)
	excluded := make(map[string]bool, len(req.Excluded))
	for _, id := range req.Excluded {
		excluded[id] = true
	}
	for id := range stable {
		if !excluded[id] {
			result.Stable = append(result.Stable, id)
		}
	}
	for id := range build {
		if _, present := stable[id]; !present && !excluded[id] {
			result.Additional = append(result.Additional, id)
		}
	}
	slices.Sort(result.Stable)
	slices.Sort(result.Additional)
	if len(result.Stable)+len(result.Additional) > limits.SourceCommits {
		// Retain no truncated list that could be mistaken for a complete inventory.
		result.Stable, result.Additional = nil, nil
		return fail(provenance.LimitExceeded, "source_commits", "")
	}
	ids := append(slices.Clone(result.Stable), result.Additional...)
	slices.Sort(ids)
	remaining := limits.MessageBytes
	for _, id := range ids {
		commit := provenance.Commit{ObjectID: id}
		// Include the failed original with Complete=false and no human declarations.
		result.Commits = append(result.Commits, commit)
		raw, err := r.body(id)
		if err != nil {
			return failure(err, "object", id)
		}
		author, committer, message, ok := originalProvenanceFields(raw)
		if !ok {
			return fail(provenance.ReadPending, "commit_shape", id)
		}
		if len(message) > remaining {
			return fail(provenance.LimitExceeded, "message_bytes", id)
		}
		remaining -= len(message)
		if strings.ContainsRune(message, 0) {
			return fail(provenance.ReadPending, "message_nul", id)
		}
		parsed, err := parser.Parse(ctx, message)
		if err != nil {
			return failure(err, "trailers", id)
		}
		declarations := provenance.ParseGitTrailers(parsed)
		declarations = append(declarations, classifier.Declarations(provenance.IdentityInput{AuthorMailbox: author, CommitterMailbox: committer, ParsedTrailers: parsed})...)
		if len(declarations) > limits.Declarations {
			return fail(provenance.LimitExceeded, "declarations", id)
		}
		commit.Complete, commit.Declarations = true, declarations
		result.Commits[len(result.Commits)-1] = commit
	}
	if err := ctx.Err(); err != nil {
		return failure(err, "read", "")
	}
	if err := r.close(); err != nil {
		return failure(err, "object", "")
	}
	result.Complete = true
	result.Diagnostic.Outcome = provenance.ReadComplete
	return result
}

func validInventoryRequest(req provenance.InventoryRequest) bool {
	if !forge.ValidDiffObjectID(req.Head) || !forge.ValidDiffObjectID(req.PinnedBase) || !forge.ValidDiffObjectID(req.BuildBase) || len(req.Excluded) > 1024 {
		return false
	}
	for _, text := range []string{req.Repository, req.Queue, req.ChangeID, req.Revision, req.Generation} {
		if text == "" || len(text) > 256 || strings.ContainsAny(text, "\x00\r\n") {
			return false
		}
	}
	for _, id := range req.Excluded {
		if !forge.ValidDiffObjectID(id) {
			return false
		}
	}
	return true
}

func originalProvenanceFields(raw string) (author, committer, message string, ok bool) {
	headers, message, found := strings.Cut(raw, "\n\n")
	if !found {
		return "", "", "", false
	}
	// Git's commit grammar starts with exactly one tree, consecutive parents,
	// then author and committer. Unexpected reserved headers after that prefix
	// must never supply identities or graph edges.
	lines := strings.Split(headers, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "tree ") || !forge.ValidDiffObjectID(strings.TrimPrefix(lines[0], "tree ")) {
		return "", "", "", false
	}
	i := 1
	for i < len(lines) && strings.HasPrefix(lines[i], "parent ") {
		if !forge.ValidDiffObjectID(strings.TrimPrefix(lines[i], "parent ")) {
			return "", "", "", false
		}
		i++
	}
	if i+1 >= len(lines) || !strings.HasPrefix(lines[i], "author ") || !strings.HasPrefix(lines[i+1], "committer ") {
		return "", "", "", false
	}
	for j, line := range lines[i:] {
		key, value, _ := strings.Cut(line, " ")
		if j >= 2 {
			if key == "tree" || key == "parent" || key == "author" || key == "committer" {
				return "", "", "", false
			}
			continue
		}
		left, right := strings.LastIndex(value, "<"), strings.LastIndex(value, ">")
		if left < 0 || right <= left || right+1 >= len(value) {
			return "", "", "", false
		}
		mailbox := value[left+1 : right]
		if key == "author" {
			if author != "" {
				return "", "", "", false
			}
			author = mailbox
		} else {
			if committer != "" {
				return "", "", "", false
			}
			committer = mailbox
		}
	}
	return author, committer, message, author != "" && committer != ""
}
