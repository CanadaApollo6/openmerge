package gitops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// validatePush inspects generated and acquired objects in this very session.
// Acquisition may hydrate a proven existing base; inspection never fetches or
// rebuilds objects. Zero options return
// before any added Git command, preserving legacy I/O and locking exactly.
func (s *session) validatePush(ctx context.Context, repo forge.RepoRef, opts forge.PushOptions, operation forge.PushOperation, refs ...forge.PushRef) error {
	if !opts.AuthorityRequired {
		return nil
	}
	// Bound a new ref to the authenticated build base, including speculative
	// prior-batch tips. The live target must still exist and reach that base.
	// An arbitrary request BaseSHA cannot change the frozen dispatch intent.
	for i := range refs {
		if operation == forge.PushBuild && refs[i].OldSHA == "" && refs[i].NewSHA != "" {
			baseRef := opts.Context.BaseRef()
			values, err := s.read(ctx, baseRef)
			if err != nil {
				return err
			}
			if values[baseRef] == "" {
				return errors.New("creation boundary base is absent")
			}
			base := opts.Context.BuildBaseSHA()
			if err := s.commits(ctx, base); err != nil {
				return err
			}
			if err := s.commits(ctx, values[baseRef]); err != nil {
				return err
			}
			_, diagnostic, err := s.run(ctx, "", "merge-base", "--is-ancestor", values[baseRef], base)
			if err != nil {
				return s.failure("prepared boundary proof", diagnostic, err)
			}
			refs[i].CreationBaseSHA = base
		}
	}
	p, err := s.preparedPush(ctx, operation, refs)
	if err != nil {
		return err
	}
	return opts.ValidateBeforePush(ctx, repo, p)
}

func (s *session) preparedPush(ctx context.Context, operation forge.PushOperation, refs []forge.PushRef) (forge.PreparedPush, error) {
	p := forge.PreparedPush{Operation: operation, Refs: slices.Clone(refs)}
	// Acquisition may have to hydrate an old test ref on update, but inspection
	// itself can only use immutable local objects, with replacement/lazy fetch off.
	for _, r := range refs {
		if r.NewSHA == "" {
			continue
		} // Deletions have no object inventory or fetch.
		for _, oid := range []string{r.OldSHA, r.NewSHA, r.CreationBaseSHA} {
			if oid != "" {
				if err := s.commits(ctx, oid); err != nil {
					return p, err
				}
			}
		}
	}
	reader := *s
	reader.diffRead = true
	before, reason, err := reader.diffHistory(ctx)
	if err != nil {
		return p, err
	}
	if reason != "" {
		return p, fmt.Errorf("push inventory history: %s", reason)
	}
	remaining := forge.MaxPushMetadataBytes
	commitCount, parentEdges, boundaryCount := 0, 0, 0
	read := func(args ...string) (string, error) {
		raw, overflow, err := reader.diffCommand(ctx, remaining, args...)
		if overflow {
			return "", errors.New("push inventory byte budget exceeded")
		}
		remaining -= len(raw)
		return raw, err
	}
	for i := range p.Refs {
		r := &p.Refs[i]
		r.Commits = []forge.PushCommit{}
		r.Boundary = []string{}
		if r.NewSHA == "" {
			continue
		}
		args := []string{"rev-list", "--topo-order", "--max-count=" + strconv.Itoa(forge.MaxPushCommits+1), r.NewSHA}
		source := r.OldSHA
		if source == "" {
			source = r.CreationBaseSHA
		}
		remaining -= len(r.Ref) + len(r.OldSHA) + len(r.NewSHA) + len(r.CreationBaseSHA)
		if remaining < 0 {
			return p, errors.New("push inventory ref budget exceeded")
		}
		if source != "" {
			args = append(args, "^"+source)
		}
		args = append(args, "--")
		raw, err := read(args...)
		if err != nil {
			return p, err
		}
		oids := strings.Fields(raw)
		commitCount += len(oids)
		if commitCount > forge.MaxPushCommits {
			return p, errors.New("push inventory commit budget exceeded")
		}
		for _, oid := range oids {
			pathCount := 0
			raw, err := read("cat-file", "commit", oid)
			if err != nil {
				return p, err
			}
			header, message, ok := strings.Cut(raw, "\n\n")
			if !ok {
				return p, errors.New("invalid prepared commit")
			}
			c := forge.PushCommit{OID: oid, Parents: []string{}, Message: message, Files: []forge.PushFile{}, Diffs: []forge.PushDiff{}}
			signature := false
			for _, line := range strings.Split(header, "\n") {
				if strings.HasPrefix(line, "parent ") {
					parentEdges++
					if parentEdges > forge.MaxPushParentEdges {
						return p, errors.New("push inventory parent-edge budget exceeded")
					}
					c.Parents = append(c.Parents, strings.TrimPrefix(line, "parent "))
				}
				if strings.HasPrefix(line, "gpgsig ") || strings.HasPrefix(line, "gpgsig-sha256 ") {
					signature = true
					c.Signature += line + "\n"
				} else if signature && strings.HasPrefix(line, " ") {
					c.Signature += line + "\n"
				} else {
					signature = false
				}
			}
			// Compare against each parent, never Git's combined merge diff.
			// --no-renames retains the source and destination as delete/add.
			parents := c.Parents
			if len(parents) == 0 {
				parents = []string{""}
			}
			files := map[string]bool{}
			for _, parent := range parents {
				remaining -= len(parent)
				if remaining < 0 {
					return p, errors.New("push inventory parent metadata budget exceeded")
				}
				args := []string{"diff-tree", "--raw", "--no-abbrev", "--no-commit-id", "--no-renames", "--no-ext-diff", "--no-textconv", "-r", "-z"}
				if parent == "" {
					args = append(args, "--root", oid)
				} else {
					args = append(args, parent, oid)
				}
				args = append(args, "--")
				raw, err := read(args...)
				if err != nil {
					return p, err
				}
				diff := forge.PushDiff{Parent: parent, Paths: []string{}}
				for raw != "" {
					meta, rest, ok := strings.Cut(raw, "\x00")
					if !ok {
						return p, errors.New("unterminated prepared diff metadata")
					}
					path, rest, ok := strings.Cut(rest, "\x00")
					fields := strings.Fields(meta)
					if !ok || len(fields) != 5 {
						return p, errors.New("invalid prepared diff entry")
					}
					raw = rest
					pathCount++
					if pathCount > forge.MaxPushPaths {
						return p, errors.New("push inventory path budget exceeded")
					}
					diff.Paths = append(diff.Paths, path)
					if fields[4] == "D" || fields[2] == fields[3] || files[path] {
						continue
					}
					kind := "blob"
					size := int64(0)
					if fields[1] == "160000" {
						kind = "commit"
					} else {
						value, err := read("cat-file", "-s", fields[3])
						if err != nil {
							return p, err
						}
						size, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
						if err != nil {
							return p, err
						}
					}
					pathCount++
					remaining -= len(path) + len(fields[3]) + len(kind)
					if pathCount > forge.MaxPushPaths || remaining < 0 {
						return p, errors.New("push inventory post-image budget exceeded")
					}
					c.Files = append(c.Files, forge.PushFile{Path: path, Kind: kind, OID: fields[3], Size: size})
					files[path] = true
				}
				c.Diffs = append(c.Diffs, diff)
			}
			r.Commits = append(r.Commits, c)
		}
		introduced := map[string]bool{}
		for _, c := range r.Commits {
			introduced[c.OID] = true
		}
		frontier := map[string]bool{}
		addBoundary := func(oid string) error {
			if frontier[oid] {
				return nil
			}
			boundaryCount++
			remaining -= len(oid)
			if boundaryCount > forge.MaxPushBoundaryEntries || remaining < 0 {
				return errors.New("push inventory frontier budget exceeded")
			}
			frontier[oid] = true
			return nil
		}
		if !introduced[r.NewSHA] {
			if err := addBoundary(r.NewSHA); err != nil {
				return p, err
			}
		}
		for _, c := range r.Commits {
			for _, parent := range c.Parents {
				if !introduced[parent] {
					if err := addBoundary(parent); err != nil {
						return p, err
					}
				}
			}
		}
		for oid := range frontier {
			if source == "" {
				return p, errors.New("unaccounted prepared parent")
			}
			_, diagnostic, err := reader.run(ctx, "", "merge-base", "--is-ancestor", oid, source)
			if err != nil {
				return p, reader.failure("prepared boundary proof", diagnostic, err)
			}
			r.Boundary = append(r.Boundary, oid)
		}
		slices.Sort(r.Boundary)
	}
	after, reason, err := reader.diffHistory(ctx)
	if err != nil {
		return p, err
	}
	if reason != "" || !before.same(after) {
		return p, errors.New("push inventory history changed")
	}
	p.Complete = true
	return p, forge.ValidatePreparedPush(p)
}

// ReadPreparedRef distinguishes absence from a failed authority lease read.
func (r *Repo) ReadPreparedRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	full, err := s.branch(ctx, ref)
	if err != nil {
		return "", err
	}
	values, err := s.read(ctx, full)
	return values[full], err
}
