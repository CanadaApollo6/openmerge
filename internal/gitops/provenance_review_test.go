package gitops

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestProvenanceInventoryLateBaseParentCannotHideAgent(t *testing.T) {
	f := newProvenanceFixture(t)
	agent := f.commit("agent\n\nAgent-Session: a:one\n", []string{f.root})
	raw := "tree " + f.tree + "\nparent " + f.root + "\nauthor Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\nparent " + agent + "\n\nbase with ignored late parent\n"
	base := gitCommand(t, f.dir, raw, nil, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	if strings.Contains(gitCommand(t, f.dir, "", nil, "rev-list", base), agent) {
		t.Fatal("Git honored the fake late parent")
	}
	head := f.commit("head\n", []string{base, agent})
	inv := f.read(f.request(head, base, base), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, agent, head)
	for _, c := range inv.Commits {
		if c.ObjectID == agent && provenance.Classify(c.Declarations) != provenance.Mixed {
			t.Fatal("agent evidence hidden")
		}
	}
	// The same malformed shape in an original must produce pending, with no
	// identities guessed from arbitrary header positions.
	malformed := f.read(f.request(base, f.root, f.root), provenance.DefaultInventoryLimits())
	expectPendingInventory(t, malformed)
	if malformed.Complete || malformed.Diagnostic.Resource != "commit_shape" || malformed.Diagnostic.Outcome != provenance.ReadPending {
		t.Fatalf("malformed = %+v", malformed)
	}
}

func TestProvenanceMappingIgnoresLateFakeParent(t *testing.T) {
	f := newProvenanceFixture(t)
	head := f.commit("source\n", []string{f.root})
	limits := provenance.DefaultInventoryLimits()
	inv := f.read(f.request(head, f.root, f.root), limits)
	raw := "tree " + f.tree + "\nparent " + f.root + "\nauthor Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\nparent " + head + "\n\nnot a merge\n"
	result := gitCommand(t, f.dir, raw, nil, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	build := provenance.BuildObservation{ID: "fake-merge", Stage: "tested", Method: "merge", Base: f.root, Result: result}
	mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits)
	if mapped.Complete || mapped.Diagnostic.Resource != "merge_shape" {
		t.Fatalf("fake merge = %+v", mapped)
	}
}

func TestProvenanceInventoryOctopusThirdSideAgent(t *testing.T) {
	f := newProvenanceFixture(t)
	left := f.commit("left\n", []string{f.root})
	second := f.commit("second\n", []string{f.root})
	third := f.commit("third\n\nAgent-Session: a:third\n", []string{f.root})
	head := f.commit("octopus\n", []string{left, second, third})
	inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, left, second, third, head)
	for _, c := range inv.Commits {
		if c.ObjectID == third && provenance.Classify(c.Declarations) != provenance.Mixed {
			t.Fatal("third side agent lost")
		}
	}
}

func TestProvenanceInventoryTrailerProcessFailureIsPending(t *testing.T) {
	f := newProvenanceFixture(t)
	head := f.commit("human\n", []string{f.root})
	git, err := exec.LookPath("git")
	must(t, err)
	shim := t.TempDir()
	// Only interpret-trailers fails: init, version, graph queries, metadata and
	// body reads still execute the real Git selected by this test run.
	script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = interpret-trailers ]; then exit 73; fi\ndone\nexec '" + strings.ReplaceAll(git, "'", "'\\''") + "' \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o700))
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
	expectPendingInventory(t, inv)
	if inv.Complete || inv.Diagnostic.Outcome != provenance.ReadPending || inv.Diagnostic.Resource != "trailers" || len(inv.Commits) != 1 || inv.Commits[0].Complete || len(inv.Commits[0].Declarations) != 0 {
		t.Fatalf("failed trailer process = %+v", inv)
	}
}

func TestProvenanceInventoryFrontierIgnoresLargeBaseHistory(t *testing.T) {
	f := newProvenanceFixture(t)
	var input strings.Builder
	// Import a small packed history without thousands of child processes or
	// loose objects. It exceeds the old whole-history ceiling.
	count := provenance.DefaultInventoryLimits().GraphCommits + 1
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&input, "commit refs/heads/history\nmark :%d\ncommitter Human <human@example.test> 1 +0000\ndata 2\nx\n", i)
		if i == 1 {
			fmt.Fprintf(&input, "from %s\n", f.root)
		} else {
			fmt.Fprintf(&input, "from :%d\n", i-1)
		}
		input.WriteByte('\n')
	}
	gitCommand(t, f.dir, input.String(), nil, "fast-import", "--quiet")
	base := gitCommand(t, f.dir, "", nil, "rev-parse", "refs/heads/history")
	head := f.commit("small PR\n", []string{base})
	limits := provenance.DefaultInventoryLimits()
	limits.GraphCommits = 1
	inv := f.read(f.request(head, base, base), limits)
	expectInventoryIDs(t, inv, head)
	build := provenance.BuildObservation{ID: "small-ff", Stage: "landed", Method: "fast-forward", Base: base, Result: head}
	if mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, inv, build, limits); !mapped.Complete {
		t.Fatalf("large base mapping = %+v", mapped)
	}
}

// packedProvenanceChain creates a real REF_DELTA pack. Each delta inserts a
// small commit while depending on the preceding reconstructed commit. index-pack
// verifies the pack, and the test limits remain KiB rather than hundreds of MiB.
func packedProvenanceChain(t *testing.T, f *provenanceFixture, sizes ...int) string {
	t.Helper()
	var pack bytes.Buffer
	pack.WriteString("PACK")
	must(t, binary.Write(&pack, binary.BigEndian, uint32(2)))
	must(t, binary.Write(&pack, binary.BigEndian, uint32(len(sizes))))
	var previous []byte
	var previousID [sha1.Size]byte
	for i, size := range sizes {
		raw := []byte("tree " + f.tree + "\nparent " + f.root + "\nauthor Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\n\nsubject " + strconv.Itoa(i) + "\n\nAgent-Session: a:one\n")
		if size > len(raw) {
			raw = append(raw, bytes.Repeat([]byte("x"), size-len(raw))...)
		}
		kind, content := byte(1), raw
		if i > 0 {
			kind = 7
			content = append(deltaInteger(len(previous)), deltaInteger(len(raw))...)
			for j := 0; j < len(raw); j += 127 {
				part := raw[j:min(j+127, len(raw))]
				content = append(content, byte(len(part)))
				content = append(content, part...)
			}
		}
		n := len(content)
		first := kind<<4 | byte(n&15)
		n >>= 4
		for n > 0 {
			pack.WriteByte(first | 128)
			first = byte(n & 127)
			n >>= 7
		}
		pack.WriteByte(first)
		if kind == 7 {
			pack.Write(previousID[:])
		}
		w := zlib.NewWriter(&pack)
		_, err := w.Write(content)
		must(t, err)
		must(t, w.Close())
		previous, previousID = raw, sha1.Sum(append(fmt.Appendf(nil, "commit %d%c", len(raw), 0), raw...))
	}
	checksum := sha1.Sum(pack.Bytes())
	pack.Write(checksum[:])
	path := filepath.Join(f.dir, "objects", "pack", "fixture.pack")
	must(t, os.WriteFile(path, pack.Bytes(), 0o600))
	gitCommand(t, f.dir, "", nil, "index-pack", path)
	head := fmt.Sprintf("%x", previousID)
	info := strings.Fields(gitCommand(t, f.dir, head+"\n", nil, "cat-file", "--batch-check=%(objectsize) %(objectsize:disk) %(deltabase)"))
	if len(info) != 3 || strings.Trim(info[2], "0") == "" {
		t.Fatalf("fixture is not deltified: %v", info)
	}
	return head
}

func deltaInteger(n int) []byte {
	var out []byte
	for n >= 128 {
		out = append(out, byte(n&127)|128)
		n >>= 7
	}
	return append(out, byte(n))
}

func TestProvenanceInventoryPackedDeltaLimits(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sizes        []int
		bytes, depth int
		complete     bool
	}{
		{"oversized base", []int{(32 << 10) + 1, 0}, 32 << 10, 128, false},
		{"cumulative chain", []int{12 << 10, 12 << 10, 0}, 20 << 10, 128, false},
		{"chain depth", []int{1024, 512, 0}, 32 << 10, 2, false},
		{"within limits", []int{1024, 512, 0}, 32 << 10, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProvenanceFixture(t)
			head := packedProvenanceChain(t, f, tc.sizes...)
			limits := provenance.DefaultInventoryLimits()
			limits.GraphBytes, limits.DeltaDepth = tc.bytes, tc.depth
			inv := f.read(f.request(head, f.root, f.root), limits)
			if tc.complete {
				expectInventoryIDs(t, inv, head)
			} else {
				expectPendingInventory(t, inv)
				if inv.Complete || inv.Diagnostic.Outcome != provenance.LimitExceeded || inv.Diagnostic.Resource != "expansion" {
					t.Fatalf("delta budget = %+v", inv)
				}
			}
		})
	}
}

func TestProvenanceMappingDoesNotCreateTrailerParser(t *testing.T) {
	f := newProvenanceFixture(t)
	head := f.commit("source\n", []string{f.root})
	limits := provenance.DefaultInventoryLimits()
	inv := f.read(f.request(head, f.root, f.root), limits)
	build := provenance.BuildObservation{ID: "ff", Stage: "landed", Method: "fast-forward", Base: f.root, Result: head}
	// Mapping has no parser storage dependency, including when scratch is absent.
	mapped := MapProvenanceBuild(t.Context(), f.dir, filepath.Join(f.scratch, "absent"), inv, build, limits)
	if !mapped.Complete || !slices.Equal(mapped.Commits, []provenance.CommitMapping{{Original: head, Build: head}}) {
		t.Fatalf("mapping = %+v", mapped)
	}
}

func TestOriginalProvenanceFieldsRequiresGitHeaderOrder(t *testing.T) {
	f := newProvenanceFixture(t)
	prefix := "tree " + f.tree + "\n"
	author, committer := "author Human <human@example.test> 1 +0000\n", "committer Human <human@example.test> 1 +0000\n"
	for i, headers := range []string{prefix + committer + author, author + prefix + committer, prefix + author + committer + "parent " + f.root + "\n", prefix + author + committer + author} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			head := gitCommand(t, f.dir, headers+"\nhuman\n", nil, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
			inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
			expectPendingInventory(t, inv)
			if inv.Complete || inv.Diagnostic.Outcome != provenance.ReadPending || inv.Diagnostic.Resource != "commit_shape" {
				t.Fatalf("header order = %+v", inv)
			}
		})
	}
}
