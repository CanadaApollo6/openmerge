package gitops

import (
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestProvenanceInventoryPackLargerThanProcessCap(t *testing.T) {
	f := newProvenanceFixture(t)
	// Stream incompressible data to disk; neither the Go fixture nor the reader
	// needs to retain this blob in memory. Only commit objects are inventoried.
	blobPath := filepath.Join(f.scratch, "blob")
	blob, err := os.Create(blobPath)
	must(t, err)
	_, err = io.CopyN(blob, rand.Reader, 32<<20)
	must(t, err)
	must(t, blob.Close())
	id := gitCommand(t, f.dir, "", nil, "hash-object", "-w", blobPath)
	f.tree = gitCommand(t, f.dir, "100644 blob "+id+"\tlarge.bin\n", nil, "mktree")
	head := f.commit("small PR\n", []string{f.root})
	prefix := filepath.Join(f.dir, "objects", "pack", "pack")
	hash := gitCommand(t, f.dir, head+"\n", nil, "pack-objects", "--revs", "--window=0", prefix)
	gitCommand(t, f.dir, "", nil, "prune-packed")
	pack, err := os.Stat(prefix + "-" + hash + ".pack")
	must(t, err)
	limits := provenance.DefaultInventoryLimits()
	limits.GraphBytes = 8 << 20
	capBytes := limits.GraphBytes + provenanceProcessOverhead
	if pack.Size() <= int64(capBytes) || pack.Size() > 34<<20 {
		t.Fatalf("expected modest pack larger than %d-byte cap, got %d", capBytes, pack.Size())
	}
	t.Logf("%d-byte pack; %d-byte process cap", pack.Size(), capBytes)

	// Source-local knobs must not restore Git's large default windows/caches.
	for _, key := range []string{"core.packedGitWindowSize", "core.packedGitLimit", "core.deltaBaseCacheLimit"} {
		gitCommand(t, f.dir, "", nil, "config", key, "1g")
	}
	// Control: this same walk fails under the same cap without the pinned knobs.
	cmd := exec.Command("prlimit", "--as="+strconv.Itoa(capBytes), "--", "git", "--git-dir="+f.dir, "-c", "core.commitGraph=false", "rev-list", "--count", head)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot be mapped") || !strings.Contains(string(out), "Cannot allocate memory") {
		t.Fatalf("uncorrected control: %v, %s", err, out)
	}
	inv := f.read(f.request(head, f.root, f.root), limits)
	expectInventoryIDs(t, inv, head)
	r, err := newProvenanceReader(t.Context(), f.dir, limits)
	must(t, err)
	defer func() { _ = r.close() }()
	for _, mode := range []string{"--batch", "--batch-check"} {
		cmd := r.command(t.Context(), "cat-file", mode)
		cmd.Stdin = strings.NewReader(head + "\n")
		out, err := cmd.CombinedOutput()
		must(t, err)
		if !strings.HasPrefix(string(out), head+" commit ") {
			t.Fatalf("%s response = %q", mode, out)
		}
	}
	must(t, r.close())
	build := provenance.BuildObservation{ID: "large-pack-ff", Stage: "landed", Method: "fast-forward", Base: f.root, Result: head}
	if mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits); !mapped.Complete {
		t.Fatalf("large-pack mapping = %+v", mapped.Diagnostic)
	}

	// A large pack is harmless, but a large reconstructed delta base still
	// exhausts the expansion budget in this same repository.
	expanded := packedProvenanceChain(t, f, 32<<20, 512, 0)
	inv = f.read(f.request(expanded, f.root, f.root), limits)
	if inv.Complete || inv.Diagnostic.Outcome != provenance.LimitExceeded || inv.Diagnostic.Resource != "expansion" {
		t.Fatalf("oversized expansion = %+v", inv.Diagnostic)
	}
}

func TestProvenanceReaderMemoryConfigOverridesRepository(t *testing.T) {
	f := newProvenanceFixture(t)
	for _, key := range []string{"core.packedGitWindowSize", "core.packedGitLimit", "core.deltaBaseCacheLimit"} {
		gitCommand(t, f.dir, "", nil, "config", key, "1g")
	}
	for _, tc := range []struct {
		graph, window, mapped int
	}{
		{128 << 20, 1 << 20, 8 << 20},
		{8 << 20, 768 << 10, 3 << 20},
		{1, 512 << 10, 2 << 20},
	} {
		t.Run(strconv.Itoa(tc.graph), func(t *testing.T) {
			limits := provenance.DefaultInventoryLimits()
			limits.GraphBytes = tc.graph
			r, err := newProvenanceReader(t.Context(), f.dir, limits)
			must(t, err)
			defer func() { _ = r.close() }()
			for key, want := range map[string]int{
				"core.packedGitWindowSize": tc.window,
				"core.packedGitLimit":      tc.mapped,
				"core.deltaBaseCacheLimit": 0,
			} {
				out, err := r.run(t.Context(), 128, "config", "get", "--type=int", key)
				must(t, err)
				if strings.TrimSpace(out) != strconv.Itoa(want) {
					t.Fatalf("effective %s = %q, want %d", key, out, want)
				}
			}
			must(t, r.close())
		})
	}
}
