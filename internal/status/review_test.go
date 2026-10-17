package status

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
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
			Handler(s, []store.QueueKey{key}, "", nil).ServeHTTP(w, r)
			if size == 4096 && w.Code != 200 || size > 4096 && (w.Code != 413 || len(s.keys) != 0 || !strings.Contains(w.Body.String(), `"error":"request-too-large"`)) {
				t.Fatalf("size=%d unknown=%t: %d %s", size, unknownLength, w.Code, w.Body)
			}
		}
	}
	s := &source{}
	r := httptest.NewRequest("GET", "/v1/queues", nil)
	r.Body = io.NopCloser(brokenBody{})
	w := httptest.NewRecorder()
	Handler(s, []store.QueueKey{key}, "", nil).ServeHTTP(w, r)
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
	q, _, why := Project(journal(events...), 1, 20, nil)
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
	events = append(events, queue.RulesCleared{Basis: 1})
	q, _, why = Project(journal(events...), 1, 20, nil)
	if len(q.Blocks) != 2 || !strings.Contains(why.Summary, "action-poisoned") || !strings.Contains(why.Summary, "landing-not-on-base") || strings.Contains(why.Summary, "repository-rules") {
		t.Fatalf("rules recovery hid or cleared another block: %+v %+v", q, why)
	}
}

func TestWhyInactiveQueueExplainsLifecycleWithoutAdmission(t *testing.T) {
	for _, state := range []string{"dormant", "retired"} {
		for _, eligible := range []bool{false, true} {
			t.Run(state+"/eligible="+strconv.FormatBool(eligible), func(t *testing.T) {
				events := []queue.Event{
					queue.LifecycleChanged{State: state, Detail: "base unavailable ghp_private123"},
					queue.PauseRequested{Reason: "landing-not-on-base", Detail: "inspect fence"},
					queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: eligible, Trusted: true},
				}
				q, entry, why := Project(journal(events...), 1, 20, nil)
				if entry.Position != 0 || entry.Status != "paused" || entry.Reason != state || q.State != state {
					t.Fatalf("inactive PR explanation lacks lifecycle: %+v %+v", q, entry)
				}
				if len(why.Events) != 2 || why.Events[0].Type != "lifecycle_changed" {
					t.Fatalf("inactive PR explanation lost lifecycle evidence: %+v", why.Events)
				}
				for _, want := range []string{state, "base unavailable [REDACTED]", "landing-not-on-base", "inspect fence"} {
					if !strings.Contains(why.Summary, want) {
						t.Fatalf("explanation lacks %q: %s", want, why.Summary)
					}
				}
				if strings.Contains(why.Summary, "@openmerge queue") || strings.Contains(why.Summary, "private123") {
					t.Fatalf("unsafe or impossible queue suggestion: %s", why.Summary)
				}
			})
		}
	}
}

func TestStatusRedactsAllProcessSecrets(t *testing.T) {
	const token = "opaque-api-secret"
	sanitizer := diagnostic.NewSanitizer("opaque-webhook-secret", "opaque-private-key-line", "old-installation-secret")
	source := &source{}
	handler := Handler(source, []store.QueueKey{key}, token, sanitizer)
	// A refresh after Handler construction must protect both new and old diagnostics.
	sanitizer.AddSecrets("new-installation-secret")
	for _, secret := range []string{token, "opaque-webhook-secret", "opaque-private-key-line", "old-installation-secret", "new-installation-secret", "Bearer\u00a0unregistered-secret", "Basic\u2003unregistered-secret"} {
		t.Run(secret, func(t *testing.T) {
			events := []queue.Event{
				queue.PauseRequested{Reason: "operator", Detail: "repair " + secret},
				queue.RulesBlocked{Problems: []string{"repair " + secret}},
				queue.ActionPoisoned{Key: "invalid", Reason: "repair " + secret},
			}
			source.journals = []store.Journal{journal(append(events, admission()...)...)}
			for _, path := range []string{"/v1/queues", "/v1/queues/owner/repo/main", "/v1/queues/owner/repo/main/prs/1"} {
				r := httptest.NewRequest("GET", path, nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 200 || strings.Contains(w.Body.String(), strings.Fields(secret)[len(strings.Fields(secret))-1]) || !strings.Contains(w.Body.String(), "[REDACTED]") {
					t.Fatalf("unsafe status response %s: %d %s", path, w.Code, w.Body)
				}
				var response Response
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				queues := response.Queues
				if response.Queue != nil {
					queues = append(queues, *response.Queue)
				}
				for _, q := range queues {
					if q.PauseDetail != "repair [REDACTED]" || len(q.Blocks) != 3 {
						t.Fatalf("missing diagnostic/block: %+v", q)
					}
					for _, block := range q.Blocks {
						if !strings.Contains(block.Detail, "repair [REDACTED]") {
							t.Fatalf("unsafe block: %+v", block)
						}
					}
				}
				if response.Why != nil {
					if !strings.Contains(response.Why.Summary, "[REDACTED]") {
						t.Fatalf("unsafe explanation: %+v", response.Why)
					}
					for _, event := range response.Why.Events[:3] {
						if !strings.Contains(event.Summary, "[REDACTED]") {
							t.Fatalf("unsafe event: %+v", event)
						}
					}
				}
			}
		})
	}
}

func TestStatusKeepsRulesEvidenceWhenStaleClearIsIgnored(t *testing.T) {
	// Failing reads always apply; only the stale clear (basis 1 after the
	// revision reached 2) is ignored.
	events := []queue.Event{
		queue.RulesBlocked{Problems: []string{"initial"}},
		queue.RulesBlocked{Problems: []string{"newer"}, Basis: 1},
		queue.RulesCleared{Basis: 1},
	}
	for _, afterAdmission := range []bool{false, true} {
		sequence := append(slices.Clone(events), admission()...)
		if afterAdmission {
			sequence = append(admission(), events...)
		}
		q, _, why := Project(journal(sequence...), 1, 20, nil)
		if q.PauseDetail != "newer" || !strings.Contains(why.Summary, "newer") {
			t.Fatalf("stale rules event changed explanation: %+v %+v", q, why)
		}
		found := false
		for _, event := range why.Events {
			if event.Type == "rules_cleared" || strings.Contains(event.Summary, "stale") {
				t.Fatalf("ignored event reported as applied: %+v", event)
			}
			found = found || event.Summary == "Repository rules block: newer"
		}
		if !found {
			t.Fatal("lost active rules block evidence")
		}
	}
}

func TestSourceProblemsAndExplanationsRemainIndependent(t *testing.T) {
	events := []queue.Event{
		queue.RulesBlocked{Problems: []string{"repair ruleset"}},
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"repair classic ghp_private123"}},
		queue.RulesSourceBlocked{Source: queue.Permissions, Problems: []string{"repair grants"}},
		queue.RulesSourceCleared{Source: queue.Rulesets, Basis: 1},
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"new classic block ghp_private123"}, Basis: 1},
		queue.RulesSourceCleared{Source: queue.ClassicProtection, Basis: 1}, // stale
	}
	events = append(events, admission()...)
	q, _, why := Project(journal(events...), 1, 20, nil)
	if !q.Paused || len(q.Blocks) != 2 || len(q.RulesSources) != 3 {
		t.Fatalf("missing source problems: %+v", q)
	}
	for i, want := range []struct {
		source   string
		problems int
		revision uint64
	}{{"rulesets", 0, 2}, {"classic_protection", 1, 2}, {"permissions", 1, 1}} {
		got := q.RulesSources[i]
		if got.Source != want.source || len(got.Problems) != want.problems || got.Revision != want.revision {
			t.Fatalf("incorrect source state: %+v", got)
		}
	}
	for _, event := range why.Events {
		if event.ID <= 2 || event.ID == 4 || event.ID == 6 {
			t.Fatalf("cleared/stale source inherited as active evidence: %+v", event)
		}
	}
	if !strings.Contains(why.Summary, "classic_protection") || !strings.Contains(why.Summary, "permissions") {
		t.Fatalf("source names missing from status: %s", why.Summary)
	}
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private123") {
		t.Fatalf("source diagnostics escaped sanitization: %s", body)
	}
	// A legacy clear still targets only rulesets after source-aware events exist.
	events = append(events, queue.RulesCleared{Basis: 2}, queue.RulesSourceCleared{Source: queue.ClassicProtection, Basis: 2})
	q, _, why = Project(journal(events...), 1, 20, nil)
	if len(q.Blocks) != 1 || q.RulesSources[2].Problems[0] != "repair grants" || !strings.Contains(why.Summary, "permissions") {
		t.Fatal("one source's recovery removed another source's block")
	}
}

func TestIndependentBlockStatusAndInheritedEvidence(t *testing.T) {
	events := []queue.Event{
		queue.PauseRequested{Reason: "legacy-hold", Detail: "legacy detail"},
		queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "config ghp_private123"},
		queue.BlockSourceBlocked{Source: queue.ActionPoison, Subject: "action", Detail: "poison detail"},
		queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "new config ghp_private123", Basis: 1},
		queue.BlockSourceCleared{Source: queue.ConfigSource, Basis: 1},
		queue.BlockSourceCleared{Source: queue.Operator, Basis: 1},
	}
	events = append(events, admission()...)
	q, _, why := Project(journal(events...), 1, 20, nil)
	if len(q.Blocks) != 2 || q.Blocks[0].Source != "config" || q.Blocks[0].Revision != 2 || q.Blocks[1].Source != "action-poisoned" {
		t.Fatalf("missing independent block sources: %+v", q.Blocks)
	}
	for _, source := range []string{"config", "action-poisoned"} {
		if !strings.Contains(why.Summary, source) {
			t.Fatal(why.Summary)
		}
	}
	found := map[string]bool{}
	for _, event := range why.Events {
		if event.ID == 1 || event.ID == 2 || event.ID == 5 || event.ID == 6 {
			t.Fatalf("inherited cleared/stale evidence: %+v", event)
		}
		found[event.Type] = true
	}
	if !found["block_source_blocked"] {
		t.Fatal("active source evidence missing")
	}
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private123") {
		t.Fatal("block detail escaped sanitization")
	}
}
