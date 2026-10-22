package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func reportingPin(t *testing.T, s *Server, w *worker, mode authority.Mode) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	pin.Authority = mode
	if mode == authority.GitHub {
		pin.OpenMerge = nil
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin}, "")
	must(t, err)
}

func reportingMoves(t *testing.T, s *Server, w *worker, count int) {
	t.Helper()
	events := []queue.Event{queue.BaseMoved{SHA: "initial"}}
	for i := range count {
		events = append(events, queue.BaseMoved{SHA: fmt.Sprintf("outside-%d", i)})
	}
	_, err := s.store.Append(t.Context(), w.id, events, "")
	must(t, err)
	// Independent production audit passes also cross the journal page boundary.
	for range count/100 + 2 {
		must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	s.publishHealth(w, state, true, nil, time.Now())
	must(t, s.refreshOutsideWarnings(t.Context(), w))
}

func reportingResponse(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	status.DynamicHandler(s, s.Keys, "", nil).ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	if rr.Code != 200 {
		t.Fatalf("%s: HTTP %d: %s", path, rr.Code, rr.Body)
	}
	return rr
}

func reportingQueue(t *testing.T, s *Server) status.Queue {
	t.Helper()
	var response status.Response
	must(t, json.Unmarshal(reportingResponse(t, s, "/v1/queues/owner/repo/main").Body.Bytes(), &response))
	return *response.Queue
}

func TestOutsideWarningsLedgerStatusHealthAndAcknowledgement(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	reportingPin(t, s, w, authority.OpenMerge)
	reportingMoves(t, s, w, 1)
	q := reportingQueue(t, s)
	if q.OutsideWarnings == nil || q.OutsideWarnings.Outstanding != 1 || q.OutsideWarnings.Truncated || len(q.OutsideWarnings.EventIDs) != 1 {
		t.Fatalf("missing ledger warning: %+v", q.OutsideWarnings)
	}
	id := q.OutsideWarnings.EventIDs[0]
	history := q.OutsidePushes
	if len(history) != 1 || history[0].EventID != id {
		t.Fatalf("warning/history IDs disagree: %+v", q)
	}
	var response status.Response
	must(t, json.Unmarshal(reportingResponse(t, s, "/v1/queues").Body.Bytes(), &response))
	if !reflect.DeepEqual(response.Queues[0].OutsideWarnings, q.OutsideWarnings) {
		t.Fatal("list lost ledger warning")
	}
	var health status.Health
	must(t, json.Unmarshal(reportingResponse(t, s, "/v1/health").Body.Bytes(), &health))
	if !health.Healthy || !reflect.DeepEqual(health.Queues[0].OutsideWarnings, q.OutsideWarnings) {
		t.Fatalf("warning changed health: %+v", health)
	}
	// Construction restores durable warnings before any reconciliation/audit loop.
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	health = status.Health{}
	must(t, json.Unmarshal(reportingResponse(t, restarted, "/v1/health").Body.Bytes(), &health))
	if !reflect.DeepEqual(health.Queues[0].OutsideWarnings, q.OutsideWarnings) {
		t.Fatal("restart lost warnings")
	}
	_, err = s.store.AcknowledgeOutsidePush(t.Context(), w.id, id, history[0].NewSHA, "operator")
	must(t, err)
	q = reportingQueue(t, s)
	if q.OutsideWarnings != nil || !reflect.DeepEqual(q.OutsidePushes, history) {
		t.Fatalf("acknowledgement removed history or retained warning: %+v", q)
	}
	must(t, s.refreshOutsideWarnings(t.Context(), w))
	health = status.Health{}
	must(t, json.Unmarshal(reportingResponse(t, s, "/v1/health").Body.Bytes(), &health))
	if !health.Healthy || health.Queues[0].OutsideWarnings != nil {
		t.Fatal("acknowledged warning remains in health")
	}
	// A cancelled ledger read cannot replace a known snapshot with a partial zero.
	previous := w.outsideWarnings.Load()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.refreshOutsideWarnings(ctx, w) == nil || w.outsideWarnings.Load().Outstanding != previous.Outstanding || w.outsideWarnings.Load().Error == "" {
		t.Fatal("failed read replaced warning snapshot")
	}
}

func TestOutsideWarningsBoundedAcrossPagesAndAuthorityRollback(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := fixture(t, repositoryConfig)
		reportingPin(t, s, w, authority.OpenMerge)
		const count = 300
		reportingMoves(t, s, w, count)
		q := reportingQueue(t, s)
		if q.OutsideWarnings == nil || q.OutsideWarnings.Outstanding != count || len(q.OutsideWarnings.EventIDs) != store.OutsideWarningIDLimit || !q.OutsideWarnings.Truncated {
			t.Fatalf("unbounded/inexact warnings: %+v", q.OutsideWarnings)
		}
		// The oldest warning is no longer in the recent history but is acknowledgeable.
		id := q.OutsideWarnings.EventIDs[0]
		for _, push := range q.OutsidePushes {
			if push.EventID == id {
				t.Fatal("test did not outgrow recent history")
			}
		}
		_, err := s.store.AcknowledgeOutsidePush(t.Context(), w.id, id, "outside-0", "operator")
		must(t, err)
		reportingPin(t, s, w, authority.GitHub)
		q = reportingQueue(t, s)
		if q.Authority != nil || q.OutsideWarnings == nil || q.OutsideWarnings.Outstanding != count-1 || q.OutsideWarnings.EventIDs[0] == id || len(q.OutsideWarnings.EventIDs) != store.OutsideWarningIDLimit || !q.OutsideWarnings.Truncated {
			t.Fatalf("rollback/ack lost historical eligibility: %+v", q)
		}
		must(t, s.refreshOutsideWarnings(t.Context(), w))
		var health status.Health
		must(t, json.Unmarshal(reportingResponse(t, s, "/v1/health").Body.Bytes(), &health))
		if !reflect.DeepEqual(health.Queues[0].OutsideWarnings, q.OutsideWarnings) {
			t.Fatal("health bounds/count disagree")
		}
	})
}

func TestGitHubOutsidePushHasNoAuthorityWarnings(t *testing.T) {
	for _, pin := range []bool{false, true} {
		t.Run(fmt.Sprint(pin), func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			if pin {
				reportingPin(t, s, w, authority.GitHub)
			}
			reportingMoves(t, s, w, 1)
			q := reportingQueue(t, s)
			if q.Authority != nil || q.OutsideWarnings != nil || len(q.OutsidePushes) != 1 {
				t.Fatalf("GitHub push gained authority warning: %+v", q)
			}
			var health status.Health
			must(t, json.Unmarshal(reportingResponse(t, s, "/v1/health").Body.Bytes(), &health))
			if health.Queues[0].OutsideWarnings != nil {
				t.Fatal("GitHub warning in health")
			}
		})
	}
}

func TestOutsideWarningsAuditLoopPublishesWhileWorkerBusy(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	reportingPin(t, s, w, authority.OpenMerge)
	reportingMoves(t, s, w, 1)
	warning := w.outsideWarnings.Load()
	_, err := s.store.AcknowledgeOutsidePush(t.Context(), w.id, warning.EventIDs[0], "outside-0", "operator")
	must(t, err)
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.outsidePushLoop(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for w.outsideWarnings.Load().Outstanding != 0 {
		select {
		case <-deadline.C:
			t.Fatal("audit loop failed to publish acknowledgement while worker busy")
		case <-ticker.C:
		}
	}
	var health status.Health
	must(t, json.Unmarshal(reportingResponse(t, s, "/v1/health").Body.Bytes(), &health))
	if !health.Healthy || health.Queues[0].OutsideWarnings != nil {
		t.Fatal("health lost refreshed ledger")
	}
}

func TestOutsideWarningErrorPreservesRegistration(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	reportingPin(t, s, w, authority.OpenMerge)
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "orphan", At: time.Now(), VerificationBeforeLanding: "none"}}, "")
	must(t, err)
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	if len(restarted.queues) != 1 || restarted.queues[0].outsideWarnings.Load().Error != "ledger_unavailable" {
		t.Fatal("registration lost queue or ledger error")
	}
	registered, err := newServer(s.store, s.options)
	must(t, err)
	restored, err := registered.registerID(t.Context(), w.Target, w.id)
	must(t, err)
	if restored.outsideWarnings.Load().Error != "ledger_unavailable" {
		t.Fatal("managed registration hid ledger failure")
	}
	q := reportingQueue(t, restarted)
	if q.OutsideWarnings == nil || q.OutsideWarnings.Error != "ledger_unavailable" {
		t.Fatal("status did not isolate ledger failure")
	}
}

func TestOutsideWarningsClearAfterPinRemoval(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	reportingPin(t, s, w, authority.OpenMerge)
	reportingMoves(t, s, w, 1)
	// Publish a new unpinned projection, as after replacing the queue's history.
	unpinned := queue.State{BaseBranch: w.Key.BaseBranch}
	s.publishHealth(w, unpinned, true, nil, time.Now())
	must(t, s.refreshOutsideWarnings(t.Context(), w))
	q, _, _ := status.Project(store.Journal{Key: w.Key, Seed: unpinned}, 0, 0, nil)
	h, err := s.Health(t.Context())
	must(t, err)
	if q.OutsideWarnings != nil || h.Queues[0].OutsideWarnings != nil || w.outsideWarnings.Load() != nil {
		t.Fatalf("frozen warning after pin removal: status=%+v health=%+v", q.OutsideWarnings, h.Queues[0].OutsideWarnings)
	}
}

func TestOutsideWarningsRefreshAfterRollback(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	reportingPin(t, s, w, authority.OpenMerge)
	reportingMoves(t, s, w, 1)
	warning := w.outsideWarnings.Load()
	reportingPin(t, s, w, authority.GitHub)
	_, err := s.store.AcknowledgeOutsidePush(t.Context(), w.id, warning.EventIDs[0], "outside-0", "operator")
	must(t, err)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	s.publishHealth(w, state, true, nil, time.Now())
	must(t, s.refreshOutsideWarnings(t.Context(), w))
	q := reportingQueue(t, s)
	h, err := s.Health(t.Context())
	must(t, err)
	if q.OutsideWarnings != nil || h.Queues[0].OutsideWarnings != nil {
		t.Fatalf("frozen warning after rollback: status=%+v health=%+v", q.OutsideWarnings, h.Queues[0].OutsideWarnings)
	}
}
