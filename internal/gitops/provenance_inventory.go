package gitops

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
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
		return fail(outcome, resource, id)
	}
	parser, err := NewTrailerParser(ctx, scratch)
	if err != nil {
		return failure(err, "parser", "")
	}
	defer func() { _ = parser.Close() }()
	result.Policy = parser.Policy()
	result.PolicyID = result.Policy.Identity()
	s := &session{r: parser.s.r, dir: gitDir, diffRead: true}
	objects := map[string]string{}
	graph, err := readProvenanceGraph(ctx, s, limits, objects, req.Head, req.PinnedBase, req.BuildBase)
	if err != nil {
		return failure(err, "graph", "")
	}
	stableBase := graph.ancestors(req.PinnedBase)
	buildBase := graph.ancestors(req.BuildBase)
	head := graph.ancestors(req.Head)
	result.MergeBases = graph.mergeBases(head, stableBase)
	result.BuildMergeBases = graph.mergeBases(head, buildBase)
	excluded := make(map[string]bool, len(req.Excluded))
	for _, id := range req.Excluded {
		excluded[id] = true
	}
	for id := range head {
		if excluded[id] {
			continue
		}
		if !stableBase[id] {
			result.Stable = append(result.Stable, id)
		} else if !buildBase[id] {
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
		raw := objects[id]
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

type provenanceGraph map[string][]string

func readProvenanceGraph(ctx context.Context, s *session, limits provenance.InventoryLimits, objects map[string]string, heads ...string) (provenanceGraph, error) {
	graph := provenanceGraph{}
	todo := slices.Clone(heads)
	remaining, parentBytes := limits.GraphBytes, 4<<20
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if _, seen := graph[id]; seen {
			continue
		}
		if len(graph) == limits.GraphCommits {
			return nil, &provenanceLimitError{"graph"}
		}
		// Query type/size before expanding every object, including excluded base
		// ancestry. rev-list alone cannot bound its own decompressed input bytes.
		raw, err := readProvenanceObject(ctx, s, id, min(limits.CommitBytes, remaining))
		if err != nil {
			if errors.Is(err, errDiffOutputLimit) && remaining < limits.CommitBytes {
				return nil, &provenanceLimitError{"graph_bytes"}
			}
			return nil, err
		}
		remaining -= len(raw)
		if objects != nil {
			objects[id] = raw
		}
		headers, _, ok := strings.Cut(raw, "\n\n")
		if !ok {
			return nil, errors.New("malformed graph commit")
		}
		var parents []string
		tree := false
		for line := range strings.SplitSeq(headers, "\n") {
			key, value, _ := strings.Cut(line, " ")
			if key != "tree" && key != "parent" {
				continue
			}
			if !forge.ValidDiffObjectID(value) {
				return nil, errors.New("invalid graph edge")
			}
			if key == "tree" {
				if tree {
					return nil, errors.New("duplicate tree")
				}
				tree = true
				continue
			}
			parentBytes -= len(value) + 1
			if parentBytes < 0 {
				return nil, &provenanceLimitError{"graph_edges"}
			}
			parents = append(parents, value)
		}
		if !tree {
			return nil, errors.New("missing tree")
		}
		graph[id] = parents
		todo = append(todo, parents...)
	}
	return graph, nil
}

func (g provenanceGraph) ancestors(head string) map[string]bool {
	seen := map[string]bool{}
	todo := []string{head}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		todo = append(todo, g[id]...)
	}
	return seen
}

func (g provenanceGraph) mergeBases(a, b map[string]bool) []string {
	common := map[string]bool{}
	for id := range a {
		if b[id] {
			common[id] = true
		}
	}
	best := make(map[string]bool, len(common))
	for id := range common {
		best[id] = true
	}
	for id := range common {
		for _, parent := range g[id] {
			delete(best, parent)
		}
	}
	var out []string
	for id := range best {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func readProvenanceObject(ctx context.Context, s *session, id string, limit int) (string, error) {
	info, err := s.provenanceCommand(ctx, id+"\n", 128, "cat-file", "--batch-check=%(objecttype) %(objectsize)")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(info)
	if len(fields) != 2 || fields[0] != "commit" {
		return "", errors.New("object is missing or is not a commit")
	}
	size, err := strconv.Atoi(fields[1])
	if err != nil || size < 0 {
		return "", errors.New("invalid commit size")
	}
	if size > limit {
		return "", &provenanceLimitError{"commit_bytes"}
	}
	return s.provenanceCommand(ctx, "", limit, "cat-file", "commit", id)
}

func originalProvenanceFields(raw string) (author, committer, message string, ok bool) {
	headers, message, found := strings.Cut(raw, "\n\n")
	if !found {
		return "", "", "", false
	}
	for line := range strings.SplitSeq(headers, "\n") {
		key, value, _ := strings.Cut(line, " ")
		if key != "author" && key != "committer" {
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
