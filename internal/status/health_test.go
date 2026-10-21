package status

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

type healthSource struct {
	calls  int
	health Health
	err    error
}

func (s *healthSource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	panic("health read journal")
}
func (s *healthSource) Health(ctx context.Context) (Health, error) {
	s.calls++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > HealthTimeout {
		panic("missing health deadline")
	}
	return s.health, s.err
}

func TestHealthAndLiveRouting(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		s := &healthSource{health: Health{Version: Version, Healthy: true, Queues: []HealthQueue{}}}
		keyCalls := 0
		h := Handler(s, nil, "secret", nil)
		if dynamic {
			h = DynamicHandler(s, func() []store.QueueKey { keyCalls++; return nil }, "secret", nil)
		}
		for _, tc := range []struct {
			method, path, body, token string
			want                      int
		}{
			{"GET", "/v1/live", "", "", 200},
			{"POST", "/v1/live", "", "", 405},
			{"GET", "/v1/live?x=1", "", "", 400},
			{"GET", "/v1/live?", "", "", 400},
			{"GET", "/v1/live", "x", "", 400},
			{"GET", "/v1/health", "", "", 401},
			{"GET", "/v1/health?x=1", "", "", 401},
			{"GET", "/v1/live/", "", "", 401},
			{"GET", "/v1/%6cive", "", "", 401},
			{"GET", "/v1/health", "", "secret", 200},
			{"POST", "/v1/health", "", "secret", 405},
			{"GET", "/v1/health?x=1", "", "secret", 400},
			{"GET", "/v1/health", "x", "secret", 400},
		} {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rr := httptest.NewRecorder()
			before := s.calls
			beforeKeys := keyCalls
			h.ServeHTTP(rr, r)
			if strings.HasPrefix(tc.path, "/v1/health") || tc.path == "/v1/live" || strings.HasPrefix(tc.path, "/v1/live?") {
				if keyCalls != beforeKeys {
					t.Fatal("probe discovers keys")
				}
			}
			if rr.Code != tc.want {
				t.Fatalf("dynamic=%t %s %s: %d %s", dynamic, tc.method, tc.path, rr.Code, rr.Body)
			}
			if tc.path != "/v1/health" || tc.want != 200 {
				if s.calls != before {
					t.Fatal("invalid/unauthorized probe read health")
				}
			}
			if tc.path == "/v1/live" && tc.want == 200 && rr.Body.String() != "{\"live\":true}\n" {
				t.Fatal(rr.Body)
			}
			if tc.want == 401 && strings.Contains(rr.Body.String(), "queues") {
				t.Fatal("unauthorized queue details")
			}
		}
	}
}

func TestHealthFailureAndBounds(t *testing.T) {
	oversized := make([]HealthQueue, MaxHealthQueues)
	for i := range oversized {
		oversized[i].Repository = strings.Repeat("x", 2048)
	}
	for _, tc := range []struct {
		name string
		s    healthSource
		want int
	}{
		{"db failure", healthSource{err: errors.New("private DB details")}, 503},
		{"unhealthy", healthSource{health: Health{Healthy: false}}, 503},
		{"too many queues", healthSource{health: Health{Healthy: true, Queues: make([]HealthQueue, MaxHealthQueues+1)}}, 503},
		{"too large", healthSource{health: Health{Healthy: true, Queues: oversized}}, 503},
		{"warnings", healthSource{health: Health{Healthy: true, Queues: []HealthQueue{{Authority: &Authority{State: "unverified"}, OutsideWarnings: &OutsideWarnings{Outstanding: 42, EventIDs: []int64{9}, Truncated: true}}}}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			Handler(&tc.s, nil, "", nil).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/health", nil))
			if rr.Code != tc.want || rr.Body.Len() > MaxHealthBytes || strings.Contains(rr.Body.String(), "private DB") {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			if tc.name == "warnings" && (!strings.Contains(rr.Body.String(), `"outstanding":42`) || !strings.Contains(rr.Body.String(), `"state":"unverified"`)) {
				t.Fatal(rr.Body)
			}
		})
	}
}

func TestEvaluateHealth(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-2 * time.Minute)
	recent := now.Add(-15 * time.Second)
	due := now.Add(-time.Minute)
	settings := DefaultHealthSettings()
	for _, tc := range []struct {
		name                       string
		q                          HealthQueue
		overdue, degraded, failing bool
	}{
		{"new", HealthQueue{Watched: true, StartedAt: now}, false, false, false},
		{"cycle missing both timestamps", HealthQueue{Watched: true, InCycle: true, StartedAt: now}, true, false, true},
		{"cycle missing progress", HealthQueue{Watched: true, InCycle: true, StartedAt: now, CycleStartedAt: &now}, true, false, true},
		{"cycle missing start", HealthQueue{Watched: true, InCycle: true, StartedAt: now, LastProgressAt: &now}, true, false, true},

		{"overdue", HealthQueue{Watched: true, StartedAt: now, NextDeadline: &due}, true, false, true},
		{"stale success", HealthQueue{Watched: true, LastSuccess: &past}, false, false, false},
		{"never succeeded", HealthQueue{Watched: true, StartedAt: now.Add(-time.Hour)}, false, true, true},
		{"recent success after three failures", HealthQueue{Watched: true, LastSuccess: &recent, Failures: 3}, false, false, false},
		{"repeated failures", HealthQueue{Watched: true, StartedAt: now, Failures: 3}, false, false, false},
		{"violation", HealthQueue{Watched: true, StartedAt: now, Authority: &Authority{State: "violated"}}, false, false, true},
		{"unverified", HealthQueue{Watched: true, StartedAt: now, Authority: &Authority{State: "unverified"}}, false, false, false},
		{"retired", HealthQueue{Watched: false, Failures: 8, NextDeadline: &due, Authority: &Authority{State: "violated"}}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := EvaluateHealth(tc.q, settings, now)
			if q.Overdue != tc.overdue || q.Degraded != tc.degraded || q.Failing() != tc.failing {
				t.Fatalf("%+v", q)
			}
		})
	}
	q := EvaluateHealth(HealthQueue{LastSuccess: &past}, settings, now)
	if q.LastSuccessAge == nil || *q.LastSuccessAge != 120 {
		t.Fatal("wrong success age")
	}
	for _, bad := range []HealthSettings{{0, time.Minute, 3 * time.Hour, 150 * time.Second}, {time.Second, 0, 3 * time.Hour, 150 * time.Second}, {61 * time.Second, time.Minute, 3 * time.Hour, 150 * time.Second}, {time.Second, 61 * time.Minute, 3 * time.Hour, 150 * time.Second}, {time.Second, time.Minute, time.Minute, 150 * time.Second}, {time.Second, time.Minute, 7 * time.Hour, 150 * time.Second}} {
		if bad.Valid() {
			t.Fatal("unbounded settings accepted")
		}
	}
}

type cancelledHealthSource struct{ healthSource }

func (cancelledHealthSource) Health(ctx context.Context) (Health, error) {
	<-ctx.Done()
	return Health{}, ctx.Err()
}

func TestHealthHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rr := httptest.NewRecorder()
	Handler(&cancelledHealthSource{}, nil, "", nil).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/health", nil).WithContext(ctx))
	if rr.Code != 503 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestHealthVisibilityBoundDoesNotMutateSnapshot(t *testing.T) {
	elements := make([]AuthorityElement, 100)
	for i := range elements {
		elements[i] = AuthorityElement{Element: "ruleset/bypass", Visibility: "hidden"}
	}
	s := &healthSource{health: Health{Healthy: true, Queues: []HealthQueue{{Authority: &Authority{State: "unverified", Elements: elements}}}}}
	rr := httptest.NewRecorder()
	Handler(s, nil, "", nil).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/health", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"elements_omitted":36`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if len(s.health.Queues[0].Authority.Elements) != 100 || s.health.Queues[0].Authority.ElementsOmitted != 0 {
		t.Fatal("health rendering mutated the shared snapshot")
	}
}

func TestHealthProgressThresholdBounds(t *testing.T) {
	for _, limit := range []time.Duration{0, time.Second - time.Nanosecond, 30*time.Minute + time.Nanosecond} {
		settings := DefaultHealthSettings()
		settings.ProgressStallLimit = limit
		if settings.Valid() {
			t.Fatalf("accepted stall limit %s", limit)
		}
	}
	for _, limit := range []time.Duration{time.Second, 150 * time.Second, 30 * time.Minute} {
		settings := DefaultHealthSettings()
		settings.ProgressStallLimit = limit
		if !settings.Valid() {
			t.Fatalf("rejected stall limit %s", limit)
		}
	}
}
