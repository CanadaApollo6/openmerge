package fake

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type fileDelta struct {
	parent               string
	changed              map[string]string
	deleted              []string
	addedPRs, removedPRs []int
}

// CompactTrees bounds materialized trees in large simulations. Every historical
// tree and PR marker set remain exactly reconstructible from first-parent deltas.
// This opt-in mode hashes those deltas and parents for commit identity, avoiding
// quadratic full-tree serialization; default fake commit identities are unchanged.
func (f *Forge) CompactTrees(repo forge.RepoRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	if r.deltas != nil {
		return
	}
	// Call before creating commits: migrating an existing graph is deliberately
	// unsupported, since a partial delta graph would lose historical evidence.
	if len(r.commits) > 0 {
		panic("enable compact trees before repository initialization")
	}
	r.deltas = make(map[string]fileDelta)
}

func (r *repository) files(sha string) map[string]string {
	c := r.commits[sha]
	if c.Files != nil {
		return c.Files
	}
	var chain []fileDelta
	for c.Files == nil {
		d, ok := r.deltas[sha]
		if !ok {
			return nil
		}
		chain = append(chain, d)
		if d.parent == "" {
			break
		}
		sha = d.parent
		c = r.commits[sha]
	}
	files := maps.Clone(c.Files)
	if files == nil {
		files = make(map[string]string)
	}
	for _, d := range slices.Backward(chain) {
		for path, value := range d.changed {
			files[path] = value
		}
		for _, path := range d.deleted {
			delete(files, path)
		}
	}
	return files
}
func (r *repository) treeDelta(c Commit) fileDelta {
	d := fileDelta{changed: make(map[string]string)}
	var parent map[string]string
	var parentPRs []int
	if len(c.Parents) > 0 {
		d.parent = c.Parents[0]
		parent = r.files(d.parent)
		parentPRs = r.prsFor(d.parent)
	}
	for path, value := range c.Files {
		if old, ok := parent[path]; !ok || old != value {
			d.changed[path] = value
		}
	}
	for path := range parent {
		if _, ok := c.Files[path]; !ok {
			d.deleted = append(d.deleted, path)
		}
	}
	slices.Sort(d.deleted)
	i, j := 0, 0
	for i < len(parentPRs) || j < len(c.PRs) {
		switch {
		case j == len(c.PRs) || i < len(parentPRs) && parentPRs[i] < c.PRs[j]:
			d.removedPRs = append(d.removedPRs, parentPRs[i])
			i++
		case i == len(parentPRs) || c.PRs[j] < parentPRs[i]:
			d.addedPRs = append(d.addedPRs, c.PRs[j])
			j++
		default:
			i++
			j++
		}
	}
	return d
}
func (r *repository) compactTree(c Commit, d fileDelta) {
	r.deltas[c.SHA] = d
	r.materialized = append(r.materialized, c.SHA)
	if len(r.materialized) > 64 {
		id := r.materialized[0]
		r.materialized = r.materialized[1:]
		old := r.commits[id]
		old.Files = nil
		old.PRs = nil
		r.commits[id] = old
	}
}
func (r *repository) prsFor(sha string) []int {
	c := r.commits[sha]
	if r.deltas == nil || c.Files != nil {
		return c.PRs
	}
	var chain []fileDelta
	for c.Files == nil {
		d, ok := r.deltas[sha]
		if !ok {
			return nil
		}
		chain = append(chain, d)
		if d.parent == "" {
			break
		}
		sha = d.parent
		c = r.commits[sha]
	}
	prs := slices.Clone(c.PRs)
	for _, d := range slices.Backward(chain) {
		prs = append(prs, d.addedPRs...)
		for _, removed := range d.removedPRs {
			prs = slices.DeleteFunc(prs, func(pr int) bool { return pr == removed })
		}
	}
	slices.Sort(prs)
	prs = slices.Compact(prs)
	if len(prs) == 0 {
		return nil
	}
	return prs
}

// CommitMetadata reads graph/content markers without materializing or copying a
// full file tree. ContainsPR independently verifies the actual changed files.
func (f *Forge) CommitMetadata(repo forge.RepoRef, sha string) (Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.repo(repo).commitMetadata(sha)
	if err != nil {
		return Commit{}, err
	}
	c.Parents = slices.Clone(c.Parents)
	c.PRs = slices.Clone(f.repo(repo).prsFor(sha))
	c.Files = nil
	return c, nil
}

func compactSHA(c Commit, d fileDelta) string {
	data, _ := json.Marshal(struct {
		Parents              []string
		AddedPRs, RemovedPRs []int
		Message              string
		Changed              map[string]string
		Deleted              []string
	}{c.Parents, d.addedPRs, d.removedPRs, c.Message, d.changed, d.deleted})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Batch construction already knows the exact file edits it just applied. Keep
// only those deltas for intermediate commits; materialize the final tip once.
func (r *repository) buildCommit(parents []string, prs []int, message string, files, changed map[string]string, added []int) string {
	if r.deltas == nil {
		return r.commit(parents, prs, message, files)
	}
	c := Commit{Parents: slices.Clone(parents), Message: message}
	d := fileDelta{parent: parents[0], changed: maps.Clone(changed), addedPRs: slices.Clone(added)}
	c.SHA = compactSHA(c, d)
	r.commits[c.SHA] = c
	r.compactTree(c, d)
	return c.SHA
}
func (r *repository) materializeTip(sha string, files map[string]string, prs []int) {
	if r.deltas == nil {
		return
	}
	c := r.commits[sha]
	c.Files = maps.Clone(files)
	c.PRs = slices.Clone(prs)
	slices.Sort(c.PRs)
	c.PRs = slices.Compact(c.PRs)
	r.commits[sha] = c
}
