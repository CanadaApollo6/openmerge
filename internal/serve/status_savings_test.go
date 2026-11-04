package serve

import (
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStatusSourceSavingsJournalMatchesCLI(t *testing.T) {
	db, conn := databaseConnection(t)
	s, err := newServer(db, Options{})
	must(t, err)
	key := store.QueueKey{Installation: "1", Repository: "fixture/app", BaseBranch: "main"}
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), id, []queue.Event{queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now().UTC()}}, "")
	must(t, err)
	journals, err := s.Journals(t.Context(), []store.QueueKey{key})
	must(t, err)
	var schema string
	must(t, conn.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema))
	u, err := url.Parse(os.Getenv("OPENMERGE_TEST_DATABASE_URL"))
	must(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	fromCLI, err := queuemetrics.ReadJournal(t.Context(), u.String(), key, time.Minute)
	must(t, err)
	if len(journals) != 1 || !reflect.DeepEqual(journals[0], fromCLI) {
		t.Fatal("Server.Journals differs from queuemetrics.ReadJournal")
	}
}
