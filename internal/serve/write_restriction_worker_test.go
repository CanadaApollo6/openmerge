package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type workerAuthorityQuota struct{ until time.Time }

func (q *workerAuthorityQuota) Error() string         { return "authority quota exhausted" }
func (q *workerAuthorityQuota) RetryAfter() time.Time { return q.until }

func installWorkerAuthorityReader(t *testing.T, w *worker, read func(context.Context) (authority.Observation, error)) {
	t.Helper()
	original := w.resolveAuthorityLifecycle
	w.resolveAuthorityLifecycle = func(ctx context.Context, candidate config.AuthorityCandidate, branch, tip string) (authorityLifecycleResolution, error) {
		resolved, err := original(ctx, candidate, branch, tip)
		if err != nil {
			return resolved, err
		}
		resolved.Activation.Read = read
		return resolved, nil
	}
}

func hasAuthorityReadError(state queue.State) bool {
	for _, problem := range state.RulesSourceProblems(queue.WriteRestriction) {
		if strings.Contains(problem, authorityReadErrorReason) {
			return true
		}
	}
	return false
}

func TestAuthorityReaderFailureDoesNotAbortCommandIngestion(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := authorityWorkerFixture(t)
		s.options.RulesInterval = time.Minute
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		_, evidence, found := state.AuthorityProtection()
		if !found {
			t.Fatal("fixture did not record authority evidence")
		}

		readerErr := errors.New("authority reader unavailable")
		installWorkerAuthorityReader(t, w, func(ctx context.Context) (authority.Observation, error) {
			if got := forge.ClassOfRequest(ctx); got != forge.RulesRequest {
				t.Fatalf("authority reader request class = %v, want rules", got)
			}
			return forge.EmptyWriteRestrictionObservation(), readerErr
		})
		f.SetNativeReviews(repo, 1, forge.NativeReviews{Comments: []forge.NativeComment{shadowComment(100, "@openmerge queue")}})
		time.Sleep(time.Minute)
		must(t, s.cycle(t.Context(), w))

		state = lifecycleState(t, s, w)
		if !hasQueueReply(f) || len(state.Entries) != 1 {
			t.Fatalf("reader failure stopped PR/comment ingestion: entries=%+v replies=%v", state.Entries, f.Comments(repo, 1))
		}
		if !hasAuthorityReadError(state) {
			t.Fatalf("reader failure was not durable: write_restriction=%v", state.RulesSourceProblems(queue.WriteRestriction))
		}
		response := httptest.NewRecorder()
		status.Handler(s, []store.QueueKey{w.Key}, "", nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/queues", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "write restriction read failed") {
			t.Fatalf("API did not expose durable authority block: %d %s", response.Code, response.Body.String())
		}

		installWorkerAuthorityReader(t, w, func(context.Context) (authority.Observation, error) {
			return evidence, nil
		})
		w.authorityObservation.Read = func(context.Context) (authority.Observation, error) {
			return evidence, nil
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		state = lifecycleState(t, s, w)
		if hasAuthorityReadError(state) {
			t.Fatalf("successful reader did not clear authority read error: %v", state.RulesSourceProblems(queue.WriteRestriction))
		}
		if state.WriteRestrictionStatus().State != "verified" {
			t.Fatalf("successful reader changed authority state: %+v", state.WriteRestrictionStatus())
		}
	})
}

func TestAuthorityReaderTimeoutRecordsReadError(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := authorityWorkerFixture(t)
		s.options.RulesInterval = time.Minute
		must(t, s.cycle(t.Context(), w))
		s.options.ActionTimeout = 10 * time.Millisecond
		w.authorityObservation.Read = func(ctx context.Context) (authority.Observation, error) {
			<-ctx.Done()
			return forge.EmptyWriteRestrictionObservation(), ctx.Err()
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))

		state := lifecycleState(t, s, w)
		if !hasAuthorityReadError(state) {
			t.Fatalf("timeout did not block write_restriction: %v", state.RulesSourceProblems(queue.WriteRestriction))
		}
		events, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		var observed *queue.WriteRestrictionObserved
		for _, row := range events {
			if event, ok := row.Event.(queue.WriteRestrictionObserved); ok {
				recorded := event
				observed = &recorded
			}
		}
		if observed == nil || observed.ReadError != authorityReadErrorReason || observed.Evidence.Inventory != authority.ReadFailure {
			t.Fatalf("timeout observation = %+v", observed)
		}
	})
}

func TestAuthorityReaderFailureBackoffAndQuotaCadence(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := authorityWorkerFixture(t)
		s.options.RulesInterval = time.Minute
		must(t, s.cycle(t.Context(), w))

		calls := 0
		readerErr := errors.New("temporary authority read failure")
		w.authorityObservation.Read = func(ctx context.Context) (authority.Observation, error) {
			calls++
			if got := forge.ClassOfRequest(ctx); got != forge.RulesRequest {
				t.Fatalf("authority reader request class = %v, want rules", got)
			}
			return forge.EmptyWriteRestrictionObservation(), readerErr
		}
		if err := s.observeWorkerAuthority(t.Context(), w); err != nil {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatalf("authority cadence ignored the successful observation deadline: calls=%d", calls)
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 1 {
			t.Fatalf("first authority retry calls=%d, want 1", calls)
		}
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 1 {
			t.Fatalf("authority retry ignored one-minute backoff: calls=%d", calls)
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 2 {
			t.Fatalf("second authority retry calls=%d, want 2", calls)
		}
		time.Sleep(2 * time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 3 {
			t.Fatalf("third authority retry calls=%d, want 3", calls)
		}
		time.Sleep(4 * time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 4 {
			t.Fatalf("fourth authority retry calls=%d, want 4", calls)
		}

		until := time.Now().Add(10 * time.Minute)
		w.authorityObservation.Read = func(ctx context.Context) (authority.Observation, error) {
			calls++
			if got := forge.ClassOfRequest(ctx); got != forge.RulesRequest {
				t.Fatalf("quota reader request class = %v, want rules", got)
			}
			return forge.EmptyWriteRestrictionObservation(), &workerAuthorityQuota{until: until}
		}
		// The fourth failure scheduled a five-minute cap. Reach that retry and
		// prove the quota deadline replaces the ordinary failure delay.
		time.Sleep(5 * time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 5 {
			t.Fatalf("quota authority retry calls=%d, want 5", calls)
		}
		time.Sleep(4 * time.Minute)
		if err := s.observeWorkerAuthority(t.Context(), w); err != nil {
			t.Fatal(err)
		}
		if calls != 5 {
			t.Fatalf("quota RetryAfter was ignored before deadline: calls=%d", calls)
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		if calls != 6 {
			t.Fatalf("quota RetryAfter did not wake authority reader: calls=%d", calls)
		}
	})
}

func TestAuthorityReaderFailureRetainsDefiniteFindings(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := authorityWorkerFixture(t)
		s.options.RulesInterval = time.Minute
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		_, evidence, found := state.AuthorityProtection()
		if !found || len(evidence.Protections) == 0 {
			t.Fatal("fixture did not record authority protection evidence")
		}
		partial := authority.CloneObservation(evidence)
		partial.Findings = []authority.Finding{}
		partial.Protections[0].BypassActors = append(partial.Protections[0].BypassActors, authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"})
		readerErr := errors.New("partial authority read failed")
		w.authorityObservation.Read = func(context.Context) (authority.Observation, error) {
			return partial, readerErr
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		state = lifecycleState(t, s, w)
		before := state.WriteRestrictionStatus().Findings
		if state.WriteRestrictionStatus().State != "violated" || len(before) == 0 {
			t.Fatalf("definite partial finding was not retained: %+v", state.WriteRestrictionStatus())
		}

		w.authorityObservation.Read = func(context.Context) (authority.Observation, error) {
			return forge.EmptyWriteRestrictionObservation(), errors.New("authority read still unavailable")
		}
		time.Sleep(time.Minute)
		must(t, s.observeWorkerAuthority(t.Context(), w))
		after := lifecycleState(t, s, w).WriteRestrictionStatus().Findings
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("read failure cleared definite findings: before=%+v after=%+v", before, after)
		}
	})
}
