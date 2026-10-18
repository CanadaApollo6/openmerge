package gitops

import (
	"context"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLocalConfigNeverHydratesMissingObjects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data := "version: 1\nqueues:\n  - name: default\n"
	commit := f.commit(f.base, "pinned config", map[string]string{".openmerge.yml": data})
	f.push("main", commit, false)
	// The read must fail while the object exists only on the remote.
	if _, err := f.g.LocalConfig(ctx, testRepo, commit); err == nil {
		t.Fatal("read hydrated remote commit")
	}
	_, err := f.g.GoverningFiles(ctx, testRepo, forge.GoverningFilesRequest{CommitSHA: commit})
	must(t, err)
	// Normal ingestion populates the cache; the read may now use it with no
	// credentials, transport, mutable branch resolution or remote callback.
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		t.Fatal("read resolved remote")
		return "", Auth{}, nil
	}
	got, err := f.g.LocalConfig(ctx, testRepo, commit)
	if err != nil || string(got) != data {
		t.Fatalf("local read: %q %v", got, err)
	}
	if _, err := f.g.LocalConfig(ctx, testRepo, strings.Repeat("9", 40)); err == nil {
		t.Fatal("missing local commit accepted")
	}
	if _, err := f.g.LocalConfig(ctx, testRepo, "main"); err == nil {
		t.Fatal("mutable ref accepted")
	}
}
