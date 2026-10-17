package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var at = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
var key = store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}

func journal(events ...queue.Event) store.Journal {
	j := store.Journal{Key: key, Seed: queue.State{BaseBranch: key.BaseBranch, Fence: "fence"}}
	for i, event := range events {
		_, payload, err := queue.EncodeEvent(event)
		if err != nil {
			panic(err)
		}
		j.Events = append(j.Events, store.JournalEvent{ID: int64(i + 1), RecordedAt: at, Payload: payload, Event: event})
	}
	return j
}

func admission() []queue.Event {
	return []queue.Event{queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: at}}
}

func testingEvents() []queue.Event {
	return append(admission(), queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Squash})
}

func TestExplainCurrentStateWithRelevantBoundedHistory(t *testing.T) {
	events := append(testingEvents(), queue.PRObserved{PR: 2, HeadSHA: "other", Eligible: true, Trusted: true}, queue.ChecksCompleted{TestedSHA: "other", Success: true}, queue.PauseRequested{Reason: "repository-rules", Detail: "fence missing deletion"})
	q, pr, why := Project(journal(events...), 1, 2, nil)
	if !q.Paused || q.PauseReason != "repository-rules" || q.ActiveBatch.ID != "b1" || len(q.Entries) != 1 || q.Watermark != int64(len(events)) {
		t.Fatalf("queue=%+v", q)
	}
	if pr.Status != "paused" || pr.Position != 1 || pr.HeadSHA != "head" || !pr.AdmittedAt.Equal(at) || !strings.Contains(why.Summary, "fence missing deletion") {
		t.Fatalf("pr=%+v why=%+v", pr, why)
	}
	if len(why.Events) != 2 || why.Events[0].ID != 5 || why.Events[1].ID != 8 {
		t.Fatalf("unrelated events or incorrect ordering: %+v", why.Events)
	}
	if !strings.Contains(why.Events[0].Type, "test_ref_built") {
		t.Fatal("lost event type")
	}
	_, missing, _ := Project(journal(events...), 99, 20, nil)
	if missing != nil {
		t.Fatal("invented an unknown PR")
	}
}

func TestOutcomesAndReadmission(t *testing.T) {
	events := append(admission(), queue.DequeueRequested{PR: 1}, queue.Dequeued{PR: 1, AdmissionID: 999, Reason: queue.Requested})
	_, pr, _ := Project(journal(events...), 1, 20, nil)
	if pr.Status != "dequeue-pending" || pr.LastDequeueReason != "" {
		t.Fatalf("stale dequeue invented an outcome: %+v", pr)
	}
	events = append(events, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.Requested})
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Status != "dequeued" || pr.Position != 0 || pr.LastDequeueReason != "requested" {
		t.Fatalf("dequeue outcome=%+v", pr)
	}
	events = append(events, queue.EnqueueRequested{PR: 1, At: at.Add(time.Hour)})
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Status != "queued" || pr.Position != 1 || pr.LastDequeueReason != "requested" || !pr.AdmittedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("readmission=%+v", pr)
	}
}

func TestRepushPinsAdmissionAndReopenExplainsNewObservation(t *testing.T) {
	events := append(admission(), queue.PRObserved{PR: 1, HeadSHA: "new-head", Eligible: true, Trusted: true})
	_, pr, _ := Project(journal(events...), 1, 20, nil)
	if pr.Status != "dequeue-pending" || pr.HeadSHA != "head" || pr.Reason != "head-changed" {
		t.Fatalf("repush changed admitted head: %+v", pr)
	}
	events = append(events, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.HeadChanged})
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Status != "dequeued" || pr.HeadSHA != "new-head" || pr.LastDequeueReason != "head-changed" {
		t.Fatalf("dequeued PR lost current observation: %+v", pr)
	}
	events = []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.PRClosed{PR: 1}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}}
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Status != "not-queued" {
		t.Fatalf("reopen retained closed state: %+v", pr)
	}
}

func TestLandingWhilePausedIsUncertainUntilSettled(t *testing.T) {
	events := append(testingEvents(), queue.ChecksCompleted{TestedSHA: "tested", Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: at}, queue.DequeueRequested{PR: 1}, queue.PauseRequested{Reason: "repository-rules", Detail: "bad fence"})
	_, pr, why := Project(journal(events...), 1, 20, nil)
	if pr.Status != "paused" || !strings.Contains(why.Summary, "still being resolved") {
		t.Fatalf("uncertain landing lost: %+v %+v", pr, why)
	}
	events = append(events, queue.Landed{BatchID: "stale", NewBaseSHA: "wrong", NewFence: "wrong"})
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Position != 1 {
		t.Fatal("stale landing removed PR")
	}
	events = append(events, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"}, queue.PRClosed{PR: 1})
	_, pr, _ = Project(journal(events...), 1, 20, nil)
	if pr.Status != "merged" || pr.Position != 0 {
		t.Fatalf("closed observation erased merge: %+v", pr)
	}
}

func TestExplanationIncludesRetainedCIBeforeTestRefWasRecorded(t *testing.T) {
	events := append(admission(), queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at}, queue.ChecksCompleted{TestedSHA: "tested", Success: true}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Squash})
	_, _, why := Project(journal(events...), 1, 2, nil)
	if len(why.Events) != 2 || why.Events[0].ID != 5 || why.Events[1].ID != 6 {
		t.Fatalf("retained CI evidence lost: %+v", why.Events)
	}
}

func TestExplanationIncludesCurrentPauseInheritedAtAdmission(t *testing.T) {
	events := append([]queue.Event{queue.PauseRequested{Reason: "repository-rules", Detail: "fence rules need repair"}}, admission()...)
	_, pr, why := Project(journal(events...), 1, 20, nil)
	if pr.Status != "paused" || len(why.Events) == 0 || why.Events[0].ID != 1 {
		t.Fatalf("admission inherited an unexplained pause: %+v %+v", pr, why)
	}
}

func TestBlockedPRAndEmptyStableJSON(t *testing.T) {
	_, pr, why := Project(journal(queue.PRObserved{PR: 1, HeadSHA: "fork", Eligible: true}, queue.EnqueueRequested{PR: 1, At: at}), 1, 20, nil)
	if pr.Status != "blocked" || pr.Reason != "untrusted" || !strings.Contains(why.Summary, "untrusted") {
		t.Fatalf("untrusted admission=%+v %+v", pr, why)
	}
	q, _, _ := Project(journal(), 0, 0, nil)
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"installation":"7","repo":"owner/repo","base":"main","lane":"","watermark":0,"base_sha":"","paused":false,"pause_reason":"","pause_detail":"","blocks":[],"active_batch":null,"entries":[]}`
	if string(data) != want {
		t.Fatalf("schema drift: %s", data)
	}
}

type source struct {
	journals []store.Journal
	keys     []store.QueueKey
	err      error
}

func (s *source) Journals(_ context.Context, keys []store.QueueKey) ([]store.Journal, error) {
	s.keys = keys
	return s.journals, s.err
}

func TestHTTPReadOnlyAuthRoutingAndErrors(t *testing.T) {
	s := &source{journals: []store.Journal{journal(testingEvents()...)}}
	other := key
	other.Repository = "other/private"
	handler := Handler(s, []store.QueueKey{other, key}, "secret", nil)
	for _, tc := range []struct {
		method, path, token string
		code                int
	}{
		{"GET", "/v1/queues", "", 401},
		{"GET", "/v1/queues", "Bearer wrong", 401},
		{"POST", "/v1/queues", "Bearer secret", 405},
		{"HEAD", "/v1/queues", "Bearer secret", 405},
		{"GET", "/v1/queues/owner/repo/main", "Bearer secret", 200},
		{"GET", "/v1/queues/owner/repo/main/prs/1?events=2", "Bearer secret", 200},
		{"GET", "/v1/queues/owner/repo/main/prs/99", "Bearer secret", 404},
		{"GET", "/v1/queues/owner/repo/main/prs/0", "Bearer secret", 400},
		{"GET", "/v1/queues/owner/repo/main/prs/1?events=101", "Bearer secret", 400},
		{"GET", "/v1/queues/owner/repo/main/prs/1?events=0", "Bearer secret", 400},
		{"GET", "/v1/queues/owner/repo/main/prs/1?events=1&events=2", "Bearer secret", 400},
		{"GET", "/v1/queues?events=2", "Bearer secret", 400},
		{"GET", "/v1/queues/unserved/repo/main", "Bearer secret", 404},
		{"GET", "/v2/queues", "Bearer secret", 404},
		{"GET", "/", "Bearer secret", 404},
	} {
		t.Run(tc.method+tc.path+tc.token, func(t *testing.T) {
			s.keys = nil
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response=%d %s %s", w.Code, w.Header(), w.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["version"] != float64(1) {
				t.Fatalf("unversioned response: %s", w.Body)
			}
			if tc.code == 200 && !reflect.DeepEqual(s.keys, []store.QueueKey{key}) {
				t.Fatalf("read outside selected queue: %+v", s.keys)
			}
			if tc.code != 200 && !strings.Contains(tc.path, "prs/99") && len(s.keys) != 0 {
				t.Fatal("rejected request touched DB")
			}
		})
	}
	s.err = errors.New("postgres://private-credentials")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authorized("/v1/queues"))
	if w.Code != 503 || strings.Contains(w.Body.String(), "private-credentials") {
		t.Fatalf("DB failure=%s", w.Body)
	}
}

func authorized(path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer secret")
	return r
}

func TestEmptyListSlashBaseAndAmbiguity(t *testing.T) {
	s := &source{}
	w := httptest.NewRecorder()
	Handler(s, nil, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues", nil))
	if w.Body.String() != "{\"version\":1,\"queues\":[]}\n" {
		t.Fatalf("empty list=%s", w.Body)
	}
	slash := key
	slash.BaseBranch = "release/v1"
	j := journal(admission()...)
	j.Key, j.Seed.BaseBranch = slash, slash.BaseBranch
	s.journals = []store.Journal{j}
	w = httptest.NewRecorder()
	Handler(s, []store.QueueKey{slash}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/owner/repo/release%2Fv1", nil))
	if w.Code != 200 || !reflect.DeepEqual(s.keys, []store.QueueKey{slash}) {
		t.Fatalf("encoded base=%d %+v", w.Code, s.keys)
	}
	other := key
	other.Installation = "8"
	w = httptest.NewRecorder()
	Handler(s, []store.QueueKey{key, other}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/owner/repo/main", nil))
	if w.Code != 409 {
		t.Fatal("ambiguous queue silently selected")
	}
}

func TestListenAndTokens(t *testing.T) {
	for input, want := range map[string]string{"": "", ":8081": "127.0.0.1:8081", "[::1]:0": "[::1]:0"} {
		got, err := ListenAddress(input, "")
		if err != nil || got != want {
			t.Fatalf("listen %q=%q %v", input, got, err)
		}
	}
	for _, input := range []string{"8081", "localhost", ":-1", ":65536", ":http"} {
		if _, err := ListenAddress(input, ""); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadToken("", file); err != nil || token != "secret" {
		t.Fatalf("file token=%q %v", token, err)
	}
	for _, tc := range [][2]string{{"env", file}, {"", file + ".missing"}, {"has space", ""}, {"line\nbreak", ""}} {
		if _, err := ReadToken(tc[0], tc[1]); err == nil {
			t.Fatal("accepted invalid token configuration")
		}
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken("", file); err == nil {
		t.Fatal("empty token file silently disabled auth")
	}
}

func TestConfigAppliedSummaryExplainsRebuild(t *testing.T) {
	active := queue.State{Active: &queue.Batch{ID: "b1"}}
	idle := queue.State{}
	event := queue.ConfigApplied{Commit: "0123456789abcdef0123", Digest: strings.Repeat("a", 64)}
	if got := eventSummary(event, active, idle); !strings.Contains(got, "rebuilt the active batch") || !strings.Contains(got, "0123456789ab") || strings.Contains(got, "0123456789abc") {
		t.Fatalf("rebuild not explained: %q", got)
	}
	if got := eventSummary(event, idle, idle); strings.Contains(got, "rebuilt") || !strings.Contains(got, "Applied configuration") {
		t.Fatalf("unchanged batch misreported: %q", got)
	}
}
