package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type dependencyCLIJournal struct{ j store.Journal }

func (s dependencyCLIJournal) Journals(_ context.Context, _ []store.QueueKey) ([]store.Journal, error) {
	return []store.Journal{s.j}, nil
}

func TestDependencyCLIPrivacyGolden(t *testing.T) {
	var want []byte
	for i, code := range []string{"missing", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope", "dependency-unresolved"} {
		j, b, _, _ := statusfixture.StatusJournal(t, code, i+1)
		j, err := status.ProjectDependencies(j, b, nil, statusfixture.StatusSources())
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		status.Handler(dependencyCLIJournal{j}, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/acme/client/main/prs/2?events=1", nil))
		var response status.Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := printStatus(&out, response); err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = bytes.Clone(out.Bytes())
		}
		if !bytes.Equal(want, out.Bytes()) {
			t.Fatalf("%s CLI disclosed private source:\n%s", code, &out)
		}
	}
	path := filepath.Join("testdata", "dependency-private-cli.txt")
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.WriteFile(path, want, 0644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(golden, want) {
		t.Fatal("CLI golden differs", err)
	}
}
