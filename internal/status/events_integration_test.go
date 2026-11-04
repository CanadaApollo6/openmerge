package status

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestQueueEventsStoredCursorSurvivesReaderRestart(t *testing.T) {
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL to run journal-backed event pagination")
	}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := pgx.Connect(t.Context(), raw)
	require(err)
	schema := fmt.Sprintf("queue_events_%d", time.Now().UnixNano())
	_, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require(err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require(err)
		require(admin.Close(ctx))
	})
	u, err := url.Parse(raw)
	require(err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := store.Open(t.Context(), u.String(), store.WithTestLockScope(schema))
	require(err)
	t.Cleanup(db.Close)
	id, err := db.EnsureQueue(t.Context(), key)
	require(err)
	_, err = db.Append(t.Context(), id, admission(), "")
	require(err)
	other := key
	other.Repository = "other/repo"
	otherID, err := db.EnsureQueue(t.Context(), other)
	require(err)
	_, err = db.Append(t.Context(), otherID, []queue.Event{queue.RefreshRequested{}}, "")
	require(err)
	end, err := db.Append(t.Context(), id, []queue.Event{queue.PRMetadataObserved{PR: 1, Title: "untrusted", ApprovedReviewsBy: []string{}, Labels: []string{}, Files: []string{}}, queue.PauseRequested{Reason: "operator", Detail: "secret"}}, "")
	require(err)
	db.Close()
	reader, err := store.OpenJournalReader(t.Context(), u.String(), 15*time.Second)
	require(err)
	_, err = reader.Journals(t.Context(), []store.QueueKey{key})
	require(err)
	page := readEvents(t, Handler(reader, []store.QueueKey{key}, "secret", nil), "?limit=3")
	reader.Close()
	if !page.HasMore || page.NextCursor != 3 {
		t.Fatal(page)
	}
	reader, err = store.OpenJournalReader(t.Context(), u.String(), 15*time.Second)
	require(err)
	defer reader.Close()
	next := readEvents(t, Handler(reader, []store.QueueKey{key}, "secret", nil), fmt.Sprintf("?after=%d&limit=3", page.NextCursor))
	if next.HasMore || next.NextCursor != end {
		t.Fatal(next)
	}
	var ids []int64
	for _, event := range append(page.Events, next.Events...) {
		ids = append(ids, event.ID)
	}
	if !reflect.DeepEqual(ids, []int64{1, 2, 3, 5, 6}) {
		t.Fatal("journal IDs lost across reader restart or another queue leaked", ids)
	}
}
