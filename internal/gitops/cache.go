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

// baseRef keeps the newest base commit's history across collection. The cache
// has no other durable refs, so without it `gc --prune=now` would delete the
// whole history and the next operation would fetch it all again.
const baseRef = "refs/openmerge-cache/base"

// maintainCache runs under the cache lock after the operation has completed.
// Failures are opportunistic: leave the old timestamp so a later operation retries.
func (s *session) maintainCache(ctx context.Context) error {
	// Retry release failures independently of the GC schedule or disabled GC.
	if state, ok := acquisitionStates.Load(s.dir); ok {
		if err := s.sweepAcquisitions(ctx, state.(map[string]bool)); err != nil {
			return err
		}
	}
	if s.retain != "" {
		if _, err := s.command(ctx, "", "update-ref", baseRef, s.retain); err != nil {
			return err
		}
	}

	if s.r.cfg.GCInterval < 0 && s.r.cfg.GCThresholdBytes < 0 {
		return nil
	}
	// The stamp's modification time is the last successful collection, and its
	// content is the cache size that collection left. The size trigger measures
	// growth since then: retained history alone must never make every
	// operation collect again.
	stamp := filepath.Join(s.dir, "openmerge-gc")
	data, err := os.ReadFile(stamp)
	var info os.FileInfo
	if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(stamp, nil, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if info, err = os.Stat(stamp); err != nil {
		return err
	}
	due := info != nil && s.r.cfg.GCInterval > 0 && time.Since(info.ModTime()) >= s.r.cfg.GCInterval
	if !due && s.r.cfg.GCThresholdBytes > 0 {
		size, err := s.cacheSize(ctx)
		if err != nil {
			return err
		}
		baseline, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		due = size-max(baseline, 0) >= s.r.cfg.GCThresholdBytes
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
	size, err := s.cacheSize(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte(strconv.FormatInt(size, 10)+"\n"), 0o600)
}

// cacheSize is the bytes of loose and packed objects, from `git count-objects`.
func (s *session) cacheSize(ctx context.Context) (int64, error) {
	out, err := s.command(ctx, "", "count-objects", "-v")
	if err != nil {
		return 0, err
	}
	var size int64
	for line := range strings.SplitSeq(out, "\n") {
		key, value, _ := strings.Cut(line, ": ")
		if key == "size" || key == "size-pack" {
			kib, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return 0, err
			}
			size += kib * 1024
		}
	}
	return size, nil
}
