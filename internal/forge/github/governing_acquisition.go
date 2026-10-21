package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// operatorGoverning uses the same bounded, typed collector as gitops. Every
// presence/absence and mode comes from the immutable server tree, not --config.
func (c *setupAPI) operatorGoverning(ctx context.Context, repo, commit string) (forge.GoverningFiles, error) {
	tree, err := c.governingTree(ctx, repo, commit)
	if err != nil {
		return forge.GoverningFiles{}, &config.GoverningReadError{Boundary: "tree", Cause: err}
	}
	return forge.CollectGoverningFiles(ctx, forge.GoverningFilesRequest{CommitSHA: commit, PathsComplete: true}, func(ctx context.Context, path string, remaining int) (forge.GoverningFile, error) {
		file := forge.GoverningFile{Path: path, Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}}
		entry, exists := tree[path]
		sha := entry.SHA
		if !exists {
			return file, nil
		}
		file.Present, file.ObjectID, file.Mode = true, sha, entry.Mode
		// A policy path must be a regular blob, never a symlink or gitlink.
		if entry.Type != "blob" || entry.Mode != "100644" && entry.Mode != "100755" {
			file.Coverage.Completeness = forge.DiffIncomplete
			return file, nil
		}
		if entry.Size < 0 || entry.Size > remaining {
			file.Coverage.Completeness = forge.DiffIncomplete
			return file, nil
		}
		data, err := c.importBoundedBlob(ctx, repo, sha, remaining)
		if err != nil {
			return file, &config.GoverningReadError{Boundary: "blob", Cause: err}
		}
		file.Content = string(data)
		return file, nil
	})
}

func (c *setupAPI) operatorIdentities() (*Driver, error) {
	base := c.base.String()
	httpClient := *c.http
	httpClient.Transport = readOnlyTransport{base: c.http.Transport}
	client, err := gh.NewClient(gh.WithHTTPClient(&httpClient), gh.WithURLs(&base, nil))
	if err != nil {
		return nil, err
	}
	return &Driver{client: client}, nil
}

func (c *setupAPI) landedAuthority(ctx context.Context, repo, branch string) (config.AuthorityCandidate, string, forge.GoverningFiles, error) {
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := c.request(ctx, http.MethodGet, "repos/"+repo+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref); err != nil {
		return config.AuthorityCandidate{}, "", forge.GoverningFiles{}, &config.GoverningReadError{Boundary: "default tip", Cause: err}
	}
	if ref.Object.Type != "commit" {
		return config.AuthorityCandidate{}, "", forge.GoverningFiles{}, errors.New("authority governing default ref does not identify a commit")
	}
	g, err := c.operatorGoverning(ctx, repo, ref.Object.SHA)
	if err != nil {
		return config.AuthorityCandidate{}, "", g, err
	}
	var contents []byte
	count := 0
	for _, f := range g.Config {
		if f.Present {
			count++
			contents = []byte(f.Content)
		}
	}
	if count != 1 || g.Coverage.Completeness != forge.DiffComplete {
		return config.AuthorityCandidate{}, "", g, errors.New("authority requires one complete landed governing config")
	}
	candidate, _, err := config.LoadAuthorityCandidate(contents)
	if err != nil {
		return candidate, "", g, errors.New("authority landed governing config is invalid")
	}
	return candidate, ref.Object.SHA, g, nil
}

func (c *setupAPI) validateLandedAuthority(ctx context.Context, repo, branch, commit string, candidate config.AuthorityCandidate, g forge.GoverningFiles) error {
	ids, err := c.operatorIdentities()
	if err != nil {
		return err
	}
	return candidate.ValidateGoverningIdentities(ctx, forge.RepoRef{Path: repo}, branch, commit, g, ids)
}

func (c *setupAPI) importBoundedBlob(ctx context.Context, repo, sha string, limit int) ([]byte, error) {
	var blob struct {
		Size              *int `json:"size"`
		Encoding, Content string
	}
	if !forge.ValidDiffObjectID(sha) {
		return nil, errors.New("invalid governing blob identity")
	}
	if err := c.request(ctx, http.MethodGet, "repos/"+repo+"/git/blobs/"+url.PathEscape(sha), nil, &blob); err != nil {
		return nil, err
	}
	if blob.Size == nil || *blob.Size < 0 || *blob.Size > limit || blob.Encoding != "base64" {
		return nil, errors.New("governing blob size or encoding invalid")
	}
	encoded := strings.ReplaceAll(blob.Content, "\n", "")
	if len(encoded) > base64.StdEncoding.EncodedLen(limit) {
		return nil, errors.New("governing blob budget exceeded")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > limit || len(data) != *blob.Size {
		return nil, errors.New("governing blob content invalid or over budget")
	}
	return data, nil
}

func (c *setupAPI) operatorProtection(ctx context.Context, repo string, refs []string) (authority.Observation, error) {
	ids, err := c.operatorIdentities()
	if err != nil {
		return authority.Observation{}, err
	}
	o, err := ids.observeProtectionRules(ctx, forge.RepoRef{Path: repo})
	if err == nil {
		o, err = observeClassicInventory(ctx, c, forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: repo}, ExpectedRefs: refs}, o)
	}
	if err != nil {
		return o, &config.GoverningReadError{Boundary: "protection", Cause: err}
	}
	if err := forge.ValidateAuthorityAcquisition(o); err != nil {
		return o, err
	}
	return o, nil
}

type governingEntry struct {
	Path, Type, Mode, SHA string
	Size                  int
}

func (c *setupAPI) governingTree(ctx context.Context, repo, commit string) (map[string]governingEntry, error) {
	if !forge.ValidDiffObjectID(commit) {
		return nil, errors.New("governing commit must be a full object identity")
	}
	var result struct {
		Truncated *bool
		Tree      []governingEntry
	}
	if err := c.request(ctx, http.MethodGet, "repos/"+repo+"/git/trees/"+url.PathEscape(commit)+"?recursive=1", nil, &result); err != nil {
		return nil, err
	}
	if result.Truncated == nil || *result.Truncated || result.Tree == nil {
		return nil, errors.New("incomplete governing tree inventory")
	}
	tree := map[string]governingEntry{}
	for _, entry := range result.Tree {
		if _, exists := tree[entry.Path]; exists || !forge.ValidDiffPath(entry.Path) || !forge.ValidDiffObjectID(entry.SHA) {
			return nil, errors.New("invalid governing tree provenance")
		}
		tree[entry.Path] = entry
	}
	// An absent child beneath a non-directory is a complete absence, except
	// policy directory symlinks: those cannot establish usable governing policy.
	for _, parent := range []string{".github", "docs"} {
		if entry, exists := tree[parent]; exists && (entry.Type != "tree" || entry.Mode != "040000") {
			return nil, errors.New("non-directory governing policy parent")
		}
	}
	return tree, nil
}

// AcquireGoverning protects --verify as well as setup/import. It performs no
// writes and derives the governing ref from the forge, never a verification plan.
func (r *AdminWriteRestrictionReader) AcquireGoverning(ctx context.Context, repo string) error {
	c, err := setupClient(r.api, r.token)
	if err != nil {
		return err
	}
	c.http.Transport = readOnlyTransport{c.http.Transport}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.request(ctx, http.MethodGet, "repos/"+repo, nil, &info); err != nil {
		return &config.GoverningReadError{Boundary: "repository", Cause: err}
	}
	landed, commit, g, err := c.landedAuthority(ctx, repo, info.DefaultBranch)
	if err != nil {
		return err
	}
	if err := c.validateLandedAuthority(ctx, repo, info.DefaultBranch, commit, landed, g); err != nil {
		return err
	}
	refs, err := c.operatorAuthorityRefs(ctx, repo, info.DefaultBranch, commit, landed)
	if err != nil {
		return err
	}
	_, err = c.operatorProtection(ctx, repo, refs)
	return err
}

// Protection reads cover the server's complete current branch inventory. Proposed
// selectors may widen setup planning, but cannot narrow landed obligations.
func (c *setupAPI) operatorAuthorityRefs(ctx context.Context, repo, branch, commit string, candidates ...config.AuthorityCandidate) ([]string, error) {
	refs := []string{}
	seen := map[string]bool{}
	defaultSeen := false
	for page := 1; page <= 10; page++ {
		var entries []importBranch
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/branches?per_page=100&page=%d", repo, page), nil, &entries)
		if err != nil {
			return nil, &config.GoverningReadError{Boundary: "branches", Cause: err}
		}
		if entries == nil {
			return nil, errors.New("authority governing branch inventory incomplete")
		}
		for _, entry := range entries {
			ref, err := forge.BranchRef(entry.Name)
			if err != nil || !forge.ValidDiffObjectID(entry.Commit.SHA) || seen[entry.Name] {
				return nil, errors.New("authority governing branch inventory invalid")
			}
			seen[entry.Name] = true
			if entry.Name == branch {
				if entry.Commit.SHA != commit {
					return nil, errors.New("authority governing default tip changed; retry acquisition")
				}
				defaultSeen = true
			}
			selected := entry.Name == branch
			for _, candidate := range candidates {
				for _, q := range candidate.Config.Queues {
					selected = selected || q.MatchesBase(entry.Name, branch)
				}
			}
			for _, family := range []string{"fence", "queue", "green", "verify"} {
				selected = selected || strings.HasPrefix(entry.Name, "openmerge/"+family+"/")
			}
			if selected {
				refs = append(refs, ref)
			}
		}
		if !next {
			if !defaultSeen {
				return nil, errors.New("authority governing default branch absent from inventory")
			}
			slices.Sort(refs)
			return refs, nil
		}
	}
	return nil, errors.New("authority governing branch inventory exceeds acquisition bound")
}
