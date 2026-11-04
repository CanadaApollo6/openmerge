package status

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

func TestSavingsHTTPContractAndValidation(t *testing.T) {
	j := journal(testingEvents()...)
	j.TreeDedupeShadow = savingsShadowLedger(t, at)
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

func savingsShadowLedger(t *testing.T, at time.Time) []byte {
	t.Helper()
	ledger := treededupe.Ledger{}
	ledger.ObserveBuild(treededupe.Build{At: at.Add(-3 * time.Minute), BatchID: "source", SHA: strings.Repeat("a", 40), Kind: treededupe.KindBatch, Key: "same-tree"})
	ledger.ObserveOutcome(strings.Repeat("a", 40), at.Add(-2*time.Minute), true)
	ledger.ObserveBuild(treededupe.Build{At: at.Add(-time.Minute), BatchID: "hit", SHA: strings.Repeat("b", 40), Kind: treededupe.KindBatch, Key: "same-tree"})
	raw, err := ledger.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSavingsJournalInputsSurviveDependencyProjection(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	at := j.Events[0].RecordedAt
	j.TreeDedupeShadow = savingsShadowLedger(t, at)
	j.OutsideWarnings = store.OutsideWarningSummary{Outstanding: 1, EventIDs: []int64{1}, Truncated: true, Error: "fixture-warning"}
	j.HasAuthorityPin = true
	j.ProvenanceCapacity = &store.ProvenanceCapacity{FenceCount: 1, ProjectedBytes: 10, LimitBytes: 100, HighWaterBytes: 80}
	for i := range j.Events {
		dedupe := fmt.Sprintf("fixture-%d", i)
		j.Events[i].DedupeKey = &dedupe
	}
	// New journal inputs need a nonzero fixture before projection parity can cover them.
	value := reflect.ValueOf(j)
	for i := 0; i < value.NumField(); i++ {
		switch value.Type().Field(i).Name {
		case "DependencyQueue", "DependencyRedacted", "DependencyStatus":
		default:
			if value.Field(i).IsZero() {
				t.Fatalf("populate journal field %s", value.Type().Field(i).Name)
			}
		}
	}
	window := queuemetrics.SavingsWindow{Since: at.Add(-time.Hour), Until: at.Add(time.Hour), FullSuiteMinutes: 20}
	want, err := queuemetrics.ComputeSavings(j, window)
	if err != nil {
		t.Fatal(err)
	}
	if want.Shadow == nil || want.Shadow.WouldReuseSuccess != 1 {
		t.Fatal("fixture has no shadow savings", want.Shadow)
	}
	for name, grants := range map[string][]dependency.Grant{"authorized": {grant}, "redacted": nil} {
		t.Run(name, func(t *testing.T) {
			projected, err := ProjectDependencies(j, binding, map[string][]dependency.Grant{ref: grants}, statusfixture.StatusSources())
			if err != nil {
				t.Fatal(err)
			}
			got, err := queuemetrics.ComputeSavings(projected, window)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("projected savings differ: got %+v, want %+v, err %v", got, want, err)
			}
			// These are the only additive dependency presentation fields.
			projected.DependencyQueue = j.DependencyQueue
			projected.DependencyRedacted = j.DependencyRedacted
			projected.DependencyStatus = j.DependencyStatus
			if !reflect.DeepEqual(j, projected) {
				t.Fatal("dependency projection changed the persisted journal")
			}
		})
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
