package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var dependencyQueue = Key{Installation: "7", Repository: "acme/client", Base: "main"}

func redactedEntries() []DependencyEntry {
	return []DependencyEntry{
		{Kind: "policy", Evidence: DependencyPolicyEvidence{Enabled: true, SingleMaintainer: true, GoverningCommit: strings.Repeat("c", 40), PolicyDigest: strings.Repeat("d", 64), Writers: 2}},
		{Kind: "declaration", Evidence: DependencyDeclarationEvidence{ChangeID: "chg1_7", Revision: 1, Held: true, Editors: []string{"11"}}},
		{Kind: "self_acknowledgement", Evidence: DependencySelfAcknowledgementEvidence{ChangeID: "chg1_7", Writer: "11", Overridden: []string{"11"}, SingleMaintainer: true, GoverningCommit: strings.Repeat("c", 40), PolicyDigest: strings.Repeat("d", 64)}},
	}
}

// rechain re-signs edited record lines so a test reaches the check under test
// rather than a digest mismatch.
func rechain(t *testing.T, manifest []byte, lines []string, audience string) ([]byte, []byte) {
	t.Helper()
	var m DependencyManifest
	if err := strict(bytes.TrimSuffix(manifest, []byte("\n")), &m); err != nil {
		t.Fatal(err)
	}
	stream := []byte(strings.Join(lines, "\n") + "\n")
	m.Audience, m.Count, m.RecordsSHA256, m.ManifestSHA256 = audience, int64(len(lines)), digest(stream), ""
	m.TerminalSHA256 = digest([]byte(lines[len(lines)-1]))
	raw, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	m.ManifestSHA256 = digest(raw)
	raw, err = encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n'), stream
}

func TestDependencyExportStrictRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	m, err := WriteDependencyExport(dir, dependencyQueue, DependencyAudienceRedacted, redactedEntries())
	if err != nil || m.Count != 3 || m.Version != DependencyExportVersion || m.Format != DependencyExportFormat {
		t.Fatal(m, err)
	}
	got, records, err := VerifyDependencyExport(dir)
	if err != nil || got != m || len(records) != 3 || records[2].Kind != "self_acknowledgement" || records[2].Position != 3 {
		t.Fatal(got, records, err)
	}
	if _, err := WriteDependencyExport(dir, dependencyQueue, DependencyAudienceRedacted, redactedEntries()); err == nil {
		t.Fatal("overwrote a published export")
	}
	_, manifest, stream, err := EncodeDependencyExport(dependencyQueue, DependencyAudienceRedacted, redactedEntries())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(stream), "\n"), "\n")
	for name, edit := range map[string]func() ([]byte, []byte){
		"flipped byte": func() ([]byte, []byte) { return manifest, bytes.Replace(stream, []byte(`"11"`), []byte(`"12"`), 1) },
		"manifest digest": func() ([]byte, []byte) {
			return bytes.Replace(manifest, []byte(`"count":3`), []byte(`"count":2`), 1), stream
		},
		"unknown manifest": func() ([]byte, []byte) { return bytes.Replace(manifest, []byte(`{`), []byte(`{"extra":1,`), 1), stream },
		"reordered": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{lines[1], lines[0], lines[2]}, DependencyAudienceRedacted)
		},
		"dropped": func() ([]byte, []byte) { return rechain(t, manifest, lines[1:], DependencyAudienceRedacted) },
		"unknown evidence field": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{strings.Replace(lines[0], `{"enabled"`, `{"aaa_secret":"x","enabled"`, 1), lines[1], lines[2]}, DependencyAudienceRedacted)
		},
		"missing evidence field": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{strings.Replace(lines[0], `"enabled":true,`, ``, 1), lines[1], lines[2]}, DependencyAudienceRedacted)
		},
		"private kind": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{strings.Replace(lines[0], `"kind":"policy"`, `"kind":"observation"`, 1), lines[1], lines[2]}, DependencyAudienceRedacted)
		},
		"redacted source": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{strings.Replace(lines[0], `"source":null`, `"source":{"id":4,"recorded_at":"2026-01-01T00:00:00.000000Z","type":"dependency_policy_applied"}`, 1), lines[1], lines[2]}, DependencyAudienceRedacted)
		},
		"projection claims completeness": func() ([]byte, []byte) { return rechain(t, manifest, lines, DependencyAudienceComplete) },
		"non-canonical": func() ([]byte, []byte) {
			return rechain(t, manifest, []string{strings.Replace(lines[0], `{`, `{ `, 1), lines[1], lines[2]}, DependencyAudienceRedacted)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, s := edit()
			if _, err := DecodeDependencyExport(m, s); !errors.Is(err, ErrDependencyExport) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}

func TestDependencyExportCompleteEvidenceIsStrict(t *testing.T) {
	payload := json.RawMessage(`{"version":1,"type":"refresh_requested","data":{}}`)
	entry := DependencyEntry{Kind: "invalidation", Source: &DependencySource{ID: 9, Type: "dependency_compositions_invalidated", RecordedAt: "2026-01-01T00:00:00.000000Z"}, Evidence: payload}
	if _, err := WriteDependencyExport(filepath.Join(t.TempDir(), "x"), dependencyQueue, DependencyAudienceComplete, []DependencyEntry{entry}); err == nil {
		t.Fatal("accepted evidence of another journal type")
	}
	entry.Evidence = json.RawMessage(`{"version":1,"type":"dependency_compositions_invalidated","data":{"batch_ids":["b1"]}}`)
	if _, err := WriteDependencyExport(filepath.Join(t.TempDir(), "x"), dependencyQueue, DependencyAudienceComplete, []DependencyEntry{entry}); err == nil {
		t.Fatal("accepted a malformed journal record")
	}
	if _, err := WriteDependencyExport(filepath.Join(t.TempDir(), "x"), dependencyQueue, DependencyAudienceComplete, redactedEntries()); err == nil {
		t.Fatal("complete export accepted records without journal metadata")
	}
}

// TestDependencyExportVersionBoundary proves the formats cannot be confused:
// the canonical v1/v2 verifier refuses a scoped export before any database
// access, its strict record decoders reject scoped lines, and the scoped
// verifier refuses canonical manifests.
func TestDependencyExportVersionBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scoped")
	if _, err := WriteDependencyExport(dir, dependencyQueue, DependencyAudienceRedacted, redactedEntries()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "replayed")
	if _, err := (&Reader{}).Replay(context.Background(), filepath.Join(dir, "manifest.json"), target); !errors.Is(err, errIntegrity) {
		t.Fatalf("canonical verifier accepted a scoped manifest: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("canonical verifier published output")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var old Manifest
	if strict(bytes.TrimSuffix(raw, []byte("\n")), &old) == nil {
		t.Fatal("canonical manifest decoder accepted a scoped manifest")
	}
	stream, err := os.ReadFile(filepath.Join(dir, dependencyRecordsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(stream), "\n"), "\n") {
		if strict([]byte(line), &Selection{}) == nil || strict([]byte(line), &envelope{}) == nil {
			t.Fatalf("canonical record decoders accepted %s", line)
		}
	}
	for _, version := range []int{1, 2} {
		canonical, err := encode(Manifest{Version: version, Selection: "selection.jsonl", Events: "events.jsonl", Watermarks: "watermarks.jsonl", Dependencies: "dependency_boundaries.jsonl", TerminalSHA256: zeroDigest})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeDependencyExport(append(canonical, '\n'), nil); !errors.Is(err, ErrDependencyExport) {
			t.Fatalf("scoped verifier accepted canonical v%d", version)
		}
	}
}

func TestPageDependencyRecords(t *testing.T) {
	_, manifest, stream, err := EncodeDependencyExport(dependencyQueue, DependencyAudienceRedacted, redactedEntries())
	if err != nil {
		t.Fatal(err)
	}
	records, err := DecodeDependencyExport(manifest, stream)
	if err != nil {
		t.Fatal(err)
	}
	var positions []int64
	var nexts []int64
	for after := int64(0); ; {
		page, err := PageDependencyRecords(records, after, 2)
		if err != nil || page.Total != 3 {
			t.Fatal(page, err)
		}
		for _, r := range page.Records {
			positions = append(positions, r.Position)
		}
		nexts = append(nexts, page.Next)
		if page.Next == 0 {
			break
		}
		after = page.Next
	}
	if len(positions) != 3 || positions[0] != 1 || positions[2] != 3 || len(nexts) != 2 || nexts[0] != 2 {
		t.Fatal(positions, nexts)
	}
	if page, err := PageDependencyRecords(records, 3, 100); err != nil || len(page.Records) != 0 || page.Next != 0 {
		t.Fatal(page, err)
	}
	for _, bad := range []struct {
		after int64
		limit int
	}{{-1, 1}, {4, 1}, {0, 0}, {0, 101}} {
		if _, err := PageDependencyRecords(records, bad.after, bad.limit); !errors.Is(err, ErrSelectors) {
			t.Fatalf("accepted cursor %d limit %d", bad.after, bad.limit)
		}
	}
}

// TestDependencyExportVerifiesLargeStream streams the records file: only each
// line is bounded, so an export larger than MaxBytes that the writer publishes
// verifies, while a line over MaxBytes or an unterminated last line does not.
func TestDependencyExportVerifiesLargeStream(t *testing.T) {
	editor := strings.Repeat("e", 64<<10)
	var entries []DependencyEntry
	for range MaxBytes/len(editor) + 2 {
		entries = append(entries, DependencyEntry{Kind: "declaration", Evidence: DependencyDeclarationEvidence{ChangeID: "chg1_7", Revision: 1, Held: true, Editors: []string{editor}}})
	}
	dir := filepath.Join(t.TempDir(), "export")
	m, err := WriteDependencyExport(dir, dependencyQueue, DependencyAudienceRedacted, entries)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, dependencyRecordsFile))
	if err != nil || info.Size() <= MaxBytes {
		t.Fatalf("records file is not larger than MaxBytes: %v", err)
	}
	got, records, err := VerifyDependencyExport(dir)
	if err != nil || got != m || len(records) != len(entries) {
		t.Fatal(got, len(records), err)
	}
	_, manifest, stream, err := EncodeDependencyExport(dependencyQueue, DependencyAudienceRedacted, redactedEntries())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(stream), "\n"), "\n")
	lines[0] = strings.Repeat(" ", MaxBytes+1)
	long, longStream := rechain(t, manifest, lines, DependencyAudienceRedacted)
	if _, err := DecodeDependencyExport(long, longStream); !errors.Is(err, ErrDependencyExport) {
		t.Fatal("accepted a line over MaxBytes", err)
	}
	if _, err := DecodeDependencyExport(manifest, bytes.TrimSuffix(stream, []byte("\n"))); !errors.Is(err, ErrDependencyExport) {
		t.Fatal("accepted an unterminated last line", err)
	}
}
