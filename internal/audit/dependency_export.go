package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
)

// The ADR 0011 scoped dependency export reuses the ADR 0008 model: a
// self-hashed canonical manifest over a hash-chained canonical JSONL stream,
// published atomically and read back with strict decoders. It is a distinct
// format. Its manifest carries fields and a version that the canonical v1/v2
// decoder rejects as unknown, so an older verifier refuses it instead of
// misreading it. Canonical operator exports are unchanged.
const (
	DependencyExportFormat  = "openmerge.dependency.scoped-export"
	DependencyExportVersion = 3
	dependencyRecordsFile   = "dependency_records.jsonl"
	maxDependencyPage       = 100
)

// Dependency export audiences. A complete audience may read every source the
// queue's journal declares; any other audience receives the redacted projection.
const (
	DependencyAudienceComplete = "complete"
	DependencyAudienceRedacted = "redacted"
)

// ErrDependencyExport reports a malformed, tampered or mis-scoped export.
var ErrDependencyExport = errors.New("audit: dependency export integrity check failed")

// DependencyManifest pins the scoped stream. Positions are ordinals within this
// export, never journal IDs, so withheld records cannot shift them.
type DependencyManifest struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	Format         string `json:"format"`
	Version        int    `json:"version"`
	Queue          Key    `json:"queue"`
	Audience       string `json:"audience"`
	Records        string `json:"records"`
	RecordsSHA256  string `json:"records_sha256"`
	Count          int64  `json:"count"`
	TerminalSHA256 string `json:"terminal_sha256"`
}

// DependencySource is retained journal metadata. Only complete-audience exports
// carry it: journal IDs and recording times reveal interleaved private records.
type DependencySource struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	RecordedAt string `json:"recorded_at"`
}

// DependencyRecord is one chained export line.
type DependencyRecord struct {
	Position       int64             `json:"position"`
	Kind           string            `json:"kind"`
	Source         *DependencySource `json:"source"`
	Evidence       json.RawMessage   `json:"evidence"`
	PreviousSHA256 string            `json:"previous_sha256"`
}

// DependencyEntry is an unchained record supplied by a scoped builder.
type DependencyEntry struct {
	Kind     string
	Source   *DependencySource
	Evidence any
}

// Redacted evidence is constructed only from these consumer-repository facts.

// DependencyPolicyEvidence is the governing consumer policy.
type DependencyPolicyEvidence struct {
	Enabled          bool   `json:"enabled"`
	SingleMaintainer bool   `json:"single_maintainer"`
	GoverningCommit  string `json:"governing_commit"`
	PolicyDigest     string `json:"policy_digest"`
	Writers          int    `json:"writers"`
}

// DependencyDeclarationEvidence is a consumer declaration with sources withheld.
type DependencyDeclarationEvidence struct {
	ChangeID      string   `json:"change_id"`
	Revision      uint64   `json:"revision"`
	Held          bool     `json:"held"`
	UnknownEditor bool     `json:"unknown_editor"`
	Editors       []string `json:"editors"`
	Redeclaration bool     `json:"redeclaration"`
}

// DependencyAcknowledgementEvidence is an independent writer acknowledgement.
type DependencyAcknowledgementEvidence struct {
	ChangeID        string   `json:"change_id"`
	Writer          string   `json:"writer"`
	Exclusions      []string `json:"exclusions"`
	GoverningCommit string   `json:"governing_commit"`
	PolicyDigest    string   `json:"policy_digest"`
}

// DependencySelfAcknowledgementEvidence is the distinct, audited override of
// identity exclusions under the governing single-maintainer opt-in.
type DependencySelfAcknowledgementEvidence struct {
	ChangeID         string   `json:"change_id"`
	Writer           string   `json:"writer"`
	Overridden       []string `json:"overridden"`
	SingleMaintainer bool     `json:"single_maintainer"`
	GoverningCommit  string   `json:"governing_commit"`
	PolicyDigest     string   `json:"policy_digest"`
}

// redactedEvidence decodes the only kinds a redacted export may contain.
func redactedEvidence(kind string, raw []byte) error {
	var v any
	switch kind {
	case "policy":
		v = &DependencyPolicyEvidence{}
	case "declaration":
		v = &DependencyDeclarationEvidence{}
	case "acknowledgement":
		v = &DependencyAcknowledgementEvidence{}
	case "self_acknowledgement":
		v = &DependencySelfAcknowledgementEvidence{}
	default:
		return ErrDependencyExport
	}
	return strict(raw, v)
}

// EncodeDependencyExport chains entries in order and returns the manifest and
// stream bytes. Equal inputs always produce equal bytes. Readers must verify
// with DecodeDependencyExport; WriteDependencyExport does so before publishing.
func EncodeDependencyExport(queue Key, audience string, entries []DependencyEntry) (DependencyManifest, []byte, []byte, error) {
	m := DependencyManifest{Format: DependencyExportFormat, Version: DependencyExportVersion, Queue: queue, Audience: audience, Records: dependencyRecordsFile, TerminalSHA256: zeroDigest}
	var stream bytes.Buffer
	for i, entry := range entries {
		evidence, err := encode(entry.Evidence)
		if err != nil {
			return m, nil, nil, err
		}
		raw, err := encode(DependencyRecord{Position: int64(i + 1), Kind: entry.Kind, Source: entry.Source, Evidence: evidence, PreviousSHA256: m.TerminalSHA256})
		if err != nil {
			return m, nil, nil, err
		}
		stream.Write(append(raw, '\n'))
		m.TerminalSHA256 = digest(raw)
		m.Count++
	}
	m.RecordsSHA256 = digest(stream.Bytes())
	raw, err := encode(m)
	if err != nil {
		return m, nil, nil, err
	}
	m.ManifestSHA256 = digest(raw)
	manifest, err := encode(m)
	if err != nil {
		return m, nil, nil, err
	}
	return m, append(manifest, '\n'), stream.Bytes(), nil
}

// WriteDependencyExport verifies, then atomically publishes, a new export
// directory. It never publishes an export its own verifier would refuse.
func WriteDependencyExport(target string, queue Key, audience string, entries []DependencyEntry) (DependencyManifest, error) {
	m, manifest, records, err := EncodeDependencyExport(queue, audience, entries)
	if err != nil {
		return m, err
	}
	if _, err := DecodeDependencyExport(manifest, records); err != nil {
		return m, err
	}
	return m, publish(target, func(dir string) error {
		for _, file := range []struct {
			name string
			raw  []byte
		}{{dependencyRecordsFile, records}, {"manifest.json", manifest}} {
			s, err := newStream(dir, file.name)
			if err != nil {
				return err
			}
			if _, err = s.b.Write(file.raw); err != nil {
				_ = s.f.Close()
				return errOutput
			}
			if _, err = s.finish(); err != nil {
				return err
			}
		}
		return nil
	})
}

// VerifyDependencyExport reads a published export through regular-file checks.
func VerifyDependencyExport(dir string) (DependencyManifest, []DependencyRecord, error) {
	var m DependencyManifest
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, nil, ErrDependencyExport
	}
	defer func() { _ = root.Close() }()
	read := func(name string) ([]byte, error) {
		f, err := openRegular(root, name)
		if err != nil {
			return nil, ErrDependencyExport
		}
		defer func() { _ = f.Close() }()
		raw, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		if err != nil || len(raw) > MaxBytes {
			return nil, ErrDependencyExport
		}
		return raw, nil
	}
	manifest, err := read("manifest.json")
	if err != nil {
		return m, nil, err
	}
	stream, err := read(dependencyRecordsFile)
	if err != nil {
		return m, nil, err
	}
	records, err := DecodeDependencyExport(manifest, stream)
	if err != nil {
		return m, nil, err
	}
	_ = strict(bytes.TrimSuffix(manifest, []byte("\n")), &m)
	return m, records, nil
}

// DecodeDependencyExport strictly verifies manifest and stream bytes: canonical
// encoding, unknown and missing fields, digests, chain, positions and per-kind
// evidence. Complete-audience evidence must be a strict journal record of the
// stated type; redacted evidence must be an allowlisted projection.
func DecodeDependencyExport(manifest, stream []byte) ([]DependencyRecord, error) {
	var m DependencyManifest
	raw, ok := bytes.CutSuffix(manifest, []byte("\n"))
	if !ok || !canonicalLine(raw) || strict(raw, &m) != nil || m.Format != DependencyExportFormat || m.Version != DependencyExportVersion || m.Records != dependencyRecordsFile || m.Audience != DependencyAudienceComplete && m.Audience != DependencyAudienceRedacted || !validDigest(m.RecordsSHA256) || !validDigest(m.TerminalSHA256) || !validDigest(m.ManifestSHA256) {
		return nil, ErrDependencyExport
	}
	self := m
	self.ManifestSHA256 = ""
	check, err := encode(self)
	if err != nil || digest(check) != m.ManifestSHA256 || digest(stream) != m.RecordsSHA256 {
		return nil, ErrDependencyExport
	}
	var out []DependencyRecord
	previous, last := zeroDigest, int64(0)
	for _, line := range strings.SplitAfter(string(stream), "\n") {
		if line == "" {
			continue
		}
		raw, ok := strings.CutSuffix(line, "\n")
		var r DependencyRecord
		if !ok || !canonicalLine([]byte(raw)) || strict([]byte(raw), &r) != nil || r.Position != int64(len(out)+1) || r.PreviousSHA256 != previous {
			return nil, ErrDependencyExport
		}
		if verifyDependencyEvidence(m.Audience, r, &last) != nil {
			return nil, ErrDependencyExport
		}
		previous = digest([]byte(raw))
		out = append(out, r)
	}
	if int64(len(out)) != m.Count || previous != m.TerminalSHA256 {
		return nil, ErrDependencyExport
	}
	return out, nil
}

// canonicalLine requires the exact canonical bytes that the writer chains.
func canonicalLine(raw []byte) bool {
	canonical, err := Canonical(raw)
	return err == nil && bytes.Equal(canonical, raw)
}

func verifyDependencyEvidence(audience string, r DependencyRecord, last *int64) error {
	if audience == DependencyAudienceRedacted {
		if r.Source != nil {
			return ErrDependencyExport
		}
		return redactedEvidence(r.Kind, r.Evidence)
	}
	typ, kind, ok := journalreader.ExportKind(r.Evidence)
	if !ok || kind != r.Kind || r.Source == nil || r.Source.Type != typ || r.Source.ID <= *last || !validStamp(r.Source.RecordedAt) {
		return ErrDependencyExport
	}
	*last = r.Source.ID
	decoded, err := journalreader.DecodeEvent(r.Evidence)
	if err != nil || decoded.Queue == nil && decoded.Dependency == nil {
		return ErrDependencyExport
	}
	return nil
}

func validStamp(s string) bool {
	at, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && stamp(at) == s
}

// DependencyPage is one cursor page over verified records. After and Next are
// export positions; Total counts this export's records only.
type DependencyPage struct {
	Records []DependencyRecord `json:"records"`
	Next    int64              `json:"next"`
	Total   int64              `json:"total"`
}

// PageDependencyRecords returns at most limit records after the cursor.
func PageDependencyRecords(records []DependencyRecord, after int64, limit int) (DependencyPage, error) {
	total := int64(len(records))
	if limit < 1 || limit > maxDependencyPage || after < 0 || after > total {
		return DependencyPage{}, ErrSelectors
	}
	end := min(total, after+int64(limit))
	page := DependencyPage{Records: append([]DependencyRecord{}, records[after:end]...), Total: total}
	if end < total {
		page.Next = end
	}
	return page, nil
}
