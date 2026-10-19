package status

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestChangeTerminalEntryRetainsRequestSanitizer(t *testing.T) {
	j, id, b, _ := briefRegressionBase(t)
	who := cp.Identity{ID: "github.com:user:1", Login: "secret", Kind: cp.HumanAccount}
	briefAppend(&j, queue.Accountability(b, who, who, nil, "digest"))
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
	var entries []string
	for _, path := range []string{changePath(key, string(id)), "/v1/queues/owner/repo/main/prs/1"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var result struct {
			PR json.RawMessage `json:"pr"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var entry Entry
		if err := json.Unmarshal(result.PR, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Change == nil || entry.Change.Accountability.Actor == nil {
			t.Fatal("fixture lacks accountability")
		}
		entries = append(entries, string(result.PR))
		if strings.Contains(string(result.PR), `"login":"secret"`) {
			t.Errorf("request secret exposed in %s", path)
		}
	}
	if entries[0] != entries[1] {
		t.Error("selected Entry DTO redaction differs between change and alias")
	}
}

func TestLedgerEntryRetainsRequestSanitizer(t *testing.T) {
	j, _, b, _ := briefRegressionBase(t)
	who := cp.Identity{ID: "github.com:user:1", Login: "opaque-secret", Kind: cp.HumanAccount}
	briefAppend(&j, queue.Accountability(b, who, who, nil, "digest"))
	q, _, _ := Project(j, 0, 0, diagnostic.NewSanitizer("opaque-secret"))
	entry := q.LedgerEntry(1)
	if entry == nil || entry.Change == nil || entry.Change.Accountability.Actor == nil {
		t.Fatal("missing accountability")
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "opaque-secret") {
		t.Fatal("ledger exposed request secret", string(raw))
	}
}
