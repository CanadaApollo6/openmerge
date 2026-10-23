package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
)

var errUnsupported = errors.New("audit: atomic publication unsupported")

var errOutput = errors.New("audit: output unavailable or already exists")

// Manifest contains only deterministic selection and integrity data. The
// metadata streams are separate files so manifest memory is independent of size.
type Manifest struct {
	ManifestSHA256   string  `json:"manifest_sha256"`
	Version          int     `json:"version"`
	Request          Request `json:"request"`
	Selection        string  `json:"selection"`
	SelectionSHA256  string  `json:"selection_sha256"`
	Watermarks       string  `json:"watermarks"`
	WatermarksSHA256 string  `json:"watermarks_sha256"`
	Events           string  `json:"events"`
	EventsSHA256     string  `json:"events_sha256"`
	Count            int64   `json:"count"`
	TerminalSHA256   string  `json:"terminal_sha256"`
}

type stream struct {
	f *os.File
	b *bufio.Writer
	h hash.Hash
}

func newStream(dir, name string) (*stream, error) {
	f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, errOutput
	}
	h := sha256.New()
	return &stream{f: f, b: bufio.NewWriterSize(io.MultiWriter(f, h), 64<<10), h: h}, nil
}
func (s *stream) write(raw []byte) error {
	if _, e := s.b.Write(raw); e != nil {
		return errOutput
	}
	if e := s.b.WriteByte('\n'); e != nil {
		return errOutput
	}
	return nil
}
func (s *stream) finish() (string, error) {
	if s.b.Flush() != nil || s.f.Sync() != nil || s.f.Close() != nil {
		return "", errOutput
	}
	return hex.EncodeToString(s.h.Sum(nil)), nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func encode(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, errCanonical
	}
	return Canonical(b)
}
func metadata(s Selection) ([]byte, error) {
	return encode(struct {
		Source     Source      `json:"source"`
		Role       string      `json:"role"`
		References []Reference `json:"references"`
	}{s.Source, s.Role, s.References})
}

// Export publishes a fresh source selection from one repeatable-read snapshot.
// It retains every selected kind and does not interpret event/wire semantics.
func (r *Reader) Export(ctx context.Context, req Request, target string) (Manifest, error) {
	var result Manifest
	err := publish(target, func(dir string) error {
		return r.snapshot(ctx, func(ctx context.Context, tx pgx.Tx) error {
			resolved, e := resolve(ctx, tx, req)
			if e != nil {
				return e
			}
			return build(dir, resolved, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
				return queues(ctx, tx, resolved, func(w watermark) error {
					if e := water(w); e != nil {
						return e
					}
					return scan(ctx, tx, w, resolved, func(e sourceEvent) error { return emit(e, nil) })
				})
			}, nil, &result)
		})
	})
	return result, err
}

type producer func(func(sourceEvent, *Selection) error, func(watermark) error) error

func build(dir string, req Request, produce producer, want *Manifest, result *Manifest) error {
	events, e := newStream(dir, "events.jsonl")
	if e != nil {
		return e
	}
	defer func() { _ = events.f.Close() }()
	selected, e := newStream(dir, "selection.jsonl")
	if e != nil {
		return e
	}
	defer func() { _ = selected.f.Close() }()
	waters, e := newStream(dir, "watermarks.jsonl")
	if e != nil {
		return e
	}
	defer func() { _ = waters.f.Close() }()
	m := Manifest{Version: 1, Request: req, Selection: "selection.jsonl", Watermarks: "watermarks.jsonl", Events: "events.jsonl", TerminalSHA256: zeroDigest}
	emit := func(event sourceEvent, want *Selection) error {
		payload, e := Canonical(event.payload)
		if e != nil {
			return e
		}
		s := Selection{Source: event.source, PayloadSHA256: digest(payload), Role: "scope", References: []Reference{}}
		if want != nil {
			s.Role = want.Role
			s.References = want.References
		}
		meta, e := metadata(s)
		if e != nil {
			return e
		}
		s.MetadataSHA256 = digest(meta)
		if want != nil {
			got, e := encode(s)
			if e != nil {
				return e
			}
			expected, e := encode(want)
			if e != nil || !bytes.Equal(got, expected) {
				return errIntegrity
			}
		}
		raw, e := encode(envelope{Kind: "source", Source: event.source, Payload: payload, Role: s.Role, References: s.References, PreviousSHA256: m.TerminalSHA256})
		if e != nil {
			return e
		}
		if e = events.write(raw); e != nil {
			return e
		}
		m.TerminalSHA256 = digest(raw)
		m.Count++
		raw, e = encode(s)
		if e != nil {
			return e
		}
		return selected.write(raw)
	}
	if e = produce(emit, func(w watermark) error {
		raw, e := encode(w)
		if e != nil {
			return e
		}
		return waters.write(raw)
	}); e != nil {
		return e
	}
	m.EventsSHA256, e = events.finish()
	if e != nil {
		return e
	}
	m.SelectionSHA256, e = selected.finish()
	if e != nil {
		return e
	}
	m.WatermarksSHA256, e = waters.finish()
	if e != nil {
		return e
	}
	rawMeta, e := encode(m)
	if e != nil {
		return e
	}
	m.ManifestSHA256 = digest(rawMeta)
	if want != nil && m != *want {
		return errIntegrity
	}
	raw, e := encode(m)
	if e != nil {
		return e
	}
	manifest, e := newStream(dir, "manifest.json")
	if e != nil {
		return e
	}
	defer func() { _ = manifest.f.Close() }()
	if e = manifest.write(raw); e != nil {
		return e
	}
	if _, e = manifest.finish(); e != nil {
		return e
	}
	*result = m
	return nil
}

// Replay is the internal equivalent of an exact --manifest operation. It does
// not rerun the range query: only listed sources are read, including old IDs
// committed after the original snapshot. Such unlisted IDs remain excluded.
func (r *Reader) Replay(ctx context.Context, manifestPath, target string) (Manifest, error) {
	var result Manifest
	root, e := os.OpenRoot(filepath.Dir(manifestPath))
	if e != nil {
		return result, errIntegrity
	}
	defer func() { _ = root.Close() }()
	f, e := openRegular(root, filepath.Base(manifestPath))
	if e != nil {
		return result, e
	}
	defer func() { _ = f.Close() }()
	raw, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if e != nil || len(raw) > MaxBytes {
		return result, errIntegrity
	}
	var m Manifest
	if strict(raw, &m) != nil || m.Version != 1 || m.Count < 0 || m.Selection != "selection.jsonl" || m.Events != "events.jsonl" || m.Watermarks != "watermarks.jsonl" || !validDigest(m.TerminalSHA256) {
		return result, errIntegrity
	}
	canonicalManifest, ce := encode(m)
	if ce != nil || !bytes.Equal(raw, append(canonicalManifest, '\n')) {
		return result, errIntegrity
	}
	expectedManifest := m.ManifestSHA256
	m.ManifestSHA256 = ""
	checkManifest, ce := encode(m)
	m.ManifestSHA256 = expectedManifest
	if ce != nil || digest(checkManifest) != expectedManifest {
		return result, errIntegrity
	}
	if _, _, e = parseRange(m.Request); e != nil || m.Request.Installation == "" {
		return result, errIntegrity
	}
	// Pin open file descriptors for all input streams throughout verification/replay.
	files := make([]*os.File, 0, 3)
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	for _, item := range []struct{ name, sha string }{{m.Selection, m.SelectionSHA256}, {m.Watermarks, m.WatermarksSHA256}, {m.Events, m.EventsSHA256}} {
		f, e := openRegular(root, item.name)
		if e != nil {
			return result, e
		}
		files = append(files, f)
		if !validDigest(item.sha) {
			return result, errIntegrity
		}
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != item.sha {
			return result, errIntegrity
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			return result, errIntegrity
		}
	}
	e = publish(target, func(dir string) error {
		return r.snapshot(ctx, func(ctx context.Context, tx pgx.Tx) error {
			resolved, e := resolve(ctx, tx, m.Request)
			if e != nil || resolved != m.Request {
				return errIntegrity
			}
			identities := map[int64]watermark{}
			if e = queues(ctx, tx, m.Request, func(w watermark) error { identities[w.QueueID] = w; return nil }); e != nil {
				return e
			}
			waters := map[int64]watermark{}
			var previousWater *Source
			return build(dir, m.Request, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
				if e = lines(files[1], func(raw []byte) error {
					var w watermark
					if strict(raw, &w) != nil || w.ID < 0 || w.QueueID <= 0 || w.Queue.Installation != m.Request.Installation || w.Queue.Repository != m.Request.Repository {
						return errIntegrity
					}
					actual, ok := identities[w.QueueID]
					if !ok || actual.Queue != w.Queue || w.ID > actual.ID {
						return errIntegrity
					}
					if w.ID > 0 {
						var exists bool
						if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE queue_id=$1 AND id=$2)`, w.QueueID, w.ID).Scan(&exists) != nil || !exists {
							return errIntegrity
						}
					}
					if _, duplicate := waters[w.QueueID]; duplicate {
						return errIntegrity
					}
					current := Source{QueueID: w.QueueID, Queue: w.Queue}
					if previousWater != nil && !sourceLess(*previousWater, current) {
						return errIntegrity
					}
					previousWater = &current
					waters[w.QueueID] = w
					return water(w)
				}); e != nil {
					return e
				}
				// Only the frozen inventory is required: newer/unselected queues do not
				// expand the saved source selection.
				var previous *Source
				var page []Selection
				flush := func() error {
					if e := fetchPage(ctx, tx, page, emit); e != nil {
						return e
					}
					page = page[:0]
					return nil
				}
				e = lines(files[0], func(raw []byte) error {
					var s Selection
					if strict(raw, &s) != nil || s.Source.ID <= 0 || s.Source.QueueID <= 0 || !validRole(s.Role) || !validReferences(s.References) || !validDigest(s.PayloadSHA256) || !validDigest(s.MetadataSHA256) {
						return errIntegrity
					}
					w, ok := waters[s.Source.QueueID]
					if !ok || w.Queue != s.Source.Queue || s.Source.ID > w.ID {
						return errIntegrity
					}
					if s.Source.Queue.Installation != m.Request.Installation || s.Source.Queue.Repository != m.Request.Repository {
						return errIntegrity
					}
					if previous != nil && !sourceLess(*previous, s.Source) {
						return errIntegrity
					}
					p := s.Source
					previous = &p
					if len(page) > 0 && (len(page) == pageSize || page[0].Source.QueueID != s.Source.QueueID) {
						if e := flush(); e != nil {
							return e
						}
					}
					page = append(page, s)
					return nil
				})
				if e != nil {
					return e
				}
				return flush()
			}, &m, &result)
		})
	})
	return result, e
}

func sourceLess(a, b Source) bool {
	if a.Queue != b.Queue {
		return keyLess(a.Queue, b.Queue)
	}
	return a.QueueID == b.QueueID && a.ID < b.ID
}
func validRole(role string) bool            { return role == "scope" }
func validReferences(refs []Reference) bool { return refs != nil && len(refs) == 0 }
func validDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func strict(raw []byte, v any) error {
	canonical, e := Canonical(raw)
	if e != nil {
		return errIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return errIntegrity
	}
	round, e := encode(v)
	if e != nil || !bytes.Equal(round, canonical) {
		return errIntegrity
	}
	return nil
}
func lines(r io.Reader, fn func([]byte) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), MaxBytes+2)
	for s.Scan() {
		raw := s.Bytes()
		if len(raw) == 0 || fn(raw) != nil {
			return errIntegrity
		}
	}
	if s.Err() != nil {
		return errIntegrity
	}
	return nil
}
func openRegular(root *os.Root, name string) (*os.File, error) {
	info, e := root.Lstat(name)
	if e != nil || !info.Mode().IsRegular() {
		return nil, errIntegrity
	}
	expected := info
	f, e := root.OpenFile(name, os.O_RDONLY|openInputFlags, 0)
	if e != nil {
		return nil, errIntegrity
	}
	info, e = f.Stat()
	if e != nil || !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		_ = f.Close()
		return nil, errIntegrity
	}
	return f, nil
}

func publish(target string, build func(string) error) error {
	if _, e := os.Lstat(target); !os.IsNotExist(e) {
		return errOutput
	}
	dir, e := os.MkdirTemp(filepath.Dir(target), ".audit-staging-")
	if e != nil {
		return errOutput
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if e = os.Chmod(dir, 0700); e != nil {
		return errOutput
	}
	if e = build(dir); e != nil {
		return e
	}
	f, e := os.Open(dir)
	if e != nil {
		return errOutput
	}
	e = f.Sync()
	_ = f.Close()
	if e != nil {
		return errOutput
	}
	if e = renameNoReplace(dir, target); e != nil {
		return publicationError(e)
	}
	parent, e := os.Open(filepath.Dir(target))
	if e != nil {
		return errOutput
	}
	e = parent.Sync()
	closeErr := parent.Close()
	if e != nil || closeErr != nil {
		return errOutput
	}
	return nil
}

func publicationError(e error) error {
	if errors.Is(e, syscall.EINVAL) || errors.Is(e, syscall.ENOSYS) || errors.Is(e, syscall.EOPNOTSUPP) || errors.Is(e, errUnsupported) {
		return errUnsupported
	}
	return errOutput
}
