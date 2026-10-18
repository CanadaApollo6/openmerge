package queue

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func classificationEvent(t *testing.T) ChangeClassified {
	t.Helper()
	id, err := PRChangeID("7", "Owner/Repo", "release", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	full := forge.DiffCoverage{Completeness: forge.DiffComplete}
	i := cp.ClassificationInputs{ChangeID: string(id), Revision: 1, Generation: 1, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), MergeBase: strings.Repeat("c", 40), GoverningCommit: strings.Repeat("d", 40), PolicyDigest: "semantic-policy"}
	d := forge.RevisionDiff{BaseSHA: i.Base, HeadSHA: i.Head, MergeBaseSHA: i.MergeBase, Semantics: forge.DiffSemantics, Inventory: full, Hunks: full, Files: []forge.DiffFile{{NewPath: "docs/a.md", NewMode: "100644", NewObjectID: strings.Repeat("e", 40), BinaryKnown: true, Rename: forge.DiffNotRenamed, HunkCoverage: full, Hunks: []forge.DiffHunk{{NewStart: 1, NewLines: 1, Text: "+hello\n"}}}}}
	g := forge.GoverningFiles{CommitSHA: i.GoverningCommit, Semantics: forge.DiffSemantics, Coverage: full, Config: []forge.GoverningFile{{Path: ".openmerge.yml", Present: true, Mode: "100644", ObjectID: strings.Repeat("f", 40), Content: "version: 1", Coverage: full}}, Codeowners: []forge.GoverningFile{{Path: ".github/CODEOWNERS", Coverage: full}, {Path: "CODEOWNERS", Present: true, Mode: "100644", ObjectID: strings.Repeat("1", 40), Content: "* @owner", Coverage: full}}, SelectedCodeowners: "CODEOWNERS", Attributes: []forge.GoverningFile{{Path: ".gitattributes", Coverage: full}, {Path: "docs/.gitattributes", Coverage: full}}}
	e, err := classification.Classify(i, d, g, classification.Settings{Docs: []string{"docs/**"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ChangeClassified{Evidence: e}
}
func TestClassificationWireAndVariants(t *testing.T) {
	for _, name := range []string{"event_change_classified.json", "variants/classified_complete.json", "variants/classified_incomplete.json"} {
		raw, err := os.ReadFile("testdata/wire/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, again, err := EncodeEvent(event)
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatal("wire round trip", err)
		}
	}
	e := classificationEvent(t)
	_, raw, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/wire/v1/event_change_classified.json")
	if err != nil || !bytes.Equal(raw, golden) {
		t.Fatal("classification golden drift", err)
	}
	// Strict nested structure presence, null/unknown fields and envelope version.
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "evidence") },
		func(v map[string]any) { v["extra"] = true },
		func(v map[string]any) { v["evidence"].(map[string]any)["extra"] = true },
		func(v map[string]any) {
			delete(v["evidence"].(map[string]any)["Classification"].(map[string]any), "HunksComplete")
		},
		func(v map[string]any) {
			delete(v["evidence"].(map[string]any)["Classification"].(map[string]any)["Inputs"].(map[string]any), "Generation")
		},
		func(v map[string]any) {
			v["evidence"].(map[string]any)["Classification"].(map[string]any)["Facts"] = nil
		},
	} {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		mutate(v["data"].(map[string]any))
		invalid, _ := json.Marshal(v)
		if _, err := DecodeEvent(invalid); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}
func TestClassificationSnapshotTailAndImmutableApply(t *testing.T) {
	first := classificationEvent(t)
	second := detachedClassification(first)
	second.Evidence.Classification.Inputs.Generation = 2
	second.Evidence.Classification.Inputs.Base = strings.Repeat("2", 40)
	second.Evidence.InputFingerprint = strings.Repeat("3", 64)
	events := []Event{first, second, PRObserved{12, "new-head", true, true}}
	for split := 0; split <= len(events); split++ {
		full := State{}
		for _, e := range events[:split] {
			full = Apply(full, e)
		}
		raw, err := EncodeSnapshot(full)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DecodeSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events[split:] {
			full = Apply(full, e)
			restored = Apply(restored, e)
		}
		a, _ := EncodeSnapshot(full)
		b, _ := EncodeSnapshot(restored)
		if !bytes.Equal(a, b) {
			t.Fatalf("snapshot-plus-tail != full replay at %d", split)
		}
	}
	old := Apply(State{}, first)
	before, _ := EncodeSnapshot(old)
	next := Apply(old, second)
	second.Evidence.Classification.Files[0].Hunks.Hunks[0].Text = "mutated caller"
	id := ChangeID(first.Evidence.Classification.Inputs.ChangeID)
	read, _ := next.Classification(id, 1)
	read.Evidence.Governing.Config[0].Content = "mutated getter"
	read.Evidence.Classification.Facts["docs"] = cp.False
	after, _ := EncodeSnapshot(old)
	if !bytes.Equal(before, after) {
		t.Fatal("Apply mutated old state")
	}
	read, _ = next.Classification(id, 1)
	if read.Evidence.Classification.Files[0].Hunks.Hunks[0].Text == "mutated caller" || read.Evidence.Governing.Config[0].Content == "mutated getter" || read.Evidence.Classification.Facts["docs"] != cp.True {
		t.Fatal("aliased classification")
	}
	// Equal-diff rewrites still advance generation and reject an old replay.
	stale := Apply(next, first)
	got, _ := stale.Classification(id, 1)
	if got.Evidence.Classification.Inputs.Generation != 2 {
		t.Fatal("old generation replayed")
	}
	if old.revision != next.revision {
		t.Fatal("classification changed planner revision")
	}
}
func TestClassificationDormancyAndLegacyBindings(t *testing.T) {
	cfg, s, now := chainState(t, 4, 2)
	s, _ = admitChain(t, cfg, s, now)
	if s.classifications != nil {
		t.Fatal("legacy queue populated classification")
	}
	before := Plan(cfg, s, now.Add(time.Hour))
	s = Apply(s, classificationEvent(t))
	if !reflect.DeepEqual(before, Plan(cfg, s, now.Add(time.Hour))) {
		t.Fatal("evidence changed legacy plan")
	}
	id, _ := PRChangeID("7", "Owner/Repo", "main", "", 12)
	record, err := BoundPRRevision(RevisionRecord{Revision: 2, HeadSHA: "h"}, id)
	if err != nil || record.ChangeID != id {
		t.Fatal(record, err)
	}
	record, err = BoundBatchRevision(BatchRevision{PR: 12, Revision: 1, HeadSHA: "old-head"}, id)
	if err != nil || record.Revision != 1 || record.HeadSHA != "old-head" {
		t.Fatal(record, err)
	}
	if _, err := BoundBatchRevision(BatchRevision{PR: 12, HeadSHA: "old-head"}, id); err == nil {
		t.Fatal("invented unrecorded batch revision")
	}
	if _, err := BoundPRRevision(RevisionRecord{Revision: 1, HeadSHA: "h"}, ""); err == nil {
		t.Fatal("empty ID bound")
	}
}
func TestClassificationDecodesVersion3(t *testing.T) {
	raw, err := EncodeSnapshot(State{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["Version"] = json.RawMessage(`3`)
	delete(fields, "Classifications")
	raw, _ = json.Marshal(fields)
	s, err := DecodeSnapshot(raw)
	if err != nil || s.classifications != nil {
		t.Fatal("v3 compatibility", err)
	}
	fields["Version"] = json.RawMessage(`4`)
	raw, _ = json.Marshal(fields)
	if _, err := DecodeSnapshot(raw); err == nil {
		t.Fatal("v4 accepted missing projection")
	}
}

func TestClassificationGenerationRejectsRebinding(t *testing.T) {
	first := classificationEvent(t)
	s := Apply(State{}, first)
	for _, mutate := range []func(*ChangeClassified){
		func(e *ChangeClassified) { e.Evidence.Classification.Facts["docs"] = cp.False },
		func(e *ChangeClassified) { e.Evidence.Classification.Inputs.Base = strings.Repeat("9", 40) },
		func(e *ChangeClassified) { e.Evidence.Classification.Inputs.ClassifierVersion = "new-version" },
		func(e *ChangeClassified) { e.Evidence.Classification.Inputs.Generation = 3 },
		func(e *ChangeClassified) {
			e.Evidence.Classification.Inputs.Generation = 2
			e.Evidence.Classification.Inputs.Head = strings.Repeat("9", 40)
		},
	} {
		e := detachedClassification(first)
		mutate(&e)
		got := Apply(s, e)
		a, _ := EncodeSnapshot(s)
		b, _ := EncodeSnapshot(got)
		if !bytes.Equal(a, b) {
			t.Fatal("rebound semantic generation")
		}
	}
	provenance := detachedClassification(first)
	provenance.Evidence.Classification.Inputs.ConfigBlob = "format-only"
	provenance.Evidence.Classification.Inputs.GoverningCommit = strings.Repeat("8", 40)
	provenance.Evidence.Governing.CommitSHA = strings.Repeat("8", 40)
	provenance.Evidence.Governing.Config[0].ObjectID = "format-only"
	provenance.Evidence.InputFingerprint = strings.Repeat("8", 64)
	got := Apply(s, provenance)
	record, _ := got.Classification(ChangeID(first.Evidence.Classification.Inputs.ChangeID), 1)
	if record.Evidence.Classification.Inputs.ConfigBlob != "format-only" || record.Evidence.Classification.Inputs.Generation != 1 {
		t.Fatal("provenance-only refresh invalidated generation")
	}
}
