package status

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestPublicListenRequiresToken(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8081", "[::]:8081", "192.0.2.1:8081", "[2001:db8::1]:8081", "[::ffff:0.0.0.0]:8081"} {
		if _, err := ListenAddress(address, ""); err == nil {
			t.Fatalf("public bind without token: %s", address)
		}
		if got, err := ListenAddress(address, "secret"); err != nil || got != address {
			t.Fatalf("authenticated bind %s: %q %v", address, got, err)
		}
	}
	for _, tc := range []struct {
		ips  []string
		err  error
		want string
	}{
		{ips: []string{"127.0.0.1", "::1"}, want: "127.0.0.1:8081"},
		{ips: []string{"::1"}, want: "[::1]:8081"},
		{ips: []string{"127.0.0.1", "192.0.2.1"}},
		{ips: []string{"::1", "::"}},
		{err: errors.New("DNS unavailable")},
		{},
	} {
		lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host != "api.example" {
				t.Fatalf("lookup host=%s", host)
			}
			var ips []net.IPAddr
			for _, ip := range tc.ips {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(ip)})
			}
			return ips, tc.err
		}
		got, err := listenAddress(t.Context(), "api.example:8081", "", lookup)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Fatalf("hostname resolution %+v: %q %v", tc, got, err)
		}
	}
}

func TestHTTPBodyLimitIncludingUnknownLength(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		for _, unknownLength := range []bool{false, true} {
			s := &source{journals: []store.Journal{journal()}}
			r := httptest.NewRequest("GET", "/v1/queues", strings.NewReader(strings.Repeat("x", size)))
			if unknownLength {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			Handler(s, []store.QueueKey{key}, "").ServeHTTP(w, r)
			if size == 4096 && w.Code != 200 || size > 4096 && (w.Code != 413 || len(s.keys) != 0 || !strings.Contains(w.Body.String(), `"error":"request-too-large"`)) {
				t.Fatalf("size=%d unknown=%t: %d %s", size, unknownLength, w.Code, w.Body)
			}
		}
	}
	s := &source{}
	r := httptest.NewRequest("GET", "/v1/queues", nil)
	r.Body = io.NopCloser(brokenBody{})
	w := httptest.NewRecorder()
	Handler(s, []store.QueueKey{key}, "").ServeHTTP(w, r)
	if w.Code != 400 || strings.Contains(w.Body.String(), "private-read-error") || len(s.keys) > 0 {
		t.Fatalf("body error leaked or reached DB: %d %s", w.Code, w.Body)
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("private-read-error") }

func TestWhySummariesRedactDiagnosticsAndExposeEveryBlock(t *testing.T) {
	detail := "ghp_private123 Bearer private-token\n\x1b[31m\u202e diagnostic " + strings.Repeat("x", 2000)
	events := []queue.Event{
		queue.PauseRequested{Reason: "landing-not-on-base", Detail: detail},
		queue.ActionPoisoned{Key: "bad", Reason: detail},
		queue.RulesBlocked{Problems: []string{detail}},
	}
	events = append(events, admission()...)
	q, _, why := Project(journal(events...), 1, 20)
	if len(q.Blocks) != 3 || !q.Paused || len(why.Events) != 5 {
		t.Fatalf("missing inherited evidence/blocks: %+v %+v", q, why)
	}
	body, err := json.Marshal(Response{Version: Version, Queue: &q, Why: why})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private123", "private-token", `\u001b`, "\u202e", `"data":`, `"payload":`, `"event":`} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("unsafe/private diagnostic %q exposed", secret)
		}
	}
	for _, event := range why.Events {
		if event.ID == 0 || event.Type == "" || !event.At.Equal(at) || event.Summary == "" || len([]rune(event.Summary)) > 1025 {
			t.Fatalf("unstable/unbounded event: %+v", event)
		}
	}
	events = append(events, queue.RulesCleared{})
	q, _, why = Project(journal(events...), 1, 20)
	if len(q.Blocks) != 2 || !strings.Contains(why.Summary, "action-poisoned") || !strings.Contains(why.Summary, "landing-not-on-base") || strings.Contains(why.Summary, "repository-rules") {
		t.Fatalf("rules recovery hid or cleared another block: %+v %+v", q, why)
	}
}
