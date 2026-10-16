package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *session) deleteRefs(ctx context.Context, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	var input strings.Builder
	for _, ref := range refs {
		input.WriteString("delete " + ref + "\n")
	}
	_, err := s.command(ctx, input.String(), "update-ref", "--stdin")
	return err
}

// maintainCache runs under the cache lock after the operation has completed.
// Failures are opportunistic: leave the old timestamp so a later operation retries.
func (s *session) maintainCache(ctx context.Context) error {
	if s.r.cfg.GCInterval < 0 && s.r.cfg.GCThresholdBytes < 0 {
		return nil
	}
	stamp := filepath.Join(s.dir, "openmerge-gc")
	info, err := os.Stat(stamp)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(stamp, nil, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	due := info != nil && s.r.cfg.GCInterval > 0 && time.Since(info.ModTime()) >= s.r.cfg.GCInterval
	if !due && s.r.cfg.GCThresholdBytes > 0 {
		out, err := s.command(ctx, "", "count-objects", "-v")
		if err != nil {
			return err
		}
		var size int64
		for line := range strings.SplitSeq(out, "\n") {
			key, value, _ := strings.Cut(line, ": ")
			if key == "size" || key == "size-pack" {
				kib, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return err
				}
				size += kib * 1024
			}
		}
		due = size >= s.r.cfg.GCThresholdBytes
	}
	if !due {
		return nil
	}
	// Remove refs left by older versions or interrupted operations as well.
	refs, err := s.command(ctx, "", "for-each-ref", "--format=%(refname)", "refs/openmerge-cache/pr/")
	if err != nil {
		return err
	}
	if err := s.deleteRefs(ctx, strings.Fields(refs)); err != nil {
		return err
	}
	if _, err := s.command(ctx, "", "gc", "--prune=now", "--quiet"); err != nil {
		return err
	}
	return os.WriteFile(stamp, nil, 0o600)
}
