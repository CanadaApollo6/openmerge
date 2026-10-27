package gitops

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestProvenanceInventoryIndexLargerThanFixedOverhead(t *testing.T) {
	f := newProvenanceFixture(t)
	var input strings.Builder
	// One fast-import avoids hundreds of thousands of processes/loose files.
	// 610,000 tiny unique blobs make the idx alone exceed the old 16 MiB
	// headroom, while keeping the pack and CI fixture modest.
	for i := range 610_000 {
		body := strconv.Itoa(i)
		fmt.Fprintf(&input, "blob\ndata %d\n%s\n", len(body), body)
	}
	fmt.Fprintf(&input, "commit refs/heads/pr\ncommitter Human <human@example.test> 1 +0000\ndata 9\nsmall PR\n\nfrom %s\n\n", f.root)
	gitCommand(t, f.dir, input.String(), nil, "fast-import", "--quiet")
	head := gitCommand(t, f.dir, "", nil, "rev-parse", "refs/heads/pr")
	indexes, err := filepath.Glob(filepath.Join(f.dir, "objects/pack/*.idx"))
	must(t, err)
	if len(indexes) != 1 {
		t.Fatalf("expected one pack index, got %v", indexes)
	}
	idx, err := os.Stat(indexes[0])
	must(t, err)
	if idx.Size() <= provenanceProcessOverhead {
		t.Fatalf("idx size %d does not exceed old headroom", idx.Size())
	}
	limits := provenance.DefaultInventoryLimits()
	limits.GraphBytes = 8 << 20
	for _, withRev := range []bool{false, true} {
		t.Run(fmt.Sprintf("rev=%t", withRev), func(t *testing.T) {
			if withRev {
				gitCommand(t, f.dir, "", nil, "index-pack", "--rev-index", strings.TrimSuffix(indexes[0], ".idx")+".pack")
			} else {
				rev := strings.TrimSuffix(indexes[0], ".idx") + ".rev"
				if err := os.Remove(rev); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			gitCommand(t, f.dir, "", nil, "multi-pack-index", "write")
			indexBytes, err := provenanceIndexBytes(t.Context(), f.dir)
			must(t, err)
			t.Logf("idx=%d; index allowance=%d; process cap=%d", idx.Size(), indexBytes, limits.GraphBytes+provenanceProcessOverhead+indexBytes)
			// Control: the old fixed-headroom cap cannot inventory even this
			// single-commit frontier, with the same pinned window settings.
			r, err := newProvenanceReader(t.Context(), f.dir, limits)
			must(t, err)
			r.indexBytes = 0
			_, err = r.run(t.Context(), 128, "rev-list", head, "^"+f.root)
			must(t, r.close())
			var exceeded *provenanceLimitError
			if !errors.As(err, &exceeded) || exceeded.resource != "expansion" {
				t.Fatalf("fixed-headroom control = %v, want expansion limit", err)
			}
			inv := f.read(f.request(head, f.root, f.root), limits)
			expectInventoryIDs(t, inv, head)
		})
	}
}

func TestProvenanceIndexSizingFailsClosed(t *testing.T) {
	for _, name := range []string{"objects", "objects/pack", "objects/pack/outside.idx", "objects/pack/outside.rev", "objects/pack/multi-pack-index", "objects/info/alternates", "commondir"} {
		t.Run(name, func(t *testing.T) {
			f := newProvenanceFixture(t)
			outside := t.TempDir()
			if name != "objects" && name != "objects/pack" {
				outside = filepath.Join(outside, "file")
				must(t, os.WriteFile(outside, []byte("external"), 0o600))
			}
			path := filepath.Join(f.dir, name)
			must(t, os.RemoveAll(path))
			must(t, os.Symlink(outside, path))
			inv := f.read(f.request(f.root, f.root, f.root), provenance.DefaultInventoryLimits())
			if inv.Complete || inv.Diagnostic.Outcome != provenance.ReadPending || inv.Diagnostic.Resource != "reader" {
				t.Fatalf("external path accepted: %+v", inv.Diagnostic)
			}
		})
	}
	for _, content := range []string{"", "../../../external/objects\n"} {
		f := newProvenanceFixture(t)
		must(t, os.WriteFile(filepath.Join(f.dir, "objects/info/alternates"), []byte(content), 0o600))
		if _, err := newProvenanceReader(t.Context(), f.dir, provenance.DefaultInventoryLimits()); err == nil {
			t.Fatal("alternate file accepted")
		}
	}
}

func TestProvenanceIndexAllowanceAndCeiling(t *testing.T) {
	f := newProvenanceFixture(t)
	pack := filepath.Join(f.dir, "objects/pack")
	for name, size := range map[string]int{"a.idx": 2400, "a.rev": 100, "b.idx": 2400, "multi-pack-index": 200} {
		must(t, os.WriteFile(filepath.Join(pack, name), make([]byte, size), 0o600))
	}
	got, err := provenanceIndexBytes(t.Context(), f.dir)
	must(t, err)
	if want := 2400 + 100 + 2400 + 200 + 1200; got != want {
		t.Fatalf("index allowance = %d, want %d", got, want)
	}
	file, err := os.Create(filepath.Join(pack, "huge.idx"))
	must(t, err)
	must(t, file.Truncate(1<<40)) // Sparse: no large allocation or disk use.
	must(t, file.Close())
	r, err := newProvenanceReader(t.Context(), f.dir, provenance.DefaultInventoryLimits())
	must(t, err)
	must(t, r.close())
	cmd := r.command(t.Context(), "version")
	if cmd.Args[1] != "--as="+strconv.Itoa(provenanceProcessCeiling) {
		t.Fatalf("ceiling not applied: %v", cmd.Args)
	}
}

func TestProvenanceReaderCloseAfterCleanExit(t *testing.T) {
	f := newProvenanceFixture(t)
	r, err := newProvenanceReader(t.Context(), f.dir, provenance.DefaultInventoryLimits())
	must(t, err)
	// Model a late cancellation observed by close while the batch process
	// exits cleanly on EOF. Its command context remains live so no kill races
	// with the clean-exit assertion.
	ctx, cancel := context.WithCancel(t.Context())
	r.ctx = ctx
	cancel()
	must(t, r.close())
}

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
	indexBytes, err := provenanceIndexBytes(t.Context(), f.dir)
	must(t, err)
	capBytes := limits.GraphBytes + provenanceProcessOverhead + indexBytes
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
