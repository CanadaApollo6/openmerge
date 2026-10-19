package status

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestChangeReportingGoldens(t *testing.T) {
	j, id, _, _ := briefRegressionBase(t)
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	for _, endpoint := range []struct{ name, path string }{
		{"change-show.json", changePath(key, string(id))},
		{"change-brief.json", changePath(key, string(id)) + "/brief?audience=reviewer"},
		{"change-pr-alias.json", "/v1/queues/owner/repo/main/prs/1"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", endpoint.path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		reportingGolden(t, endpoint.name, w.Body.Bytes())
	}
	text, err := ReviewerCheckSummary(j, 1, cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Human, Count: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	reportingGolden(t, "reviewer-check.txt", []byte(text))
}

func reportingGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != string(got) {
		t.Fatalf("reporting golden %s differs: %v", name, err)
	}
}
