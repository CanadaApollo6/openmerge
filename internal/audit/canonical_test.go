package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	must(t, e)
	return b
}

func TestCanonicalV1Golden(t *testing.T) {
	var vectors []struct{ Input, Bytes, SHA256 string }
	must(t, json.Unmarshal(read(t, "testdata/canonical-v1.json"), &vectors))
	for _, v := range vectors {
		out, e := Canonical([]byte(v.Input))
		must(t, e)
		if string(out) != v.Bytes || digest(out) != v.SHA256 {
			t.Fatalf("golden mismatch: %q", v.Input)
		}
		again, e := Canonical(out)
		must(t, e)
		if !bytes.Equal(again, out) {
			t.Fatal("not idempotent")
		}
	}
}

func TestCanonicalV1Rejects(t *testing.T) {
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `"\ud800"`, `"\udfff"`, `"\ud800\u0041"`, string([]byte{'"', 255, '"'}), `1 2`, `NaN`, `1e999999999`, `1e5000000`, strings.Repeat("[", MaxDepth+2) + "0" + strings.Repeat("]", MaxDepth+2), strings.Repeat(" ", MaxBytes+1), `[1e3000000,1e3000000]`} {
		if _, e := Canonical([]byte(s)); e == nil {
			t.Fatalf("accepted invalid input length %d", len(s))
		}
	}
}

func TestCanonicalV1EscapesLineSeparators(t *testing.T) {
	const want = `"\u0085\u2028\u2029"`
	for _, input := range []string{`"\u0085\u2028\u2029"`, `"` + string([]rune{0x0085, 0x2028, 0x2029}) + `"`} {
		got, e := Canonical([]byte(input))
		must(t, e)
		if string(got) != want {
			t.Fatalf("input %q: got %q, want %q", input, got, want)
		}
	}
}

func TestExportV1Golden(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "full"
		if empty {
			name = "empty"
		}
		dir := t.TempDir()
		var m Manifest
		req := Request{Installation: "i", Repository: "owner/repo"}
		source := Source{QueueID: 1, ID: 9007199254740993, Type: "historic", RecordedAt: "2026-10-22T12:00:00.123456Z", Queue: Key{Installation: "i", Repository: "owner/repo", Base: "main", Lane: "é"}}
		must(t, build(dir, req, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
			if empty {
				return nil
			}
			if e := water(watermark{QueueID: 1, Queue: source.Queue, ID: source.ID + 1}); e != nil {
				return e
			}
			if e := emit(sourceEvent{source: source, payload: []byte(`{"type":"historic","unicode":"雪😀<>&","integer":9007199254740993,"decimal":123.4500,"at":"2026-10-22T07:00:00-05:00"}`)}, nil); e != nil {
				return e
			}
			source.ID++
			return emit(sourceEvent{source: source, payload: []byte(`{"type":"historic","decimal":12345e-2}`)}, nil)
		}, nil, &m))
		for _, file := range []string{"events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl", "manifest.json"} {
			got := read(t, filepath.Join(dir, file))
			want := read(t, filepath.Join("testdata", name+"-"+file))
			if !bytes.Equal(got, want) {
				t.Fatalf("%s %s golden mismatch\ngot %s\nwant %s", name, file, got, want)
			}
		}
	}
}

func TestPublicationNoReplaceAndCleanup(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "output")
	fail := func(dir string) error {
		info, e := os.Stat(dir)
		must(t, e)
		if info.Mode().Perm() != 0700 || filepath.Dir(dir) != parent {
			t.Fatal("unsafe staging")
		}
		must(t, os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("truncated"), 0600))
		return errIntegrity
	}
	if e := publish(target, fail); e == nil {
		t.Fatal("published truncated output")
	}
	entries, e := os.ReadDir(parent)
	must(t, e)
	if len(entries) != 0 {
		t.Fatal("failed staging leaked")
	}
	var winner os.FileInfo
	e = publish(target, func(dir string) error {
		must(t, os.Mkdir(target, 0700))
		winner, e = os.Stat(target)
		must(t, e)
		return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("loser"), 0600)
	})
	current, statErr := os.Stat(target)
	must(t, statErr)
	if e == nil || !os.SameFile(winner, current) {
		t.Fatal("target race replaced winner")
	}
	if _, e = os.Stat(filepath.Join(target, "manifest.json")); !os.IsNotExist(e) {
		t.Fatal("losing manifest published")
	}
	entries, e = os.ReadDir(parent)
	must(t, e)
	if len(entries) != 1 {
		t.Fatal("race staging leaked")
	}
	if publish(target, func(string) error { t.Fatal("existing target reached build"); return nil }) == nil {
		t.Fatal("replaced existing target")
	}
}

func TestStrictDecode(t *testing.T) {
	for _, s := range []string{`{"version":1,"future":true}`, `{"version":1,"version":1}`, `{"version":1} {}`, `null`, `{}`} {
		var m Manifest
		if strict([]byte(s), &m) == nil {
			t.Fatal("accepted incomplete or unknown manifest")
		}
	}
}
