package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
)

// A spool is private to publication staging. Its runs contain at most MaxBytes
// of payload (plus one bounded record). Merge fan-in is fixed, including the
// last pass; neither run names nor the closure visited set accumulate in RAM.
type spoolRecord struct {
	Source     Source          `json:"source"`
	MaxID      int64           `json:"max_id"`
	Payload    json.RawMessage `json:"payload"`
	Role       string          `json:"role"`
	References []Reference     `json:"references"`
}

func (r spoolRecord) event() sourceEvent { return sourceEvent{r.Source, r.Payload} }
func recordLess(a, b spoolRecord) bool   { return sourceLess(a.Source, b.Source) }
func sameSource(a, b Source) bool {
	return a.QueueID == b.QueueID && a.ID == b.ID && a.Queue == b.Queue
}
func mergeRecord(a, b spoolRecord) (spoolRecord, error) {
	if a.Source != b.Source || !bytes.Equal(a.Payload, b.Payload) {
		return a, errIntegrity
	}
	if a.MaxID == 0 {
		a.MaxID = b.MaxID
	}
	if b.MaxID != 0 && a.MaxID != b.MaxID {
		return a, errIntegrity
	}
	if a.Role == "scope" || b.Role == "scope" {
		a.Role = "scope"
	}
	// A pending candidate has not yet been resolved. The resolved copy wins.
	if a.References == nil {
		a.References = b.References
	}
	if b.References != nil && a.References != nil {
		x, _ := encode(a.References)
		y, _ := encode(b.References)
		if !bytes.Equal(x, y) {
			return a, errIntegrity
		}
	}
	return a, nil
}

type spoolCursor struct {
	f     *os.File
	s     *bufio.Scanner
	value spoolRecord
	ok    bool
}

func openSpool(path string) (*spoolCursor, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errOutput
	}
	c := &spoolCursor{f: f, s: bufio.NewScanner(f)}
	c.s.Buffer(make([]byte, 64<<10), 3*MaxBytes)
	if e = c.next(); e != nil {
		_ = f.Close()
		return nil, e
	}
	return c, nil
}
func (c *spoolCursor) next() error {
	c.ok = c.s.Scan()
	if !c.ok {
		if c.s.Err() != nil {
			return errOutput
		}
		return nil
	}
	c.value = spoolRecord{}
	if json.Unmarshal(c.s.Bytes(), &c.value) != nil {
		return errIntegrity
	}
	return nil
}
func spoolWrite(w io.Writer, r spoolRecord) error {
	b, e := json.Marshal(r)
	if e != nil {
		return errCanonical
	}
	b = append(b, '\n')
	if _, e = w.Write(b); e != nil {
		return errOutput
	}
	return nil
}
func spoolFile(path string, fn func(io.Writer) error) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return errOutput
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriterSize(f, 64<<10)
	if e = fn(w); e != nil {
		return e
	}
	if w.Flush() != nil {
		return errOutput
	}
	return nil
}
func runPath(dir string, pass, n int) string {
	return filepath.Join(dir, fmt.Sprintf("run-%d-%d", pass, n))
}
func mergeRuns(paths []string, out string) error {
	cursors := make([]*spoolCursor, 0, len(paths))
	defer func() {
		for _, c := range cursors {
			_ = c.f.Close()
		}
	}()
	for _, p := range paths {
		c, e := openSpool(p)
		if e != nil {
			return e
		}
		cursors = append(cursors, c)
	}
	return spoolFile(out, func(w io.Writer) error {
		var pending *spoolRecord
		for {
			smallest := -1
			for i, c := range cursors {
				if c.ok && (smallest < 0 || recordLess(c.value, cursors[smallest].value)) {
					smallest = i
				}
			}
			if smallest < 0 {
				break
			}
			c := cursors[smallest]
			r := c.value
			if pending != nil && sameSource(pending.Source, r.Source) {
				merged, e := mergeRecord(*pending, r)
				if e != nil {
					return e
				}
				pending = &merged
			} else {
				if pending != nil {
					if e := spoolWrite(w, *pending); e != nil {
						return e
					}
				}
				pending = &r
			}
			if e := c.next(); e != nil {
				return e
			}
		}
		if pending != nil {
			return spoolWrite(w, *pending)
		}
		return nil
	})
}
func sortSpool(dir, input, output string) error {
	c, e := openSpool(input)
	if e != nil {
		return e
	}
	defer func() { _ = c.f.Close() }()
	n := 0
	for c.ok {
		chunk := make([]spoolRecord, 0, pageSize)
		size := 0
		for c.ok && (len(chunk) == 0 || size < MaxBytes && len(chunk) < pageSize) {
			r := c.value
			chunk = append(chunk, r)
			raw, _ := json.Marshal(r)
			size += len(raw)
			if e = c.next(); e != nil {
				return e
			}
		}
		sort.Slice(chunk, func(i, j int) bool { return recordLess(chunk[i], chunk[j]) })
		e = spoolFile(runPath(dir, 0, n), func(w io.Writer) error {
			for i := 0; i < len(chunk); i++ {
				r := chunk[i]
				for i+1 < len(chunk) && sameSource(r.Source, chunk[i+1].Source) {
					i++
					var err error
					r, err = mergeRecord(r, chunk[i])
					if err != nil {
						return err
					}
				}
				if e := spoolWrite(w, r); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		n++
	}
	if n == 0 {
		return spoolFile(output, func(io.Writer) error { return nil })
	}
	pass := 0
	for n > 1 {
		next := 0
		for start := 0; start < n; start += 16 {
			paths := make([]string, 0, 16)
			for i := start; i < n && i < start+16; i++ {
				paths = append(paths, runPath(dir, pass, i))
			}
			if e = mergeRuns(paths, runPath(dir, pass+1, next)); e != nil {
				return e
			}
			for _, p := range paths {
				if os.Remove(p) != nil {
					return errOutput
				}
			}
			next++
		}
		n = next
		pass++
	}
	if os.Rename(runPath(dir, pass, 0), output) != nil {
		return errOutput
	}
	return nil
}

// closure exhausts a typed worklist. Each wave is sorted/deduplicated and
// merged with the on-disk visited set before resolving new records. References
// may point forward in the final total order: queue UTF-8 tuple, then event ID.
func closure(ctx context.Context, tx pgx.Tx, dir, input string) (string, error) {
	return closureVersion(ctx, tx, dir, input, 1)
}

func closureVersion(ctx context.Context, tx pgx.Tx, dir, input string, version int) (string, error) {
	if version == 2 {
		indexDir, err := os.MkdirTemp(dir, "provenance-index-")
		if err != nil {
			return "", errOutput
		}
		defer func() { _ = os.RemoveAll(indexDir) }()
		ctx = context.WithValue(ctx, provenanceIndexKey{}, provenanceIndex{indexDir})
	}
	visited := filepath.Join(dir, "visited")
	if e := spoolFile(visited, func(io.Writer) error { return nil }); e != nil {
		return "", e
	}
	for wave := 0; ; wave++ {
		pending := filepath.Join(dir, "pending")
		if e := sortSpool(dir, input, pending); e != nil {
			return "", e
		}
		_ = os.Remove(input)
		old, e := openSpool(visited)
		if e != nil {
			return "", e
		}
		fresh, e := openSpool(pending)
		if e != nil {
			_ = old.f.Close()
			return "", e
		}
		next := filepath.Join(dir, "next")
		merged := filepath.Join(dir, "merged")
		added := false
		hasWork := false
		e = spoolFile(next, func(work io.Writer) error {
			return spoolFile(merged, func(out io.Writer) error {
				for old.ok || fresh.ok {
					if old.ok && (!fresh.ok || recordLess(old.value, fresh.value)) {
						if e := spoolWrite(out, old.value); e != nil {
							return e
						}
						if e := old.next(); e != nil {
							return e
						}
						continue
					}
					r := fresh.value
					if old.ok && sameSource(old.value.Source, r.Source) {
						var e error
						r, e = mergeRecord(old.value, r)
						if e != nil {
							return e
						}
						if e = old.next(); e != nil {
							return e
						}
					} else {
						added = true
						refs, deps, e := resolveEvidenceVersion(ctx, tx, r.event(), r.MaxID, version)
						if e != nil {
							return e
						}
						if !validResolutionVersion(r.Source, refs, deps, version) {
							return errIntegrity
						}
						r.References = refs
						for _, d := range deps {
							hasWork = true
							if e = spoolWrite(work, spoolRecord{Source: d.source, Payload: d.payload, Role: "dependency", MaxID: r.MaxID}); e != nil {
								return e
							}
						}
					}
					if e := spoolWrite(out, r); e != nil {
						return e
					}
					if e := fresh.next(); e != nil {
						return e
					}
				}
				return nil
			})
		})
		_ = old.f.Close()
		_ = fresh.f.Close()
		if e != nil {
			return "", e
		}
		_ = os.Remove(visited)
		_ = os.Remove(pending)
		if os.Rename(merged, visited) != nil {
			return "", errOutput
		}
		if !added || !hasWork {
			_ = os.Remove(next)
			if version == 2 {
				if err := validateProvenanceClosure(visited, dir); err != nil {
					return "", err
				}
			}
			return visited, nil
		}
		input = next
	}
}
func emitSpoolVersion(path string, emit func(sourceEvent, *Selection) error, version int) error {
	c, e := openSpool(path)
	if e != nil {
		return e
	}
	defer func() { _ = c.f.Close() }()
	for c.ok {
		r := c.value
		s, e := selectionForVersion(r.event(), r.Role, r.References, version)
		if e != nil {
			return e
		}
		if e = emit(r.event(), &s); e != nil {
			return e
		}
		if e = c.next(); e != nil {
			return e
		}
	}
	return nil
}
func selectionFor(e sourceEvent, role string, refs []Reference) (Selection, error) {
	return selectionForVersion(e, role, refs, 1)
}

func selectionForVersion(e sourceEvent, role string, refs []Reference, version int) (Selection, error) {
	payload, err := Canonical(e.payload)
	if err != nil {
		return Selection{}, err
	}
	s := Selection{Source: e.source, PayloadSHA256: digest(payload), Role: role, References: refs, Kinds: evidenceKindsVersion(e.source.Type, version)}
	m, err := metadata(s)
	if err != nil {
		return s, err
	}
	s.MetadataSHA256 = digest(m)
	return s, nil
}

// Replay must contain exactly the transitive closure of its listed scope roots.
// Export's own closure is recomputed from those roots in replay's snapshot, so
// a contradicted reference or absence, a missing retained source, and an
// unrelated dependency component (including a self-contained cycle) all fail.
func verifyExactClosureVersion(ctx context.Context, tx pgx.Tx, dir, roots, selected string, version int) error {
	expectedPath, e := closureVersion(ctx, tx, dir, roots, version)
	if e != nil {
		return e
	}
	expected, e := openSpool(expectedPath)
	if e != nil {
		return e
	}
	defer func() { _ = expected.f.Close() }()
	actual, e := openSpool(selected)
	if e != nil {
		return e
	}
	defer func() { _ = actual.f.Close() }()
	for expected.ok || actual.ok {
		if !expected.ok || !actual.ok {
			return errIntegrity
		}
		want, e := selectionForVersion(expected.value.event(), expected.value.Role, expected.value.References, version)
		if e != nil {
			return e
		}
		got, e := selectionForVersion(actual.value.event(), actual.value.Role, actual.value.References, version)
		if e != nil {
			return e
		}
		wantRaw, e := encode(want)
		if e != nil {
			return e
		}
		gotRaw, e := encode(got)
		if e != nil || !bytes.Equal(wantRaw, gotRaw) {
			return errIntegrity
		}
		if e = expected.next(); e != nil {
			return e
		}
		if e = actual.next(); e != nil {
			return e
		}
	}
	return nil
}

// The resolver's references and emitted sources must agree before a work item
// is marked resolved. The lists are bounded by one record's evidence budget.
func validResolution(owner Source, refs []Reference, deps []sourceEvent) bool {
	return validResolutionVersion(owner, refs, deps, 1)
}

func validResolutionVersion(owner Source, refs []Reference, deps []sourceEvent, version int) bool {
	if !validReferencesVersion(refs, version) {
		return false
	}
	for _, ref := range refs {
		if ref.Owner.QueueID != owner.QueueID || ref.Owner.EventID != owner.ID {
			return false
		}
		if ref.Source == nil {
			continue
		}
		found := false
		for _, dep := range deps {
			if dep.source == *ref.Source {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, dep := range deps {
		found := false
		for _, ref := range refs {
			if ref.Source != nil && *ref.Source == dep.source {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
