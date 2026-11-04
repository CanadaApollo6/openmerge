package serve

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

func TestStatusSourceSavingsJournalMatchesCLI(t *testing.T) {
	db, conn := databaseConnection(t)
	s, err := newServer(db, Options{})
	must(t, err)
	key := store.QueueKey{Installation: "1", Repository: "fixture/app", BaseBranch: "main", Lane: "fast"}
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	at := time.Now().UTC().Truncate(time.Microsecond)
	must(t, db.InitializeFence(t.Context(), id, "initial-fence"))
	_, err = db.Append(t.Context(), id, []queue.Event{
		queue.BaseMoved{SHA: "base"},
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 1, At: at},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: at},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: at},
		queue.ChecksCompleted{TestedSHA: "tested", Success: true},
	}, "savings-fixture")
	must(t, err)
	ledger := treededupe.Ledger{}
	ledger.ObserveBuild(treededupe.Build{At: at.Add(-3 * time.Minute), BatchID: "source", SHA: strings.Repeat("a", 40), Kind: treededupe.KindBatch, Key: "same-tree"})
	ledger.ObserveOutcome(strings.Repeat("a", 40), at.Add(-2*time.Minute), true)
	ledger.ObserveBuild(treededupe.Build{At: at.Add(-time.Minute), BatchID: "hit", SHA: strings.Repeat("b", 40), Kind: treededupe.KindBatch, Key: "same-tree"})
	raw, err := ledger.Encode()
	must(t, err)
	must(t, db.UpdateTreeDedupeShadow(t.Context(), id, func([]byte) ([]byte, error) { return raw, nil }))
	var token [32]byte
	token[0] = 1
	binding, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", key.Installation)
	must(t, err)
	depSide(t, db, id, binding, "savings-binding", binding)
	var schema string
	must(t, conn.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema))
	u, err := url.Parse(os.Getenv("OPENMERGE_TEST_DATABASE_URL"))
	must(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	fromCLI, err := queuemetrics.ReadJournal(t.Context(), u.String(), key, time.Minute)
	must(t, err)
	window := queuemetrics.SavingsWindow{Since: at.Add(-time.Hour), Until: time.Now().UTC(), FullSuiteMinutes: 20}
	want, err := queuemetrics.ComputeSavings(fromCLI, window)
	must(t, err)
	if want.Shadow == nil || want.Shadow.WouldReuseSuccess != 1 || want.Runs.Executed != 1 {
		t.Fatalf("fixture lacks executed or shadow savings: %+v", want)
	}
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint("dependency-active-", active), func(t *testing.T) {
			s.queues = nil
			if active {
				s.queues = []*worker{{Key: key, dependencies: &dependencyPass{binding: binding}}}
			}
			journals, err := s.Journals(t.Context(), []store.QueueKey{key})
			must(t, err)
			if len(journals) != 1 {
				t.Fatalf("got %d journals", len(journals))
			}
			got, err := queuemetrics.ComputeSavings(journals[0], window)
			must(t, err)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Server.Journals savings differ from CLI: got %+v, want %+v", got, want)
			}
			persisted := journals[0]
			// These are the only additive dependency presentation fields.
			persisted.DependencyQueue = fromCLI.DependencyQueue
			persisted.DependencyRedacted = fromCLI.DependencyRedacted
			persisted.DependencyStatus = fromCLI.DependencyStatus
			if !reflect.DeepEqual(persisted, fromCLI) {
				t.Fatal("Server.Journals differs from queuemetrics.ReadJournal")
			}
			target := "/v1/queues/fixture/app/main/reports/savings?since=" + url.QueryEscape(window.Since.Format(time.RFC3339Nano)) + "&until=" + url.QueryEscape(window.Until.Format(time.RFC3339Nano)) + "&full-suite-minutes=20"
			request := httptest.NewRequest("GET", target, nil)
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			status.Handler(s, []store.QueueKey{key}, "secret", nil).ServeHTTP(response, request)
			var body status.SavingsResponse
			must(t, json.Unmarshal(response.Body.Bytes(), &body))
			if response.Code != 200 || !reflect.DeepEqual(body.Savings, want) {
				t.Fatalf("API differs from CLI: %d %s", response.Code, response.Body)
			}
		})
	}
}
