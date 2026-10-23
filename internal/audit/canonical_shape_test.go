package audit

import "testing"

func TestCanonicalReferenceShapeGolden(t *testing.T) {
	source := Source{
		QueueID:    7,
		ID:         42,
		Type:       "landing_decision",
		RecordedAt: "2026-10-22T12:34:56.123456Z",
		Queue: Key{
			Installation: "i",
			Repository:   "owner/repo",
			Base:         "main",
			Lane:         "é",
		},
	}
	selection := Selection{
		Source:         source,
		PayloadSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Role:           "dependency",
		References: []Reference{
			{Source: source, Relationship: "requires", Status: "recorded"},
			{Source: source, Relationship: "caused_by", Status: "not_recorded"},
		},
	}
	got, err := encode(selection)
	must(t, err)
	want := `{"metadata_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","references":[{"relationship":"requires","source":{"id":42,"queue":{"base":"main","installation":"i","lane":"é","repository":"owner/repo"},"queue_id":7,"recorded_at":"2026-10-22T12:34:56.123456Z","type":"landing_decision"},"status":"recorded"},{"relationship":"caused_by","source":{"id":42,"queue":{"base":"main","installation":"i","lane":"é","repository":"owner/repo"},"queue_id":7,"recorded_at":"2026-10-22T12:34:56.123456Z","type":"landing_decision"},"status":"not_recorded"}],"role":"dependency","source":{"id":42,"queue":{"base":"main","installation":"i","lane":"é","repository":"owner/repo"},"queue_id":7,"recorded_at":"2026-10-22T12:34:56.123456Z","type":"landing_decision"}}`
	if string(got) != want {
		t.Fatalf("selection shape mismatch\ngot  %s\nwant %s", got, want)
	}
}

func TestCanonicalEnvelopeAndManifestShapeGolden(t *testing.T) {
	source := Source{QueueID: 7, ID: 42, Type: "landing_decision", RecordedAt: "2026-10-22T12:34:56.123456Z", Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main", Lane: "é"}}
	references := []Reference{{Source: source, Relationship: "requires", Status: "not_recorded"}}
	envelopeBytes, err := encode(envelope{
		Kind:           "source",
		Source:         source,
		Payload:        []byte(`{"ok":true}`),
		Role:           "dependency",
		References:     references,
		PreviousSHA256: zeroDigest,
	})
	must(t, err)
	wantEnvelope := `{"kind":"source","payload":{"ok":true},"previous_sha256":"0000000000000000000000000000000000000000000000000000000000000000","references":[{"relationship":"requires","source":{"id":42,"queue":{"base":"main","installation":"i","lane":"é","repository":"owner/repo"},"queue_id":7,"recorded_at":"2026-10-22T12:34:56.123456Z","type":"landing_decision"},"status":"not_recorded"}],"role":"dependency","source":{"id":42,"queue":{"base":"main","installation":"i","lane":"é","repository":"owner/repo"},"queue_id":7,"recorded_at":"2026-10-22T12:34:56.123456Z","type":"landing_decision"}}`
	if string(envelopeBytes) != wantEnvelope {
		t.Fatalf("envelope shape mismatch\ngot  %s\nwant %s", envelopeBytes, wantEnvelope)
	}

	manifestBytes, err := encode(Manifest{
		DependencyBoundaries:       "dependency_boundaries.jsonl",
		DependencyBoundariesSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ManifestSHA256:             "",
		Version:                    1,
		Request:                    Request{Installation: "i", Repository: "owner/repo"},
		Selection:                  "selection.jsonl",
		SelectionSHA256:            zeroDigest,
		Watermarks:                 "watermarks.jsonl",
		WatermarksSHA256:           zeroDigest,
		Events:                     "events.jsonl",
		EventsSHA256:               zeroDigest,
		Count:                      1,
		TerminalSHA256:             zeroDigest,
	})
	must(t, err)
	wantManifest := `{"count":1,"dependency_boundaries":"dependency_boundaries.jsonl","dependency_boundaries_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","events":"events.jsonl","events_sha256":"0000000000000000000000000000000000000000000000000000000000000000","manifest_sha256":"","request":{"from":"","installation":"i","repository":"owner/repo","until":""},"selection":"selection.jsonl","selection_sha256":"0000000000000000000000000000000000000000000000000000000000000000","terminal_sha256":"0000000000000000000000000000000000000000000000000000000000000000","version":1,"watermarks":"watermarks.jsonl","watermarks_sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`
	if string(manifestBytes) != wantManifest {
		t.Fatalf("manifest shape mismatch\ngot  %s\nwant %s", manifestBytes, wantManifest)
	}
}
