package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/gitops"
)

func TestGitOperationLogs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats gitops.OperationStats
		level string
	}{
		{"ordinary", gitops.OperationStats{}, "DEBUG"},
		{"below thresholds", gitops.OperationStats{Wait: time.Second - 1, Hold: 10*time.Second - 1, Maintenance: 5*time.Second - 1}, "DEBUG"},
		{"wait threshold", gitops.OperationStats{Wait: time.Second}, "INFO"},
		{"hold threshold", gitops.OperationStats{Hold: 10 * time.Second}, "INFO"},
		{"maintenance threshold", gitops.OperationStats{Maintenance: 5 * time.Second}, "INFO"},
		{"gc", gitops.OperationStats{GCRan: true, GC: time.Second, GCOutcome: gitops.GCOK, CacheBytesMeasured: true, CacheBytes: 1024}, "INFO"},
		{"failed", gitops.OperationStats{GCRan: true, GCOutcome: gitops.GCFailed}, "WARN"},
		{"timed out", gitops.OperationStats{GCRan: true, GCOutcome: gitops.GCTimedOut}, "WARN"},
		{"backoff", gitops.OperationStats{GCOutcome: gitops.GCSkippedBackoff}, "DEBUG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			tc.stats.Repository, tc.stats.Operation = "owner/repo", "build-test-ref"
			observeGitOperations(logger)(tc.stats)
			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{
				"level": tc.level, "msg": "git cache operation", "repository": "owner/repo", "operation": "build-test-ref",
				"wait_ms": tc.stats.Wait.Seconds() * 1000, "hold_ms": tc.stats.Hold.Seconds() * 1000,
				"maintenance_ms": tc.stats.Maintenance.Seconds() * 1000,
				"gc_ran":         tc.stats.GCRan, "gc_ms": tc.stats.GC.Seconds() * 1000, "gc_outcome": string(tc.stats.GCOutcome),
			} {
				if record[key] != want {
					t.Fatalf("%s = %v, want %v", key, record[key], want)
				}
			}
			value, measured := record["cache_bytes"]
			if measured != tc.stats.CacheBytesMeasured || measured && value != float64(tc.stats.CacheBytes) {
				t.Fatalf("cache bytes = %v, measured = %v", value, measured)
			}
		})
	}
	if observeGitOperations(nil) != nil {
		t.Fatal("nil logger enabled observation")
	}
}
