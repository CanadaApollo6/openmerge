package gitops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

var errProvenanceShape = errors.New("unexpected commit shape")

// provenanceReader shares one cat-file process for metadata and original bodies.
// Only this inert adapter requires prlimit; the live transport is unaffected.
type provenanceReader struct {
	s          *session
	limits     provenance.InventoryLimits
	prlimit    string
	batch      *exec.Cmd
	input      io.WriteCloser
	output     *bufio.Reader
	diagnostic *limitedDiffBuffer
	remaining  int
	sizes      map[string]int
	approved   map[string]bool
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
	r := &provenanceReader{s: &session{r: &Repo{git: git, cfg: Config{Now: time.Now}}, dir: gitDir, diffRead: true}, limits: limits, prlimit: supervisor, remaining: limits.GraphBytes, sizes: map[string]int{}, approved: map[string]bool{}}
	r.batch = r.command(ctx, "cat-file", "--batch-command=%(objectname) %(objecttype) %(objectsize) %(objectsize:disk) %(deltabase)")
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
	// rev-list and merge-base parse bodies internally, before their output can
	// be preflighted. Bound their address space too, including mmap/cache overhead.
	cmd := r.s.newCommand(ctx, args...)
	cmd.Args = append([]string{cmd.Args[0], "-c", "core.packedGitWindowSize=1m", "-c", "core.packedGitLimit=16m", "-c", "core.deltaBaseCacheLimit=0"}, cmd.Args[1:]...)
	cmd.Args = append([]string{r.prlimit, "--as=" + strconv.Itoa(r.limits.GraphBytes+(256<<20)), "--"}, cmd.Args...)
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
	if r.diagnostic.overflow {
		return errDiffOutputLimit
	}
	return provenanceProcessError(err, r.diagnostic.String())
}

func (r *provenanceReader) run(ctx context.Context, limit int, args ...string) (string, error) {
	cmd := r.command(ctx, args...)
	out, diagnostic := &limitedDiffBuffer{limit: limit}, &limitedDiffBuffer{limit: 8192}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.overflow {
		return "", &provenanceLimitError{"graph_edges"}
	}
	if diagnostic.overflow {
		return "", errDiffOutputLimit
	}
	if err != nil {
		return "", provenanceProcessError(err, diagnostic.String())
	}
	return out.String(), nil
}

func provenanceProcessError(err error, diagnostic string) error {
	if err == nil {
		return nil
	}
	// Git's C-locale allocator diagnostics identify the supervising address
	// space ceiling, including expansion during its internal graph walk.
	for _, marker := range []string{"out of memory", "Out of memory", "Cannot allocate memory"} {
		if strings.Contains(diagnostic, marker) {
			return &provenanceLimitError{"expansion"}
		}
	}
	if !exitCode(err, 1) {
		return errors.Join(errProvenanceShape, err)
	}
	return err
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
	if _, err = fmt.Fprintln(r.input, "info "+id); err != nil {
		return 0, "", err
	}
	fields, err := r.header()
	if err != nil {
		return 0, "", err
	}
	if len(fields) != 5 || fields[0] != id || fields[1] != "commit" || !forge.ValidDiffObjectID(fields[4]) {
		return 0, "", errProvenanceShape
	}
	size, err = strconv.Atoi(fields[2])
	disk, diskErr := strconv.ParseUint(fields[3], 10, 64)
	if err != nil || size <= 0 || diskErr != nil || disk == 0 {
		return 0, "", errProvenanceShape
	}
	return size, fields[4], nil
}

func (r *provenanceReader) preflight(id string) error {
	if r.approved[id] {
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
		// Charge each independently requested object's full reconstruction chain;
		// shared delta bases may be expanded again with Git's cache disabled.
		if size > r.remaining {
			if id != original || strings.Trim(base, "0") != "" {
				return &provenanceLimitError{"expansion"}
			}
			return &provenanceLimitError{"graph_bytes"}
		}
		r.remaining -= size
		r.sizes[id] = size
		if strings.Trim(base, "0") == "" {
			r.approved[original] = true
			return nil
		}

		id = base
	}
}

func (r *provenanceReader) body(id string) (string, error) {
	if err := r.preflight(id); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(r.input, "contents "+id); err != nil {
		return "", err
	}
	fields, err := r.header()
	if err != nil {
		return "", err
	}
	if len(fields) != 5 || fields[0] != id || fields[1] != "commit" || fields[2] != strconv.Itoa(r.sizes[id]) {
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
	if err := r.preflight(id); err != nil {
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
