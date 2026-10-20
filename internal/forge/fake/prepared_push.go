package fake

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func (r *repository) validatePush(ctx context.Context, repo forge.RepoRef, opts forge.PushOptions, operation forge.PushOperation, refs ...forge.PushRef) error {
	if !opts.AuthorityRequired {
		return nil
	}
	p := forge.PreparedPush{Operation: operation, Refs: refs}
	count, paths, metadata, parentEdges := 0, 0, 0, 0
	for i := range p.Refs {
		ref := &p.Refs[i]
		ref.Commits = []forge.PushCommit{}
		ref.Boundary = []string{}
		if ref.NewSHA == "" {
			continue
		}
		old := map[string]bool{}
		stack := []string{}
		if ref.OldSHA != "" {
			stack = append(stack, ref.OldSHA)
		}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			oid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if old[oid] {
				continue
			}
			c, ok := r.commits[oid]
			if !ok {
				return fmt.Errorf("missing old push object %s", oid)
			}
			old[oid] = true
			if len(old) > forge.MaxPushCommits*10 {
				return fmt.Errorf("old push graph exceeds proof budget")
			}
			stack = append(stack, c.Parents...)
		}
		seen, boundary := map[string]bool{}, map[string]bool{}
		stack = []string{ref.NewSHA}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			oid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if old[oid] {
				boundary[oid] = true
				continue
			}
			if seen[oid] {
				continue
			}
			seen[oid] = true
			c, ok := r.commits[oid]
			if !ok {
				return fmt.Errorf("missing prepared push object %s", oid)
			}
			parentEdges += len(c.Parents)
			if parentEdges > forge.MaxPushParentEdges {
				return fmt.Errorf("prepared push parent-edge budget exceeded")
			}
			parents := append([]string{}, c.Parents...)
			prepared := forge.PushCommit{OID: oid, Message: c.Message, Parents: parents, Files: []forge.PushFile{}}
			count++
			metadata += len(c.Message)
			if count > forge.MaxPushCommits || metadata > forge.MaxPushMetadataBytes {
				return fmt.Errorf("prepared push object budget exceeded")
			}
			for path, content := range r.files(oid) {
				paths++
				metadata += len(path)
				if paths > forge.MaxPushPaths || metadata > forge.MaxPushMetadataBytes {
					return fmt.Errorf("prepared push path budget exceeded")
				}
				prepared.Files = append(prepared.Files, forge.PushFile{Path: path, OID: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), Kind: "blob", Size: int64(len(content))})
			}
			slices.SortFunc(prepared.Files, func(a, b forge.PushFile) int {
				if a.Path < b.Path {
					return -1
				}
				if a.Path > b.Path {
					return 1
				}
				return 0
			})
			ref.Commits = append(ref.Commits, prepared)
			stack = append(stack, c.Parents...)
		}
		for oid := range boundary {
			ref.Boundary = append(ref.Boundary, oid)
		}
		slices.Sort(ref.Boundary)
	}
	p.Complete = true
	return opts.ValidateBeforePush(ctx, repo, p)
}

// ReadPreparedRef distinguishes absent refs from failed authority lease reads.
func (f *Forge) ReadPreparedRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "ReadPreparedRef"); err != nil {
		return "", err
	}
	full, err := forge.BranchRef(ref)
	if err != nil {
		return "", err
	}
	return f.repo(repo).refs[strings.TrimPrefix(full, "refs/heads/")], nil
}
