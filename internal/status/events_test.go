package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const eventsPath = "/v1/queues/owner/repo/main/events"

func readEvents(t *testing.T, h http.Handler, query string) QueueEventsResponse {
	t.Helper()
	r := httptest.NewRequest("GET", eventsPath+query, nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", query, w.Code, w.Body)
	}
	var response QueueEventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Version != Version || response.Events == nil || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body, w.Header())
	}
	return response
}

func TestQueueEventsPagination(t *testing.T) {
	j := journal(append(testingEvents(), queue.PRMetadataObserved{PR: 2, Title: "private title"}, queue.RefreshRequested{}, queue.PauseRequested{Reason: "operator", Detail: "paused"})...)
	j.Events = append(j.Events, store.JournalEvent{RecordedAt: at, Payload: json.RawMessage(`{"version":1,"type":"change_provenance_observed","data":{"private":"do not expose"}}`)})
	ids := []int64{7, 11, 12, 19, 21, 22, 30, 51, 60}
	for i := range j.Events {
		j.Events[i].ID = ids[i]
	}
	var got []int64
	cursor := int64(0)
	for page := 0; page < 5; page++ {
		// A fresh handler on each page also exercises restart-independent cursors.
		h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
		response := readEvents(t, h, fmt.Sprintf("?after=%d&limit=2", cursor))
		for _, event := range response.Events {
			got = append(got, event.ID)
			if event.ID <= cursor || event.Summary == "" || !event.At.Equal(at) || event.Type == "" {
				t.Fatal(event, cursor)
			}
			if event.ID == 22 && (event.PR != 2 || event.Summary != "Recorded queue event") {
				t.Fatal("metadata lost its alias or exposed a private title", event)
			}
			if event.ID == 60 && (event.Type != "change_provenance_observed" || event.Summary != "Recorded queue event") {
				t.Fatal("side evidence missing", event)
			}
		}
		if response.HasMore != (page < 4) || response.NextCursor != ids[min(2*page+1, len(ids)-1)] {
			t.Fatal(response)
		}
		cursor = response.NextCursor
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("pagination lost or repeated an event: got %v, want %v", got, ids)
	}
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
	for _, after := range []int64{60, 999} {
		response := readEvents(t, h, fmt.Sprintf("?after=%d", after))
		if len(response.Events) != 0 || response.NextCursor != after || response.HasMore {
			t.Fatal(response)
		}
	}
	response := readEvents(t, h, "?after=13&limit=1")
	if len(response.Events) != 1 || response.Events[0].ID != 19 || !response.HasMore {
		t.Fatal(response)
	}
	j.Events = append(j.Events, store.JournalEvent{ID: 80, RecordedAt: at, Event: queue.ResumeRequested{}})
	h = Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
	response = readEvents(t, h, "?after=60")
	if len(response.Events) != 1 || response.Events[0].ID != 80 || response.NextCursor != 80 || response.HasMore {
		t.Fatal("polling missed an append", response)
	}
}

func TestQueueEventsDefaultsAndLimits(t *testing.T) {
	events := make([]queue.Event, 101)
	for i := range events {
		events[i] = queue.RefreshRequested{}
	}
	src := &source{journals: []store.Journal{journal(events...)}}
	h := Handler(src, []store.QueueKey{key}, "secret", nil)
	for _, tc := range []struct {
		query string
		count int
	}{{"", 20}, {"?limit=1", 1}, {"?limit=100", 100}} {
		response := readEvents(t, h, tc.query)
		if len(response.Events) != tc.count || response.NextCursor != int64(tc.count) || !response.HasMore {
			t.Fatal(response)
		}
	}
	src.journals = []store.Journal{journal()}
	if response := readEvents(t, h, ""); len(response.Events) != 0 || response.NextCursor != 0 || response.HasMore {
		t.Fatal(response)
	}
}

func TestQueueEventsHTTPGuards(t *testing.T) {
	src := &source{journals: []store.Journal{journal()}}
	h := Handler(src, []store.QueueKey{key}, "secret", nil)
	check := func(method, path, token string, code int, name string) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != code || !strings.Contains(w.Body.String(), `"error":"`+name+`"`) {
			t.Fatal(path, w.Code, w.Body)
		}
		if code == 405 && w.Header().Get("Allow") != "GET" {
			t.Fatal(w.Header())
		}
	}
	for _, query := range []string{"after=-1", "after=", "after=no", "after=1.5", "after=9223372036854775808", "after=0&after=1", "limit=0", "limit=-1", "limit=101", "limit=", "limit=no", "limit=1.5", "limit=999999999999999999999", "limit=1&limit=2", "unknown=1", "after=%zz", "after=1;limit=1"} {
		check("GET", eventsPath+"?"+query, "secret", 400, "invalid-request")
	}
	for _, token := range []string{"", "wrong"} {
		check("GET", eventsPath, token, 401, "unauthorized")
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		check(method, eventsPath, "secret", 405, "method-not-allowed")
	}
	for _, path := range []string{strings.Replace(eventsPath, "owner/repo", "wrong/repo", 1), strings.Replace(eventsPath, "/main/", "/other/", 1)} {
		check("GET", path, "secret", 404, "not-found")
	}
	for _, other := range []store.QueueKey{{Installation: "8", Repository: key.Repository, BaseBranch: key.BaseBranch}, {Installation: key.Installation, Repository: key.Repository, BaseBranch: key.BaseBranch, Lane: "other"}} {
		h = Handler(src, []store.QueueKey{key, other}, "secret", nil)
		check("GET", eventsPath, "secret", 409, "ambiguous-queue")
	}
	h = Handler(src, []store.QueueKey{key}, "secret", nil)
	src.err = errors.New("private-provider-error")
	check("GET", eventsPath, "secret", 503, "unavailable")
	src.err = nil
	src.journals = nil
	check("GET", eventsPath, "secret", 404, "not-found")
	deadline := &savingsDeadlineSource{}
	h = Handler(deadline, []store.QueueKey{key}, "secret", nil)
	check("GET", eventsPath, "secret", 503, "unavailable")
	if deadline.deadline <= 14*time.Second || deadline.deadline > 15*time.Second {
		t.Fatal(deadline.deadline)
	}
	src.journals = []store.Journal{journal()}
	encoded := key
	encoded.BaseBranch = "release/v1"
	h = Handler(src, []store.QueueKey{encoded}, "secret", nil)
	r := httptest.NewRequest("GET", strings.Replace(eventsPath, "/main/", "/release%2Fv1/", 1), nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !reflect.DeepEqual(src.keys, []store.QueueKey{encoded}) {
		t.Fatal(w.Code, w.Body, src.keys)
	}
}

func TestQueueEventsRedactionMatchesPRDetail(t *testing.T) {
	j, _, basis, _ := briefRegressionBase(t)
	briefAppend(&j, queue.EnqueueRequested{PR: 1, At: at})
	briefAppend(&j, queue.Intent(basis, "untrusted title", "secret private body"))
	briefAppend(&j, queue.PauseRequested{Reason: "operator", Detail: "secret token=private-token\n\x1b[31muntrusted text"})
	for i := range j.Events {
		j.Events[i].ID = int64(i + 1)
		j.Events[i].RecordedAt = at
	}
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", diagnostic.NewSanitizer("private-token"))
	response := readEvents(t, h, "?limit=100")
	if len(response.Events) != len(j.Events) {
		t.Fatal("unformatted events silently skipped", response)
	}
	r := httptest.NewRequest("GET", "/v1/queues/owner/repo/main/prs/1?events=100", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var detail Response
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Why == nil {
		t.Fatal(w.Code, w.Body)
	}
	byID := map[int64]Event{}
	for _, event := range response.Events {
		byID[event.ID] = event.Event
		if strings.Contains(event.Summary, "secret") || strings.Contains(event.Summary, "private-token") || strings.Contains(event.Summary, "private body") {
			t.Fatal(event)
		}
		if event.Type == "change_intent_observed" && (event.PR != 1 || event.Summary != "Recorded queue event") {
			t.Fatal(event)
		}
	}
	if len(detail.Why.Events) == 0 {
		t.Fatal("no PR events compared")
	}
	comparedPause := false
	for _, event := range detail.Why.Events {
		if event.Type == "pause_requested" && strings.Contains(event.Summary, "[REDACTED]") {
			comparedPause = true
		}
		if !reflect.DeepEqual(byID[event.ID], event) {
			t.Fatalf("queue and PR event differ: %+v %+v", byID[event.ID], event)
		}
	}
	if !comparedPause {
		t.Fatal("secret-bearing pause summary was not compared with PR detail")
	}
}

func TestQueueEventsPreserveAllWireKinds(t *testing.T) {
	files, err := filepath.Glob("../queue/testdata/wire/v1/event_*.json")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	j := journal()
	var kinds []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		event, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(file, err)
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, header.Type)
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), RecordedAt: at, Event: event, Payload: raw})
	}
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{key}, "secret", nil)
	response := readEvents(t, h, "?limit=100")
	if len(response.Events) != len(kinds) {
		t.Fatal(len(response.Events), len(kinds))
	}
	var generic []string
	for i, event := range response.Events {
		if event.Summary == "Recorded queue event" {
			generic = append(generic, event.Type)
		}
		if event.Type != kinds[i] || event.ID != int64(i+1) || event.Summary == "" {
			t.Fatal(event, kinds[i])
		}
	}
	t.Logf("preserved %d wire kinds; generic summaries: %s", len(kinds), strings.Join(generic, ", "))
}

func TestQueueEventsRespectDependencyPrivacy(t *testing.T) {
	j, binding, _, _ := statusfixture.StatusJournal(t, "dependency-waiting", 1)
	j, err := ProjectDependencies(j, binding, nil, statusfixture.StatusSources())
	if err != nil || !j.DependencyRedacted {
		t.Fatal(err, j.DependencyRedacted)
	}
	h := Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{j.Key}, "secret", nil)
	r := httptest.NewRequest("GET", "/v1/queues/"+j.Key.Repository+"/"+j.Key.BaseBranch+"/events", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), `"events"`) || strings.Contains(w.Body.String(), "next_cursor") {
		t.Fatal(w.Code, w.Body)
	}
}

type cancelledEventsSource struct{ source }

func (s *cancelledEventsSource) Journals(ctx context.Context, keys []store.QueueKey) ([]store.Journal, error) {
	<-ctx.Done()
	return s.source.Journals(ctx, keys)
}

func TestQueueEventsDoNotServeCancelledSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h := Handler(&cancelledEventsSource{source{journals: []store.Journal{journal()}}}, []store.QueueKey{key}, "", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", eventsPath, nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}
