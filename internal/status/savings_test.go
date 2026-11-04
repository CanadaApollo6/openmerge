package status

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestSavingsHTTPContractAndValidation(t *testing.T) {
	j := journal(testingEvents()...)
	src := &source{journals: []store.Journal{j}}
	h := Handler(src, []store.QueueKey{key}, "secret", nil)
	path := "/v1/queues/owner/repo/main/reports/savings"
	query := "?since=2026-10-01T00:00:00Z&until=2026-11-01T00:00:00Z&full-suite-minutes=20"
	read := func(target string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
		return w
	}
	w := read(path+query, 200)
	var response SavingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want, err := queuemetrics.ComputeSavings(j, queuemetrics.SavingsWindow{Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), FullSuiteMinutes: 20})
	if err != nil || response.Version != Version || !reflect.DeepEqual(response.Savings, want) {
		t.Fatalf("report mismatch: %+v %v", response, err)
	}
	read(strings.Replace(path, "owner/repo", "wrong/repo", 1)+query, 404)
	read(strings.Replace(path, "/main/", "/release%2Fv1/", 1)+query, 404)
	for _, query := range []string{"", "?since=bad", "?since=2026-10-01T00:00:00Z&until=bad", "?since=2026-10-01T00:00:00Z&until=", "?since=2026-10-01T00:00:00Z&until=2026-09-01T00:00:00Z", "?since=2026-10-01T00:00:00Z&until=2999-01-01T00:00:00Z", "?since=2026-10-01T00:00:00Z&since=2026-10-02T00:00:00Z", "?since=%zz", "?since=2026-10-01T00:00:00Z&lane=x"} {
		if w := read(path+query, 400); !strings.Contains(w.Body.String(), `"invalid-request"`) {
			t.Fatal(w.Body)
		}
	}
	for _, minutes := range []string{"-1", "NaN", "Inf", "oops", ""} {
		read(path+"?since=2026-10-01T00:00:00Z&full-suite-minutes="+minutes, 400)
	}
	before := time.Now()
	w = read(path+"?since=2026-10-01T00:00:00Z", 200)
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Savings.Until.Before(before) || response.Savings.Until.After(time.Now()) {
		t.Fatal(w.Body)
	}
	src.err = errors.New("token=private-provider-detail")
	if w = read(path+query, 503); strings.Contains(w.Body.String(), "private-provider-detail") {
		t.Fatal(w.Body)
	}
	src.err = nil
	src.journals = nil
	read(path+query, 404)
	for _, other := range []store.QueueKey{{Installation: "8", Repository: key.Repository, BaseBranch: key.BaseBranch}, {Installation: key.Installation, Repository: key.Repository, BaseBranch: key.BaseBranch, Lane: "other"}} {
		h = Handler(src, []store.QueueKey{key, other}, "secret", nil)
		if w = read(path+query, 409); !strings.Contains(w.Body.String(), "ambiguous-queue") {
			t.Fatal(w.Body)
		}
	}
	deadline := &savingsDeadlineSource{}
	h = Handler(deadline, []store.QueueKey{key}, "secret", nil)
	if w = read(path+query, 503); !strings.Contains(w.Body.String(), "15 second request deadline") || deadline.deadline <= 14*time.Second || deadline.deadline > 15*time.Second {
		t.Fatal(w.Body, deadline.deadline)
	}
	encoded := key
	encoded.BaseBranch = "release/v1"
	src.journals = []store.Journal{j}
	h = Handler(src, []store.QueueKey{encoded}, "secret", nil)
	read(strings.Replace(path, "/main/", "/release%2Fv1/", 1)+query, 200)
	if !reflect.DeepEqual(src.keys, []store.QueueKey{encoded}) {
		t.Fatal(src.keys)
	}
}

func TestSavingsJournalInputsSurviveDependencyProjection(t *testing.T) {
	j, binding, ref, _ := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	projected, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: nil}, statusfixture.StatusSources())
	if err != nil {
		t.Fatal(err)
	}
	if j.Key != projected.Key || !reflect.DeepEqual(j.Seed, projected.Seed) || !reflect.DeepEqual(j.Events, projected.Events) || !reflect.DeepEqual(j.TreeDedupeShadow, projected.TreeDedupeShadow) {
		t.Fatal("dependency projection changed savings journal inputs")
	}
}

func TestSavingsEncodingFailureIsUnavailable(t *testing.T) {
	j := journal(testingEvents()...)
	j.Events = append(j.Events, store.JournalEvent{ID: 100, RecordedAt: at, Event: queue.TestRefBuilt{BatchID: "b2", TestedSHA: "other"}})
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "", nil)
	r := httptest.NewRequest("GET", "/v1/queues/owner/repo/main/reports/savings?since=2026-10-01T00:00:00Z&full-suite-minutes=1e308", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"unavailable"`) {
		t.Fatal(w.Code, w.Body)
	}
}
