package gitops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

var errProvenanceShape = errors.New("unexpected commit shape")

// provenanceProcessOverhead covers Git, shared libraries and allocator overhead.
// Index mappings and missing reverse indexes are budgeted separately. The capped
// address space, not delta metadata accounting, enforces the memory bound.
const provenanceProcessOverhead = 16 << 20

// Even repositories with enormous indexes may never raise a reader above 2 GiB.
const provenanceProcessCeiling = 2 << 30

// provenanceReader shares one cat-file process for metadata and original bodies.
// Only this inert adapter requires prlimit; the live transport is unaffected.
type provenanceReader struct {
	ctx        context.Context
	s          *session
	limits     provenance.InventoryLimits
	prlimit    string
	indexBytes int
	batch      *exec.Cmd
	input      io.WriteCloser
	output     *bufio.Reader
	diagnostic *limitedDiffBuffer
	remaining  int
	sizes      map[string]int
	inspected  map[string]bool
	closed     bool
}

func newProvenanceReader(ctx context.Context, gitDir string, limits provenance.InventoryLimits) (*provenanceReader, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	supervisor, err := exec.LookPath("prlimit")
	if err != nil {
		return nil, err
	}
	indexBytes, err := provenanceIndexBytes(ctx, gitDir)
	if err != nil {
		return nil, err
	}
	r := &provenanceReader{indexBytes: indexBytes, ctx: ctx, s: &session{r: &Repo{git: git, cfg: Config{Now: time.Now}}, dir: gitDir, diffRead: true}, limits: limits, prlimit: supervisor, remaining: limits.GraphBytes, sizes: map[string]int{}, inspected: map[string]bool{}}
	r.batch = r.command(ctx, "cat-file", "--batch-command=%(objectname) %(objecttype) %(objectsize) %(deltabase)")
	r.input, err = r.batch.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := r.batch.StdoutPipe()
	if err != nil {
		_ = r.input.Close()
		return nil, err
	}
	r.output = bufio.NewReaderSize(out, 512)
	r.diagnostic = &limitedDiffBuffer{limit: 8192}
	r.batch.Stderr = r.diagnostic
	if err := r.batch.Start(); err != nil {
		_ = r.input.Close()
		_ = out.Close()
		return nil, err
	}
	return r, nil
}

func (r *provenanceReader) command(ctx context.Context, args ...string) *exec.Cmd {
	// Every object read and traversal needs this cap: advisory metadata can
	// under-count duplicate packed representations, and walks parse bodies too.
	cmd := r.s.newCommand(ctx, args...)
	capBytes := int(min(int64(provenanceProcessCeiling), int64(r.limits.GraphBytes)+provenanceProcessOverhead+int64(r.indexBytes)))
	// Reserve at least 7/8 of the cap for Git, indexes, libraries and object
	// reconstruction. Bound pack windows independently of total pack size:
	// default cap 144 MiB -> 8 MiB mapped / 1 MiB windows;
	// lowered cap 24 MiB -> 3 MiB mapped / 768 KiB windows.
	// Git rounds windows to twice the page size; round down here as well so
	// its effective window never exceeds our allocation. Even the minimum cap
	// (>16 MiB) allows 2 MiB mapped and 512 KiB windows on supported Linux.
	mapped := min(8<<20, capBytes/8)
	alignment := 2 * os.Getpagesize()
	window := min(1<<20, mapped/4) / alignment * alignment
	// Minimize the read-side delta cache; Git can still retain its latest base.
	// pack.windowMemory and pack.deltaCacheSize
	// apply to pack-objects (writes), which this reader never invokes. Pack
	// indexes and in-use windows may exceed the mapping target; prlimit remains
	// the authoritative bound. All readers, including mapping and batch modes,
	// use these -c overrides with newCommand's scrubbed Git environment.
	cmd.Args = append([]string{cmd.Args[0], "-c", "core.packedGitWindowSize=" + strconv.Itoa(window), "-c", "core.packedGitLimit=" + strconv.Itoa(mapped), "-c", "core.deltaBaseCacheLimit=0"}, cmd.Args[1:]...)
	cmd.Args = append([]string{r.prlimit, "--as=" + strconv.Itoa(capBytes), "--"}, cmd.Args...)
	cmd.Path = r.prlimit
	cmd.WaitDelay = time.Second
	return cmd
}

func (r *provenanceReader) close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	_ = r.input.Close()
	err := r.batch.Wait()
	processCtx := r.ctx
	if err == nil {
		// A deadline arriving after a clean exit must not turn success into failure.
		// Still reject allocator diagnostics even when Git recovered and exited zero.
		processCtx = context.WithoutCancel(r.ctx)
	}
	processErr := provenanceProcessError(processCtx, err, r.diagnostic.String())
	var limitError *provenanceLimitError
	if processCtx.Err() != nil || errors.As(processErr, &limitError) {
		return processErr
	}
	if r.diagnostic.overflow {
		return errDiffOutputLimit
	}
	return processErr
}

func (r *provenanceReader) run(ctx context.Context, limit int, args ...string) (string, error) {
	cmd := r.command(ctx, args...)
	out, diagnostic := &limitedDiffBuffer{limit: limit}, &limitedDiffBuffer{limit: 8192}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err := cmd.Run()
	processErr := provenanceProcessError(ctx, err, diagnostic.String())
	var limitError *provenanceLimitError
	if ctx.Err() != nil || errors.As(processErr, &limitError) {
		return "", processErr
	}
	// A bounded writer can itself make cmd.Run fail. Preserve that resource
	// after checking for an allocator failure or kill under the process cap.
	if out.overflow {
		return "", &provenanceLimitError{"graph_edges"}
	}
	if diagnostic.overflow {
		return "", errDiffOutputLimit
	}
	if processErr != nil {
		return "", processErr
	}
	return out.String(), nil
}

func provenanceProcessError(ctx context.Context, err error, diagnostic string) error {
	// CommandContext also kills Git. Preserve external cancellation and the
	// caller's deadline outcome instead of attributing those kills to memory.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A kill while supervised is terminal even without allocator diagnostics.
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signal() == syscall.SIGKILL {
			return &provenanceLimitError{"expansion"}
		}
	}
	// Git may recover from an allocation failure using a duplicate object and
	// exit zero. It still exhausted the cap: reject its output in that case too.
	// Diagnostics never escape this adapter.
	diagnostic = strings.ToLower(diagnostic)
	for _, marker := range []string{"out of memory", "cannot allocate memory", "calloc failed", "malloc failed", "realloc failed"} {
		if strings.Contains(diagnostic, marker) {
			return &provenanceLimitError{"expansion"}
		}
	}
	if err == nil {
		return nil
	}
	if !exitCode(err, 1) {
		return errors.Join(errProvenanceShape, err)
	}
	return err
}

func (r *provenanceReader) send(request string) error {
	if _, err := fmt.Fprintln(r.input, request); err != nil {
		// The batch process may have died under the cap before this write.
		if processErr := r.close(); processErr != nil {
			return processErr
		}
		return err
	}
	return nil
}

func (r *provenanceReader) header() ([]string, error) {
	line, err := r.output.ReadSlice('\n')
	if err != nil {
		// An exited batch process may have hit the supervisor while decoding
		// metadata or delta instructions. Preserve that typed limit outcome.
		if errors.Is(err, bufio.ErrBufferFull) {
			_ = r.batch.Process.Kill()
			return nil, errProvenanceShape
		}
		if processErr := r.close(); processErr != nil {
			return nil, processErr
		}
		return nil, errProvenanceShape
	}
	return strings.Fields(string(line)), nil
}

// info uses Git's metadata-only object_info path: no contentp is requested.
// Packed delta result sizes come from the delta header, not reconstruction.
func (r *provenanceReader) info(id string) (size int, base string, err error) {
	if err = r.send("info " + id); err != nil {
		return 0, "", err
	}
	fields, err := r.header()
	if err != nil {
		return 0, "", err
	}
	if len(fields) != 4 || fields[0] != id || fields[1] != "commit" || !forge.ValidDiffObjectID(fields[3]) {
		return 0, "", errProvenanceShape
	}
	size, err = strconv.Atoi(fields[2])
	if err != nil || size <= 0 {
		return 0, "", errProvenanceShape
	}
	return size, fields[3], nil
}

// inspectDeltaMetadata is advisory early rejection only. Each base ID lookup
// may select a standalone duplicate in a different pack (including via MIDX),
// under-counting both bytes and depth of Git's actual reconstruction chain.
// Passing this check never establishes an expansion bound; command's prlimit
// cap is mandatory for every subsequent read and traversal.
func (r *provenanceReader) inspectDeltaMetadata(id string) error {
	if r.inspected[id] {
		return nil
	}
	original := id
	seen := map[string]bool{}
	for depth := 0; ; depth++ {
		if depth >= r.limits.DeltaDepth {
			return &provenanceLimitError{"expansion"}
		}
		if seen[id] {
			return errProvenanceShape
		}
		seen[id] = true
		size, base, err := r.info(id)
		if err != nil {
			return err
		}
		if id == original && size > r.limits.CommitBytes {
			return &provenanceLimitError{"commit_bytes"}
		}
		// Charge the visible chain for early rejection. Shared delta bases may be
		// expanded again with Git's cache minimized, so charge each requested object.
		if size > r.remaining {
			if id != original || strings.Trim(base, "0") != "" {
				return &provenanceLimitError{"expansion"}
			}
			return &provenanceLimitError{"graph_bytes"}
		}
		r.remaining -= size
		r.sizes[id] = size
		if strings.Trim(base, "0") == "" {
			r.inspected[original] = true
			return nil
		}

		id = base
	}
}

func (r *provenanceReader) body(id string) (string, error) {
	if err := r.inspectDeltaMetadata(id); err != nil {
		return "", err
	}
	if err := r.send("contents " + id); err != nil {
		return "", err
	}
	fields, err := r.header()
	if err != nil {
		return "", err
	}
	if len(fields) != 4 || fields[0] != id || fields[1] != "commit" || fields[2] != strconv.Itoa(r.sizes[id]) {
		return "", errProvenanceShape
	}
	raw := make([]byte, r.sizes[id]+1)
	if _, err := io.ReadFull(r.output, raw); err != nil {
		if processErr := r.close(); processErr != nil {
			return "", processErr
		}
		return "", errProvenanceShape
	}
	if raw[len(raw)-1] != '\n' {
		return "", errProvenanceShape
	}
	return string(raw[:len(raw)-1]), nil
}

type provenanceGraph map[string][]string

func (r *provenanceReader) frontier(ctx context.Context, head, base string, exclusions int) (provenanceGraph, error) {
	limit := min(r.limits.GraphCommits, r.limits.SourceCommits+exclusions)
	out, err := r.run(ctx, r.limits.GraphEdgeBytes, "rev-list", "--parents", "--topo-order", "--max-count="+strconv.Itoa(limit+1), head, "^"+base)
	if err != nil {
		return nil, err
	}
	graph, err := parseProvenanceParents(out)
	if err != nil {
		return nil, err
	}
	if len(graph) > limit {
		resource := "source_commits"
		if r.limits.GraphCommits < r.limits.SourceCommits+exclusions {
			resource = "graph"
		}
		return nil, &provenanceLimitError{resource}
	}
	return graph, nil
}

func parseProvenanceParents(out string) (provenanceGraph, error) {
	graph := provenanceGraph{}
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return nil, errProvenanceShape
		}
		for _, id := range fields {
			if !forge.ValidDiffObjectID(id) {
				return nil, errProvenanceShape
			}
		}
		if _, exists := graph[fields[0]]; exists {
			return nil, errProvenanceShape
		}
		graph[fields[0]] = fields[1:]
	}
	return graph, nil
}

func (r *provenanceReader) parents(ctx context.Context, id string) ([]string, error) {
	if err := r.inspectDeltaMetadata(id); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, r.limits.GraphEdgeBytes, "rev-list", "--parents", "--max-count=1", id)
	if err != nil {
		return nil, err
	}
	graph, err := parseProvenanceParents(out)
	if err != nil {
		return nil, err
	}
	parents, ok := graph[id]
	if !ok || len(graph) != 1 {
		return nil, errProvenanceShape
	}
	return parents, nil
}

func (r *provenanceReader) ancestor(ctx context.Context, ancestor, head string) (bool, error) {
	_, err := r.run(ctx, 128, "merge-base", "--is-ancestor", ancestor, head)
	if exitCode(err, 1) {
		return false, nil
	}
	return err == nil, err
}

func (r *provenanceReader) mergeBases(ctx context.Context, head, base string) ([]string, error) {
	out, err := r.run(ctx, r.limits.GraphEdgeBytes, "merge-base", "--all", head, base)
	if exitCode(err, 1) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	for _, id := range ids {
		if !forge.ValidDiffObjectID(id) {
			return nil, errProvenanceShape
		}
	}
	return ids, nil
}
