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
	count, paths, metadata, parentEdges, frontierEntries := 0, 0, 0, 0, 0
	for i := range p.Refs {
		ref := &p.Refs[i]
		ref.Commits = []forge.PushCommit{}
		ref.Boundary = []string{}
		if ref.NewSHA == "" {
			continue
		}
		if operation == forge.PushBuild && ref.OldSHA == "" {
			baseRef := strings.TrimPrefix(opts.Context.BaseRef(), "refs/heads/")
			ref.CreationBaseSHA = r.refs[baseRef]
			if ref.CreationBaseSHA == "" {
				return fmt.Errorf("creation boundary base is absent")
			}
		}
		metadata += len(ref.Ref) + len(ref.OldSHA) + len(ref.NewSHA) + len(ref.CreationBaseSHA)
		if metadata > forge.MaxPushMetadataBytes {
			return fmt.Errorf("prepared push ref budget exceeded")
		}
		old := map[string]bool{}
		stack := []string{}
		source := ref.OldSHA
		if source == "" {
			source = ref.CreationBaseSHA
		}
		if source != "" {
			stack = append(stack, source)
		}
		proofEdges := 0
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
			proofEdges += len(c.Parents)
			if proofEdges > forge.MaxPushParentEdges {
				return fmt.Errorf("old push graph exceeds parent proof budget")
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
				if !boundary[oid] {
					frontierEntries++
					metadata += len(oid)
					if frontierEntries > forge.MaxPushBoundaryEntries || metadata > forge.MaxPushMetadataBytes {
						return fmt.Errorf("prepared push frontier budget exceeded")
					}
					boundary[oid] = true
				}
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
			prepared := forge.PushCommit{OID: oid, Message: c.Message, Parents: parents, Files: []forge.PushFile{}, Diffs: []forge.PushDiff{}}
			count++
			metadata += len(oid) + len(c.Message)
			for _, parent := range parents {
				metadata += len(parent)
			}
			if count > forge.MaxPushCommits || metadata > forge.MaxPushMetadataBytes {
				return fmt.Errorf("prepared push object budget exceeded")
			}
			after := r.files(oid)
			comparisons := parents
			if len(comparisons) == 0 {
				comparisons = []string{""}
			}
			files := map[string]bool{}
			for _, parent := range comparisons {
				before := map[string]string{}
				if parent != "" {
					before = r.files(parent)
				}
				metadata += len(parent)
				diff := forge.PushDiff{Parent: parent, Paths: []string{}}
				changed := map[string]bool{}
				record := func(path string) error {
					if !changed[path] && paths+len(changed)+1 > forge.MaxPushPaths {
						return fmt.Errorf("prepared push changed-path budget exceeded")
					}
					changed[path] = true
					return nil
				}
				for path, content := range after {
					if old, ok := before[path]; !ok || old != content {
						if err := record(path); err != nil {
							return err
						}
					}
				}
				for path := range before {
					if _, ok := after[path]; !ok {
						if err := record(path); err != nil {
							return err
						}
					}
				}
				for path := range changed {
					paths++
					metadata += len(path)
					if paths > forge.MaxPushPaths || metadata > forge.MaxPushMetadataBytes {
						return fmt.Errorf("prepared push changed-path budget exceeded")
					}
					diff.Paths = append(diff.Paths, path)
					content, present := after[path]
					if !present || files[path] {
						continue
					}
					oid := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
					paths++
					metadata += len(path) + len(oid) + len("blob")
					if paths > forge.MaxPushPaths || metadata > forge.MaxPushMetadataBytes {
						return fmt.Errorf("prepared push post-image budget exceeded")
					}
					prepared.Files = append(prepared.Files, forge.PushFile{Path: path, OID: oid, Kind: "blob", Size: int64(len(content))})
					files[path] = true
				}
				slices.Sort(diff.Paths)
				prepared.Diffs = append(prepared.Diffs, diff)
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
