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
// Nothing is fetched or rebuilt by the inspection reader. Zero options return
// before any added Git command, preserving legacy I/O and locking exactly.
func (s *session) validatePush(ctx context.Context, repo forge.RepoRef, opts forge.PushOptions, operation forge.PushOperation, refs ...forge.PushRef) error {
	if !opts.AuthorityRequired {
		return nil
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
		for _, oid := range []string{r.OldSHA, r.NewSHA} {
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
	commitCount, pathCount, parentEdges := 0, 0, 0
	read := func(args ...string) (string, error) {
		raw, overflow, err := reader.diffCommand(ctx, remaining, args...)
		if overflow {
			return "", errors.New("push inventory byte budget exceeded")
		}
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
		if r.OldSHA != "" {
			args = append(args, "^"+r.OldSHA)
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
			raw, err := read("cat-file", "commit", oid)
			if err != nil {
				return p, err
			}
			remaining -= len(raw)
			if remaining < 0 {
				return p, errors.New("push inventory metadata budget exceeded")
			}
			header, message, ok := strings.Cut(raw, "\n\n")
			if !ok {
				return p, errors.New("invalid prepared commit")
			}
			c := forge.PushCommit{OID: oid, Parents: []string{}, Message: message, Files: []forge.PushFile{}}
			signature := false
			for _, line := range strings.Split(header, "\n") {
				if strings.HasPrefix(line, "parent ") {
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
			parentEdges += len(c.Parents)
			if parentEdges > forge.MaxPushParentEdges {
				return p, errors.New("push inventory parent-edge budget exceeded")
			}
			// A signature header alone cannot establish trust. Required signatures
			// reject until a verified-signature capability supplies actual proof.
			raw, err = read("ls-tree", "-r", "-l", "-z", "--full-tree", oid)
			if err != nil {
				return p, err
			}
			remaining -= len(raw)
			if remaining < 0 {
				return p, errors.New("push inventory tree budget exceeded")
			}
			for raw != "" {
				entry, rest, ok := strings.Cut(raw, "\x00")
				if !ok {
					return p, errors.New("unterminated prepared tree")
				}
				raw = rest
				meta, path, ok := strings.Cut(entry, "\t")
				fields := strings.Fields(meta)
				if !ok || len(fields) != 4 {
					return p, errors.New("invalid prepared tree entry")
				}
				size := int64(0)
				if fields[1] == "blob" {
					size, err = strconv.ParseInt(fields[3], 10, 64)
					if err != nil {
						return p, err
					}
				} else if fields[1] != "commit" {
					return p, errors.New("unknown prepared tree object")
				}
				c.Files = append(c.Files, forge.PushFile{Path: path, Kind: fields[1], OID: fields[2], Size: size})
				pathCount++
				if pathCount > forge.MaxPushPaths {
					return p, errors.New("push inventory path budget exceeded")
				}
			}
			r.Commits = append(r.Commits, c)
		}
		introduced := map[string]bool{}
		for _, c := range r.Commits {
			introduced[c.OID] = true
		}
		frontier := map[string]bool{}
		if !introduced[r.NewSHA] {
			frontier[r.NewSHA] = true
		}
		for _, c := range r.Commits {
			for _, parent := range c.Parents {
				if !introduced[parent] {
					frontier[parent] = true
				}
			}
		}
		for oid := range frontier {
			if r.OldSHA == "" {
				return p, errors.New("unaccounted prepared parent")
			}
			_, diagnostic, err := reader.run(ctx, "", "merge-base", "--is-ancestor", oid, r.OldSHA)
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
