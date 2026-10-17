package main

import (
	"path/filepath"
	"testing"
)

func TestCacheRepoPathUsesInstallationDirectory(t *testing.T) {
	got := cacheRepoPath("/cache", "172166049", "CanadaApollo6/openmerge")
	want := filepath.Join("/cache", "172166049", "2ff2c0cd2c8ea745c5bc7fd7dd3cadc5b2b4eacb34719001973ff248cd28826a.git")
	if got != want {
		t.Fatalf("cacheRepoPath = %q, want %q", got, want)
	}
}
