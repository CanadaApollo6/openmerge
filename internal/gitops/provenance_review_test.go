package gitops

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
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
// small commit while depending on the preceding reconstructed commit.
func packedProvenanceChain(t *testing.T, f *provenanceFixture, sizes ...int) string {
	t.Helper()
	return packedProvenanceChainKind(t, f, false, sizes...)
}

// Stream base padding so the process-cap fixture never allocates its expanded
// base in Go. The on-disk pack remains tens of KiB; index-pack verifies it.
func packedProvenanceChainKind(t *testing.T, f *provenanceFixture, ofs bool, sizes ...int) string {
	t.Helper()
	var pack bytes.Buffer
	pack.WriteString("PACK")
	must(t, binary.Write(&pack, binary.BigEndian, uint32(2)))
	must(t, binary.Write(&pack, binary.BigEndian, uint32(len(sizes))))
	previousSize, previousOffset := 0, 0
	var previousID [sha1.Size]byte
	for i, size := range sizes {
		offset := pack.Len()
		raw := []byte("tree " + f.tree + "\nparent " + f.root + "\nauthor Human <human@example.test> 1 +0000\ncommitter Human <human@example.test> 1 +0000\n\nsubject " + strconv.Itoa(i) + "\n\nAgent-Session: a:one\n")
		size = max(size, len(raw))
		// Only standalone bases need large padding in these fixtures.
		if i > 0 {
			raw = append(raw, bytes.Repeat([]byte("x"), size-len(raw))...)
		}
		kind, content := byte(1), raw
		if i > 0 {
			kind = 7
			if ofs {
				kind = 6
			}
			content = append(deltaInteger(previousSize), deltaInteger(len(raw))...)
			for j := 0; j < len(raw); j += 127 {
				part := raw[j:min(j+127, len(raw))]
				content = append(content, byte(len(part)))
				content = append(content, part...)
			}
		}
		n := len(content)
		if i == 0 {
			n = size
		}
		first := kind<<4 | byte(n&15)
		n >>= 4
		for n > 0 {
			pack.WriteByte(first | 128)
			first = byte(n & 127)
			n >>= 7
		}
		pack.WriteByte(first)
		switch kind {
		case 7:
			pack.Write(previousID[:])
		case 6:
			distance := offset - previousOffset
			encoded := []byte{byte(distance & 127)}
			for distance >>= 7; distance > 0; distance >>= 7 {
				distance--
				encoded = append([]byte{byte(distance&127) | 128}, encoded...)
			}
			pack.Write(encoded)
		}
		w := zlib.NewWriter(&pack)
		_, err := w.Write(content)
		must(t, err)
		hash := sha1.New()
		_, err = fmt.Fprintf(hash, "commit %d%c", size, 0)
		must(t, err)
		_, err = hash.Write(raw)
		must(t, err)
		if i == 0 {
			padding := bytes.Repeat([]byte("x"), 32<<10)
			for remaining := size - len(raw); remaining > 0; {
				part := padding[:min(remaining, len(padding))]
				_, err = w.Write(part)
				must(t, err)
				_, err = hash.Write(part)
				must(t, err)
				remaining -= len(part)
			}
		}
		must(t, w.Close())
		copy(previousID[:], hash.Sum(nil))
		previousSize, previousOffset = size, offset
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

func TestProvenanceInventoryMultiPackExpansionCap(t *testing.T) {
	for _, ofs := range []bool{false, true} {
		name := "REF_DELTA"
		if ofs {
			name = "OFS_DELTA"
		}
		t.Run(name, func(t *testing.T) {
			f := newProvenanceFixture(t)
			// The hidden base must exceed the fixed process overhead, not just
			// the advisory byte budget. Streaming keeps this fixture small.
			head := packedProvenanceChainKind(t, f, ofs, 32<<20, 512, 0)
			pack, err := os.Stat(filepath.Join(f.dir, "objects", "pack", "fixture.pack"))
			must(t, err)
			if pack.Size() > 128<<10 {
				t.Fatalf("fixture pack too large: %d bytes", pack.Size())
			}
			t.Logf("streamed 32 MiB base into %d-byte pack", pack.Size())
			limits := provenance.DefaultInventoryLimits()
			limits.GraphBytes = 20 << 10
			control := f.read(f.request(head, f.root, f.root), limits)
			if control.Complete || control.Diagnostic.Outcome != provenance.LimitExceeded || control.Diagnostic.Resource != "expansion" {
				t.Fatalf("visible chain = %+v", control.Diagnostic)
			}
			mid := gitCommand(t, f.dir, head+"\n", nil, "cat-file", "--batch-check=%(deltabase)")
			prefix := filepath.Join(f.dir, "objects", "pack", "pack")
			hash := gitCommand(t, f.dir, mid+"\n", nil, "pack-objects", "--window=0", "--no-reuse-delta", prefix)
			gitCommand(t, f.dir, "", nil, "multi-pack-index", "write", "--preferred-pack=pack-"+hash+".pack")
			info := strings.Fields(gitCommand(t, f.dir, mid+"\n", nil, "cat-file", "--batch-check=%(objectsize) %(deltabase)"))
			if len(info) != 2 || info[0] != "512" || strings.Trim(info[1], "0") != "" {
				t.Fatalf("preferred duplicate is not standalone: %v", info)
			}
			// Both byte and depth metadata checks now pass despite the hidden
			// base. A real body read must still fail under the mandatory cap.
			limits.DeltaDepth = 2
			r, err := newProvenanceReader(t.Context(), f.dir, limits)
			must(t, err)
			defer func() { _ = r.close() }()
			must(t, r.inspectDeltaMetadata(head))
			_, err = r.body(head)
			// Git can recover via the duplicate and return a body after an
			// allocation failure. Closing must still reject that observation.
			err = errors.Join(err, r.close())
			var limitError *provenanceLimitError
			if !errors.As(err, &limitError) || limitError.resource != "expansion" {
				t.Fatalf("batch expansion cap = %v", err)
			}
			inv := f.read(f.request(head, f.root, f.root), limits)
			expectPendingInventory(t, inv)
			if inv.Complete || inv.Diagnostic.Outcome != provenance.LimitExceeded || inv.Diagnostic.Resource != "expansion" {
				t.Fatalf("traversal expansion cap = %+v", inv.Diagnostic)
			}
			// Same objects and advisory depth succeed with more address space:
			// lowering GraphBytes, rather than a metadata rejection, caused failure.
			limits.GraphBytes = 64 << 20
			complete := f.read(f.request(head, f.root, f.root), limits)
			expectInventoryIDs(t, complete, head)
			build := provenance.BuildObservation{ID: "capped-ff", Stage: "landed", Method: "fast-forward", Base: f.root, Result: head}
			if mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, complete, build, limits); !mapped.Complete {
				t.Fatalf("larger-budget mapping = %+v", mapped.Diagnostic)
			}
			limits.GraphBytes = 20 << 10
			mapped := MapProvenanceBuild(t.Context(), f.dir, f.scratch, complete, build, limits)
			if mapped.Complete || mapped.Diagnostic.Outcome != provenance.LimitExceeded || mapped.Diagnostic.Resource != "expansion" {
				t.Fatalf("mapping expansion cap = %+v", mapped.Diagnostic)
			}
		})
	}
}

func TestProvenanceInventorySupervisorUnavailable(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(strconv.FormatBool(broken), func(t *testing.T) {
			f := newProvenanceFixture(t)
			head := f.commit("human\n", []string{f.root})
			git, err := exec.LookPath("git")
			must(t, err)
			bin := t.TempDir()
			must(t, os.Symlink(git, filepath.Join(bin, "git")))
			if broken {
				must(t, os.WriteFile(filepath.Join(bin, "prlimit"), []byte("#!/bin/sh\nexit 73\n"), 0o700))
			}
			t.Setenv("PATH", bin)
			inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
			expectPendingInventory(t, inv)
			if inv.Complete || inv.Diagnostic.Outcome != provenance.ReadPending || len(inv.Commits) != 0 {
				t.Fatalf("unavailable supervisor = %+v", inv)
			}
		})
	}
}

func TestProvenanceInventorySupervisedProcessFailure(t *testing.T) {
	for _, command := range []string{"cat-file", "rev-list", "merge-base"} {
		for _, failure := range []string{"oom", "calloc", "killed", "other"} {
			t.Run(command+"/"+failure, func(t *testing.T) {
				f := newProvenanceFixture(t)
				head := f.commit("human\n", []string{f.root})
				git, err := exec.LookPath("git")
				must(t, err)
				bin := t.TempDir()
				action := "echo 'fatal: out of memory' >&2; exit 128"
				switch failure {
				case "calloc":
					action = "echo 'fatal: calloc failed' >&2; exit 128"
				case "killed":
					action = "kill -KILL $$"
				case "other":
					action = "exit 73"
				}
				script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = " + command + " ]; then " + action + "; fi\ndone\nexec '" + strings.ReplaceAll(git, "'", "'\\''") + "' \"$@\"\n"
				must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
				expectPendingInventory(t, inv)
				outcome, resource := provenance.LimitExceeded, "expansion"
				if failure == "other" {
					outcome, resource = provenance.ReadPending, "commit_shape"
				}
				if inv.Complete || inv.Diagnostic.Outcome != outcome || inv.Diagnostic.Resource != resource {
					t.Fatalf("process failure = %+v", inv.Diagnostic)
				}
			})
		}
	}
}

func TestProvenanceReaderCanceledBatch(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	r, err := newProvenanceReader(ctx, f.dir, provenance.DefaultInventoryLimits())
	must(t, err)
	cancel()
	// CommandContext's kill must retain cancellation rather than becoming an
	// expansion failure. Wait also guarantees the persistent child is reaped.
	if err := r.close(); !errors.Is(err, context.Canceled) || r.batch.ProcessState == nil {
		t.Fatalf("canceled batch = %v, state = %v", err, r.batch.ProcessState)
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
