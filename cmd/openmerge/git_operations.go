package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

const (
	gitWaitLogThreshold        = time.Second
	gitHoldLogThreshold        = 10 * time.Second
	gitMaintenanceLogThreshold = 5 * time.Second
)

func observeGitOperations(logger *slog.Logger) func(gitops.OperationStats) {
	if logger == nil {
		return nil
	}
	return func(s gitops.OperationStats) {
		level := slog.LevelDebug
		if s.Wait >= gitWaitLogThreshold || s.Hold >= gitHoldLogThreshold || s.Maintenance >= gitMaintenanceLogThreshold || s.GCRan {
			level = slog.LevelInfo
		}
		if s.GCOutcome == gitops.GCFailed || s.GCOutcome == gitops.GCTimedOut || s.Dropped > 0 {
			level = slog.LevelWarn
		}
		attrs := []any{
			"repository", s.Repository, "operation", s.Operation,
			"wait_ms", s.Wait.Seconds() * 1000, "hold_ms", s.Hold.Seconds() * 1000,
			"maintenance_ms", s.Maintenance.Seconds() * 1000,
			"gc_ran", s.GCRan, "gc_ms", s.GC.Seconds() * 1000, "gc_outcome", s.GCOutcome,
			"dropped", s.Dropped,
		}
		if s.CacheBytesMeasured {
			attrs = append(attrs, "cache_bytes", s.CacheBytes)
		}
		logger.Log(context.Background(), level, "git cache operation", attrs...)
	}
}

func observeGitCacheOperations(logger *slog.Logger, cache *status.GitCache) func(gitops.OperationStats) {
	logOperation := observeGitOperations(logger)
	if cache == nil {
		return logOperation
	}
	return func(s gitops.OperationStats) {
		cache.Observe(s)
		if logOperation != nil {
			logOperation(s)
		}
	}
}
