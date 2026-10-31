package status

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

func TestQueueStatusTreeDedupeShadow(t *testing.T) {
	sanitizer := diagnostic.NewSanitizer()
	marshal := func(j store.Journal) string {
		t.Helper()
		q, _, _ := Project(j, 0, 0, sanitizer)
		raw, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// Absent, unreadable and future-format ledgers are omitted entirely.
	for _, raw := range [][]byte{nil, []byte(`{`), []byte(`{"version":2,"builds":[]}`)} {
		if got := marshal(store.Journal{TreeDedupeShadow: raw}); strings.Contains(got, "tree_dedupe_shadow") {
			t.Fatalf("ledger %q leaked into status: %s", raw, got)
		}
	}
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var l treededupe.Ledger
	l.ObserveBuild(treededupe.Build{At: at, BatchID: "b1", SHA: strings.Repeat("1", 40), Kind: treededupe.KindBatch, Key: "k"})
	l.ObserveOutcome(strings.Repeat("1", 40), at.Add(6*time.Minute), true)
	l.ObserveBuild(treededupe.Build{At: at.Add(7 * time.Minute), BatchID: "b2", SHA: strings.Repeat("2", 40), Kind: treededupe.KindBatch, Key: "k"})
	l.ObserveBuild(treededupe.Build{At: at.Add(8 * time.Minute), BatchID: "b3", SHA: strings.Repeat("3", 40), Kind: treededupe.KindBatch, Unknown: []string{"runner_image"}})
	raw, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got := marshal(store.Journal{TreeDedupeShadow: raw})
	want := `"tree_dedupe_shadow":{"mode":"shadow","key_version":"openmerge-tree-dedupe-key-v1","builds":3,"keyed":2,"key_unknown":{"runner_image":1},"would_reuse_success":1,"would_reuse_failure":0,"hits_by_kind":{"batch":1},"avoidable_minutes":6,"agreed":0,"disagreed":0,"retained":3,"dropped":0,"since":"2026-10-31T12:00:00Z"}`
	if !strings.Contains(got, want) {
		t.Fatalf("status shadow = %s", got)
	}
	// Only queue-local evidence feeds the summary: dependency redaction of
	// the same journal leaves it byte-identical.
	redacted := marshal(store.Journal{TreeDedupeShadow: raw, DependencyRedacted: true})
	if !strings.Contains(redacted, want) {
		t.Fatalf("redacted status shadow = %s", redacted)
	}
	// The summary carries counts only, never SHAs, batch IDs or keys.
	for _, secret := range []string{strings.Repeat("1", 40), "b1", `"k"`} {
		if strings.Contains(got[strings.Index(got, "tree_dedupe_shadow"):], secret) {
			t.Fatalf("summary exposes %q", secret)
		}
	}
}
